use std::process::Command;

/// `--version` must remain usable without configuration or a desktop session.
#[test]
fn version_prints_embedded_build_information_and_exits() {
    let output = Command::new(env!("CARGO_BIN_EXE_montray-ui"))
        .arg("--version")
        .output()
        .expect("failed to execute montray-ui --version");

    assert!(output.status.success());
    assert_eq!(
        String::from_utf8(output.stdout).unwrap(),
        montray_ui::build_info::full_description()
    );
    assert!(output.stderr.is_empty());
}
