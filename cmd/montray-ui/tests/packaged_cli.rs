#![cfg(feature = "packaged")]

use std::process::Command;

#[test]
fn setup_and_migration_are_disabled() {
    let executable = env!("CARGO_BIN_EXE_montray-ui");
    for arguments in [
        &["setup"][..],
        &["setup", "create-config"][..],
        &["setup", "migrate-from-salmon", "--dry-run"][..],
        &["setup", "anything", "--whatever"][..],
        &["setup", "--help"][..],
    ] {
        let output = Command::new(executable).args(arguments).output().unwrap();
        assert!(!output.status.success());
        assert!(output.stdout.is_empty());
        let stderr = String::from_utf8(output.stderr).unwrap();
        assert!(stderr.contains("in-app setup is disabled"), "{stderr}");
    }
}
