fn main() {
    if let Err(error) = montray_ui::app::execute() {
        if montray_ui::logging::is_initialized() {
            log::error!("{error:#}");
        } else {
            eprintln!("montray-ui: {error:#}");
        }
        std::process::exit(1);
    }
}
