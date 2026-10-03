use std::process::Command;

#[test]
fn missing_default_config_suggests_complete_setup() {
    let config_home = tempfile::tempdir().unwrap();
    let executable = env!("CARGO_BIN_EXE_montray-ui");
    let output = Command::new(executable)
        .env("XDG_CONFIG_HOME", config_home.path())
        .output()
        .expect("failed to execute montray-ui");

    assert!(!output.status.success());
    assert!(output.stdout.is_empty());
    let stderr = String::from_utf8(output.stderr).unwrap();
    assert!(stderr.contains("failed to read config"), "{stderr}");
    assert!(
        stderr.contains(
            "Hint: Run the following command to create the default configuration, desktop-autostart entry, and application launcher:"
        ),
        "{stderr}"
    );
    assert!(
        stderr.contains(&format!("    {executable} setup")),
        "{stderr}"
    );
    assert!(
        stderr.contains(
            "To create only the default configuration without installing the desktop integration, run:"
        ),
        "{stderr}"
    );
    assert!(
        stderr.contains(&format!("    {executable} setup create-config")),
        "{stderr}"
    );
}

#[test]
fn missing_custom_config_does_not_suggest_setup() {
    let directory = tempfile::tempdir().unwrap();
    let missing = directory.path().join("missing.yml");
    let output = Command::new(env!("CARGO_BIN_EXE_montray-ui"))
        .arg("--config")
        .arg(&missing)
        .output()
        .expect("failed to execute montray-ui with a custom config");

    assert!(!output.status.success());
    let stderr = String::from_utf8(output.stderr).unwrap();
    assert!(stderr.contains("failed to read config"), "{stderr}");
    assert!(!stderr.contains("Hint:"), "{stderr}");
}
