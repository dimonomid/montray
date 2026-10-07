#[test]
fn packaged_icon_matches_application_icon() {
    let application = include_bytes!("../assets/app-icon.svg");
    let packaged = include_bytes!("../../../packaging/montray-ui/montray-ui.svg");
    assert_eq!(packaged, application);
}
