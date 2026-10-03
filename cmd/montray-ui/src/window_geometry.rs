use std::cell::Cell;
use std::rc::Rc;

use anyhow::{Context, Result};
use slint::winit_030::{EventResult, WinitWindowAccessor, winit::event::WindowEvent};

use crate::persistence::{Store, WindowGeometry};

/// Coordinates native placement across startup, hide/show, and window-manager events.
///
/// This type is intentionally UI-thread-only (`Rc<Cell<_>>`). Winit reports
/// maximized bounds as ordinary resize events, so normal geometry and display
/// state must be tracked separately to avoid restoring a maximized rectangle as
/// the next normal size.
#[derive(Clone)]
pub struct WindowGeometryManager {
    store: Store,
    /// Most recently observed non-maximized, non-fullscreen physical bounds.
    last_normal: Rc<Cell<Option<WindowGeometry>>>,
    /// Suppresses redundant JSON rewrites during noisy move/resize sequences.
    last_persisted: Rc<Cell<Option<WindowGeometry>>>,
    /// Placement being restored; also gates misleading restoration-generated events.
    pending_show: Rc<Cell<Option<WindowGeometry>>>,
}

impl WindowGeometryManager {
    pub fn new(store: Store, saved: Option<WindowGeometry>) -> Self {
        Self {
            store,
            last_normal: Rc::new(Cell::new(saved.map(WindowGeometry::as_normal))),
            last_persisted: Rc::new(Cell::new(saved)),
            pending_show: Rc::new(Cell::new(saved)),
        }
    }

    /// Installs a passive Winit observer while preserving Slint's own handling.
    pub fn install_event_handler(&self, window: &slint::Window) {
        let manager = self.clone();
        window.on_winit_window_event(move |window, event| {
            manager.handle_winit_event(window, event);
            EventResult::Propagate
        });
    }

    /// Applies saved placement before mapping, then requests one scale-aware correction.
    pub fn show(&self, window: &slint::Window) -> Result<()> {
        let placement = self.pending_show.get();
        if let Some(placement) = placement {
            // Prepare the final placement while the window is still hidden. On
            // first show Winit puts this directly into the native window's
            // creation attributes; on later shows hide() has already restored
            // the normal rectangle while the window was unmapped.
            self.last_normal.set(Some(placement.as_normal()));
            window.set_maximized(false);
            apply_window_geometry(window, placement);
        }
        window.show().context("failed to show native window")?;
        if self.pending_show.get().is_some() {
            // Keep pending_show set until the first redraw so move/resize events
            // produced by restoration cannot replace the saved normal bounds.
            window.request_redraw();
        }
        Ok(())
    }

    /// Captures/persists placement before unmapping and preserves maximized restore bounds.
    pub fn hide(&self, window: &slint::Window) -> Result<()> {
        let placement = self.capture_placement(window);
        let save_result = self.persist(placement);
        self.pending_show.set(placement);
        window.hide().context("failed to hide native window")?;

        // Some window managers discard their restore rectangle when a maximized
        // window is unmapped. Re-establish it while the window is hidden.
        if placement.is_some_and(|placement| placement.maximized) {
            window.set_maximized(false);
            self.apply_last_normal(window);
        }
        save_result
    }

    pub fn save(&self, window: &slint::Window) -> Result<()> {
        let placement = self.capture_placement(window);
        self.persist(placement)
    }

    /// Avoids rewriting the state file for the stream of duplicate move/resize
    /// notifications emitted by some window managers.
    fn persist(&self, placement: Option<WindowGeometry>) -> Result<()> {
        let Some(placement) = placement else {
            return Ok(());
        };
        if self.last_persisted.get() == Some(placement) {
            return Ok(());
        }

        self.store.update(|state| {
            state.preferences.window_geometry = Some(placement);
            Ok(())
        })?;
        self.last_persisted.set(Some(placement));
        Ok(())
    }

    /// Captures normal bounds plus current display state without unmapping.
    fn capture_placement(&self, window: &slint::Window) -> Option<WindowGeometry> {
        let (maximized, fullscreen) = native_display_state(window);
        if is_normal_window_state(maximized, fullscreen) {
            self.observe_winit_window(window);
        }

        self.last_normal.get().map(|mut placement| {
            placement.maximized = maximized;
            placement
        })
    }

    /// Ignores synthetic restoration events until the first visible redraw completes.
    fn handle_winit_event(&self, window: &slint::Window, event: &WindowEvent) {
        if matches!(event, WindowEvent::RedrawRequested)
            && window.is_visible()
            && let Some(placement) = self.pending_show.take()
        {
            // On first creation the monitor scale is not known before show(),
            // so a saved physical size may only have been approximated. Apply
            // it once more with the real scale, and restore maximization here
            // because X11 may overwrite an earlier maximized request.
            window.set_maximized(false);
            apply_window_geometry(window, placement);
            window.set_maximized(placement.maximized);
            return;
        }
        if self.pending_show.get().is_some()
            || !matches!(event, WindowEvent::Moved(_) | WindowEvent::Resized(_))
        {
            return;
        }
        self.observe_winit_window(window);
    }

    /// Records physical outer position and inner size only in normal display state.
    ///
    /// Position and size use the native values available consistently across the
    /// supported Winit backends. Errors and zero-sized transitional states are ignored.
    fn observe_winit_window(&self, window: &slint::Window) {
        let geometry = window.with_winit_window(|window| {
            if window.is_maximized() || window.fullscreen().is_some() {
                return None;
            }
            let position = window.outer_position().ok()?;
            let size = window.inner_size();
            (size.width > 0 && size.height > 0).then_some(WindowGeometry {
                x: position.x,
                y: position.y,
                width: size.width,
                height: size.height,
                maximized: false,
            })
        });
        if let Some(Some(geometry)) = geometry {
            self.last_normal.set(Some(geometry));
        }
    }

    fn apply_last_normal(&self, window: &slint::Window) {
        if let Some(geometry) = self.last_normal.get() {
            apply_window_geometry(window, geometry);
        }
    }
}

/// Prefers Winit's native state but falls back when a native handle is unavailable.
fn native_display_state(window: &slint::Window) -> (bool, bool) {
    window
        .with_winit_window(|window| (window.is_maximized(), window.fullscreen().is_some()))
        .unwrap_or_else(|| (window.is_maximized(), window.is_fullscreen()))
}

/// Applies nonzero physical geometry; monitor clamping remains the window manager's job.
fn apply_window_geometry(window: &slint::Window, geometry: WindowGeometry) {
    if geometry.width == 0 || geometry.height == 0 {
        return;
    }
    window.set_size(slint::PhysicalSize::new(geometry.width, geometry.height));
    window.set_position(slint::PhysicalPosition::new(geometry.x, geometry.y));
}

fn is_normal_window_state(maximized: bool, fullscreen: bool) -> bool {
    !maximized && !fullscreen
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn maximized_and_fullscreen_bounds_are_not_normal_geometry() {
        assert!(is_normal_window_state(false, false));
        assert!(!is_normal_window_state(true, false));
        assert!(!is_normal_window_state(false, true));
        assert!(!is_normal_window_state(true, true));
    }

    #[test]
    fn saved_placement_is_split_into_normal_bounds_and_display_state() {
        let placement = WindowGeometry {
            x: 10,
            y: 20,
            width: 800,
            height: 600,
            maximized: true,
        };
        let manager =
            WindowGeometryManager::new(Store::new(PathBuf::from("unused")), Some(placement));

        assert_eq!(manager.last_normal.get(), Some(placement.as_normal()));
        assert_eq!(manager.pending_show.get(), Some(placement));
    }
}
