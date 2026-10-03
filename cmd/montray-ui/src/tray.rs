use anyhow::{Context, Result, bail};
use image::{Rgba, RgbaImage};
use slint::{Image, Rgba8Pixel, SharedPixelBuffer};
use std::sync::{Arc, Mutex};

const UNKNOWN_ICON: &[u8] = include_bytes!("../assets/tray/gray.png");
const OK_ICON: &[u8] = include_bytes!("../assets/tray/green.png");
const INTERNAL_ERROR_ICON: &[u8] = include_bytes!("../assets/tray/magenta.png");
const WARNING_ICON: &[u8] = include_bytes!("../assets/tray/yellow.png");
const ERROR_ICON: &[u8] = include_bytes!("../assets/tray/red.png");
const TRANSPARENT_ICON: &[u8] = include_bytes!("../assets/tray/transparent.png");

/// Tray rendering severity, kept UI-local so image policy does not enter the domain.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum OverallState {
    Unknown,
    Ok,
    InternalError,
    Warning,
    Error,
}

/// Complete visual and menu state needed to render the tray.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct TrayState {
    pub alerting: OverallState,
    /// Uninitialized fraction used only while `alerting` is `Unknown`.
    pub unknown_server_count: usize,
    pub server_count: usize,
    /// Visible count used by menu text, intentionally excluded from icon identity.
    pub alerting_count: usize,
    /// Highest hidden severity rendered as the lower-right overlay.
    pub snoozed: Option<OverallState>,
    pub snoozed_count: usize,
}

impl TrayState {
    pub fn is_flashing(self) -> bool {
        !matches!(self.alerting, OverallState::Unknown | OverallState::Ok)
    }

    /// Returns the visual identity relevant to icon pixels and flash cadence.
    ///
    /// Counts that affect only menu text are excluded so timestamp refreshes or
    /// equivalent incident replacements cannot restart an active flash cycle.
    fn icon_identity(self) -> TrayIconIdentity {
        if self.alerting == OverallState::Unknown {
            TrayIconIdentity::Unknown {
                unknown_server_count: self.unknown_server_count,
                server_count: self.server_count,
                snoozed: self.snoozed,
            }
        } else {
            TrayIconIdentity::Known {
                alerting: self.alerting,
                snoozed: self.snoozed,
            }
        }
    }

    pub fn status_title(self) -> String {
        let noun = if self.alerting_count == 1 {
            "incident"
        } else {
            "incidents"
        };
        let mut title = format!("Status: {} {noun}", self.alerting_count);
        if self.snoozed_count > 0 {
            title.push_str(&format!(" + {} snoozed", self.snoozed_count));
        }
        title
    }
}

/// Pixel-relevant state used to decide whether an icon genuinely changed.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum TrayIconIdentity {
    Unknown {
        unknown_server_count: usize,
        server_count: usize,
        snoozed: Option<OverallState>,
    },
    Known {
        alerting: OverallState,
        snoozed: Option<OverallState>,
    },
}

/// Opaque token tying scheduled timer callbacks to one icon generation.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct FlashCycle {
    generation: u64,
}

/// Mutable phase protected for use by UI callbacks carrying cloned controllers.
#[derive(Debug, Default)]
struct FlashState {
    icon: Option<TrayIconIdentity>,
    flashing: bool,
    /// True selects the colored icon; false selects the transparent image.
    showing_solid: bool,
    /// Invalidates already scheduled callbacks whenever visual identity changes.
    generation: u64,
}

/// Shared edge-triggered state machine for stable tray flashing.
#[derive(Clone, Debug, Default)]
pub struct TrayFlashController {
    state: Arc<Mutex<FlashState>>,
}

impl TrayFlashController {
    /// Records a rendered tray state. `Some` starts a new flash cycle; `None`
    /// means the existing icon and cadence must be left untouched.
    pub fn apply(&self, state: TrayState) -> Option<FlashCycle> {
        let mut current = self.state.lock().unwrap_or_else(|error| error.into_inner());
        let icon = state.icon_identity();
        if current.icon == Some(icon) {
            return None;
        }
        current.icon = Some(icon);
        current.flashing = state.is_flashing();
        current.showing_solid = true;
        current.generation = current.generation.wrapping_add(1);
        Some(FlashCycle {
            generation: current.generation,
        })
    }

    /// Advances an active cycle. Returns whether the solid icon should be
    /// shown, or `None` if this cycle was replaced or flashing has stopped.
    pub fn tick(&self, cycle: FlashCycle) -> Option<bool> {
        let mut current = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if current.generation != cycle.generation || !current.flashing {
            return None;
        }
        current.showing_solid = !current.showing_solid;
        Some(current.showing_solid)
    }

    /// Checks whether a just-created cycle still owns an active flash cadence.
    /// This generation check prevents an obsolete timer from restarting or
    /// perturbing the phase after a newer tray state has replaced it.
    pub fn is_flashing(&self, cycle: FlashCycle) -> bool {
        let current = self.state.lock().unwrap_or_else(|error| error.into_inner());
        current.generation == cycle.generation && current.flashing
    }
}

/// Decoded source icons and composition operations used by the tray projection.
pub struct TrayIcons {
    unknown: RgbaImage,
    ok: RgbaImage,
    internal_error: RgbaImage,
    warning: RgbaImage,
    error: RgbaImage,
    transparent: RgbaImage,
}

impl TrayIcons {
    /// Decodes all embedded icons up front so startup reports broken assets early.
    pub fn load() -> Result<Self> {
        Ok(Self {
            unknown: decode_icon(UNKNOWN_ICON)?,
            ok: decode_icon(OK_ICON)?,
            internal_error: decode_icon(INTERNAL_ERROR_ICON)?,
            warning: decode_icon(WARNING_ICON)?,
            error: decode_icon(ERROR_ICON)?,
            transparent: decode_icon(TRANSPARENT_ICON)?,
        })
    }

    pub fn icon(&self, state: TrayState) -> Result<Image> {
        Ok(to_slint_image(&self.rgba_icon(state)?))
    }

    pub fn transparent_icon(&self) -> Image {
        to_slint_image(&self.transparent)
    }

    /// Renders initialization progress and the optional snoozed-severity overlay.
    fn rgba_icon(&self, state: TrayState) -> Result<RgbaImage> {
        let mut icon = if state.alerting == OverallState::Unknown {
            compose_initialization_icon(
                &self.ok,
                &self.unknown,
                state.unknown_server_count,
                state.server_count,
            )?
        } else {
            self.for_state(state.alerting).clone()
        };
        if let Some(snoozed) = state.snoozed {
            icon = compose_overlay(&icon, self.for_state(snoozed))?;
        }
        Ok(icon)
    }

    fn for_state(&self, state: OverallState) -> &RgbaImage {
        match state {
            OverallState::Unknown => &self.unknown,
            OverallState::Ok => &self.ok,
            OverallState::InternalError => &self.internal_error,
            OverallState::Warning => &self.warning,
            OverallState::Error => &self.error,
        }
    }
}

fn decode_icon(bytes: &[u8]) -> Result<RgbaImage> {
    Ok(image::load_from_memory(bytes)
        .context("failed to decode tray icon")?
        .into_rgba8())
}

fn to_slint_image(image: &RgbaImage) -> Image {
    let buffer = SharedPixelBuffer::<Rgba8Pixel>::clone_from_slice(
        image.as_raw(),
        image.width(),
        image.height(),
    );
    Image::from_rgba8(buffer)
}

/// Renders the known-server fraction as a clockwise pie starting at 12 o'clock.
///
/// Four-by-four subpixel sampling provides a stable antialiased boundary. The
/// source images must share alpha exactly so only color—not silhouette—changes.
fn compose_initialization_icon(
    ok: &RgbaImage,
    unknown: &RgbaImage,
    unknown_server_count: usize,
    server_count: usize,
) -> Result<RgbaImage> {
    if unknown_server_count == 0 && server_count > 0 {
        return Ok(ok.clone());
    }
    if server_count == 0 || unknown_server_count >= server_count {
        return Ok(unknown.clone());
    }
    ensure_matching_dimensions(ok, unknown)?;

    const SAMPLES_PER_AXIS: usize = 4;
    const TOTAL_SAMPLES: usize = SAMPLES_PER_AXIS * SAMPLES_PER_AXIS;
    let center_x = f64::from(ok.width()) / 2.0;
    let center_y = f64::from(ok.height()) / 2.0;
    let known_angle =
        std::f64::consts::TAU * (server_count - unknown_server_count) as f64 / server_count as f64;
    let mut result = RgbaImage::new(ok.width(), ok.height());

    for y in 0..ok.height() {
        for x in 0..ok.width() {
            let mut unknown_samples = 0usize;
            for sample_y in 0..SAMPLES_PER_AXIS {
                for sample_x in 0..SAMPLES_PER_AXIS {
                    let x_offset =
                        f64::from(x) + (sample_x as f64 + 0.5) / SAMPLES_PER_AXIS as f64 - center_x;
                    let y_offset =
                        f64::from(y) + (sample_y as f64 + 0.5) / SAMPLES_PER_AXIS as f64 - center_y;
                    let mut angle = x_offset.atan2(-y_offset);
                    if angle < 0.0 {
                        angle += std::f64::consts::TAU;
                    }
                    if angle >= known_angle {
                        unknown_samples += 1;
                    }
                }
            }

            let known_samples = TOTAL_SAMPLES - unknown_samples;
            let ok_pixel = ok.get_pixel(x, y);
            let unknown_pixel = unknown.get_pixel(x, y);
            if ok_pixel[3] != unknown_pixel[3] {
                bail!("initialization tray icon alpha channels differ at ({x}, {y})");
            }
            let mix = |ok_value: u8, unknown_value: u8| {
                ((usize::from(ok_value) * known_samples
                    + usize::from(unknown_value) * unknown_samples)
                    / TOTAL_SAMPLES) as u8
            };
            result.put_pixel(
                x,
                y,
                Rgba([
                    mix(ok_pixel[0], unknown_pixel[0]),
                    mix(ok_pixel[1], unknown_pixel[1]),
                    mix(ok_pixel[2], unknown_pixel[2]),
                    ok_pixel[3],
                ]),
            );
        }
    }
    Ok(result)
}

/// Scales a severity icon into the lower-right 30% and alpha-composites it.
fn compose_overlay(base: &RgbaImage, overlay: &RgbaImage) -> Result<RgbaImage> {
    if base.width() == 0 || base.height() == 0 || overlay.width() == 0 || overlay.height() == 0 {
        bail!("tray icons must not be empty");
    }
    let mut result = base.clone();
    let size = base.width() * 30 / 100;
    if size == 0 {
        return Ok(result);
    }
    let start_x = base.width() - size;
    let start_y = base.height() - size;

    for y in 0..size {
        for x in 0..size {
            let source_x = x * overlay.width() / size;
            let source_y = y * overlay.height() / size;
            let foreground = overlay.get_pixel(source_x, source_y);
            let background = result.get_pixel(start_x + x, start_y + y);
            result.put_pixel(
                start_x + x,
                start_y + y,
                alpha_over(*foreground, *background),
            );
        }
    }
    Ok(result)
}

/// Composites straight-alpha RGBA pixels and returns straight-alpha output.
fn alpha_over(foreground: Rgba<u8>, background: Rgba<u8>) -> Rgba<u8> {
    let foreground_alpha = u32::from(foreground[3]);
    let background_alpha = u32::from(background[3]);
    let output_alpha = foreground_alpha + background_alpha * (255 - foreground_alpha) / 255;
    if output_alpha == 0 {
        return Rgba([0, 0, 0, 0]);
    }
    let channel = |index: usize| {
        let premultiplied = u32::from(foreground[index]) * foreground_alpha
            + u32::from(background[index]) * background_alpha * (255 - foreground_alpha) / 255;
        (premultiplied / output_alpha) as u8
    };
    Rgba([channel(0), channel(1), channel(2), output_alpha as u8])
}

fn ensure_matching_dimensions(first: &RgbaImage, second: &RgbaImage) -> Result<()> {
    if first.dimensions() != second.dimensions() {
        bail!(
            "tray icon dimensions differ: {:?} and {:?}",
            first.dimensions(),
            second.dimensions()
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_title_uses_expected_counts() {
        let mut state = mock_state();
        state.alerting_count = 0;
        state.snoozed_count = 0;
        assert_eq!(state.status_title(), "Status: 0 incidents");
        state.alerting_count = 1;
        assert_eq!(state.status_title(), "Status: 1 incident");
        state.alerting_count = 2;
        state.snoozed_count = 1;
        assert_eq!(state.status_title(), "Status: 2 incidents + 1 snoozed");
    }

    #[test]
    fn alert_states_flash_but_ok_and_unknown_do_not() {
        let mut state = mock_state();
        for overall in [
            OverallState::InternalError,
            OverallState::Warning,
            OverallState::Error,
        ] {
            state.alerting = overall;
            assert!(state.is_flashing());
        }
        for overall in [OverallState::Unknown, OverallState::Ok] {
            state.alerting = overall;
            assert!(!state.is_flashing());
        }
    }

    #[test]
    fn unchanged_flashing_icon_preserves_its_existing_phase() {
        let controller = TrayFlashController::default();
        let state = mock_state();
        let cycle = controller.apply(state).unwrap();

        assert_eq!(controller.tick(cycle), Some(false));

        let count_only_update = TrayState {
            alerting_count: state.alerting_count + 1,
            snoozed_count: state.snoozed_count + 1,
            ..state
        };
        assert_eq!(controller.apply(count_only_update), None);
        assert_eq!(controller.tick(cycle), Some(true));
    }

    #[test]
    fn changed_icon_restarts_flashing_from_a_full_solid_phase() {
        let controller = TrayFlashController::default();
        let warning_cycle = controller.apply(mock_state()).unwrap();
        assert_eq!(controller.tick(warning_cycle), Some(false));

        let error_cycle = controller
            .apply(TrayState {
                alerting: OverallState::Error,
                ..mock_state()
            })
            .unwrap();
        assert_eq!(controller.tick(warning_cycle), None);
        assert_eq!(controller.tick(error_cycle), Some(false));

        controller
            .apply(TrayState {
                alerting: OverallState::Ok,
                ..mock_state()
            })
            .unwrap();
        assert_eq!(controller.tick(error_cycle), None);
    }

    #[test]
    fn irrelevant_server_counts_do_not_restart_an_alerting_icon() {
        let controller = TrayFlashController::default();
        let state = mock_state();
        controller.apply(state).unwrap();

        assert_eq!(
            controller.apply(TrayState {
                unknown_server_count: 1,
                server_count: 3,
                ..state
            }),
            None
        );
    }

    #[test]
    fn generated_icons_have_a_valid_slint_rgba_buffer() {
        let icons = TrayIcons::load().unwrap();
        let image = icons.icon(mock_state()).unwrap();
        let rgba = image
            .to_rgba8()
            .expect("tray image must expose RGBA pixels");

        assert_eq!((rgba.width(), rgba.height()), (40, 40));
        assert_eq!(rgba.as_bytes().len(), 40 * 40 * 4);
    }

    #[test]
    fn initialization_icon_uses_known_fraction() {
        let icons = TrayIcons::load().unwrap();
        let base_state = TrayState {
            snoozed: None,
            snoozed_count: 0,
            ..mock_state()
        };
        let all_unknown = icons
            .rgba_icon(TrayState {
                alerting: OverallState::Unknown,
                unknown_server_count: 4,
                server_count: 4,
                ..base_state
            })
            .unwrap();
        let all_known = icons
            .rgba_icon(TrayState {
                alerting: OverallState::Unknown,
                unknown_server_count: 0,
                server_count: 4,
                ..base_state
            })
            .unwrap();
        assert_eq!(all_unknown, icons.unknown);
        assert_eq!(all_known, icons.ok);

        let one_unknown = icons
            .rgba_icon(TrayState {
                alerting: OverallState::Unknown,
                unknown_server_count: 1,
                server_count: 4,
                ..base_state
            })
            .unwrap();
        assert_eq!(one_unknown.get_pixel(25, 10), icons.ok.get_pixel(25, 10));
        assert_eq!(
            one_unknown.get_pixel(10, 15),
            icons.unknown.get_pixel(10, 15)
        );
    }

    #[test]
    fn overlay_respects_transparency() {
        let base = RgbaImage::from_pixel(4, 4, Rgba([0, 0, 255, 255]));
        let mut overlay = RgbaImage::from_pixel(4, 4, Rgba([0, 0, 0, 0]));
        overlay.put_pixel(0, 0, Rgba([255, 0, 0, 128]));
        let result = compose_overlay(&base, &overlay).unwrap();

        assert_eq!(*result.get_pixel(2, 2), Rgba([0, 0, 255, 255]));
        let mixed = result.get_pixel(3, 3);
        assert_eq!(mixed[3], 255);
        assert!(mixed[0] > 0);
        assert!(mixed[2] > 0);
    }

    fn mock_state() -> TrayState {
        TrayState {
            alerting: OverallState::Warning,
            unknown_server_count: 0,
            server_count: 2,
            alerting_count: 2,
            snoozed: Some(OverallState::Warning),
            snoozed_count: 1,
        }
    }
}
