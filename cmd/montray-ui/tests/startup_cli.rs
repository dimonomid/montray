use std::process::Command;

#[test]
fn missing_default_config_is_created_automatically() {
    let config_home = tempfile::tempdir().unwrap();
    let executable = env!("CARGO_BIN_EXE_montray-ui");
    let config = config_home.path().join("montray-ui/montray-ui.yml");
    let output = Command::new(executable)
        .env("XDG_CONFIG_HOME", config_home.path())
        .env_remove("DISPLAY")
        .env_remove("WAYLAND_DISPLAY")
        .output()
        .expect("failed to execute montray-ui");

    assert!(!output.status.success());
    assert!(output.stdout.is_empty());
    let stderr = String::from_utf8(output.stderr).unwrap();
    assert!(!stderr.contains("failed to read config"), "{stderr}");
    assert!(
        stderr.contains("created default configuration at"),
        "{stderr}"
    );
    let contents = std::fs::read_to_string(config).unwrap();
    assert!(contents.contains("id: local"), "{contents}");
}

#[test]
fn missing_custom_config_is_created_automatically() {
    let directory = tempfile::tempdir().unwrap();
    let missing = directory.path().join("missing.yml");
    let output = Command::new(env!("CARGO_BIN_EXE_montray-ui"))
        .arg("--config")
        .arg(&missing)
        .env_remove("DISPLAY")
        .env_remove("WAYLAND_DISPLAY")
        .output()
        .expect("failed to execute montray-ui with a custom config");

    assert!(!output.status.success());
    let stderr = String::from_utf8(output.stderr).unwrap();
    assert!(!stderr.contains("failed to read config"), "{stderr}");
    assert!(
        stderr.contains("created default configuration at"),
        "{stderr}"
    );
    let contents = std::fs::read_to_string(missing).unwrap();
    assert!(contents.contains("id: local"), "{contents}");
}
