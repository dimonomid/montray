//! Native Montray UI status watcher.
//!
//! Slint remains on the process main thread, while [`runtime`] owns a dedicated
//! current-thread Tokio runtime. Domain transitions are isolated in [`domain`];
//! all filesystem, notification, and UI work is performed from explicit effects
//! or projection boundaries.

/// Process startup, Slint wiring, tray callbacks, and ordered shutdown.
pub mod app;
/// Secure bearer credential generation and operator instructions.
pub mod bearer_token;
/// Compile-time version and build provenance.
pub mod build_info;
/// Side-effect-free command-line parsing.
pub mod cli;
/// YAML schema and cross-field validation.
pub mod config;
/// Deterministic application state, events, effects, and reducer.
pub mod domain;
/// Application-scoped stderr logging.
pub mod logging;
/// WebSocket protocol, transport, and external SSH supervision.
pub mod network;
/// Desktop notification boundary.
pub mod notification;
/// Forward-compatible preferences and snooze persistence.
pub mod persistence;
/// Dedicated Tokio thread and domain event pump.
pub mod runtime;
/// Linux desktop integration installer.
pub mod setup;
/// Tray icon composition and stable flashing state machine.
pub mod tray;
/// Projection from domain snapshots into Slint models.
pub mod ui;
/// Native normal/maximized window placement tracking.
pub mod window_geometry;
