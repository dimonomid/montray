//! Compile-time identity printed by `montray-ui --version`.

/// Formats the version and provenance embedded by the build entry point.
pub fn full_description() -> String {
    format_description(
        env!("MONTRAY_BUILD_VERSION"),
        env!("MONTRAY_BUILD_COMMIT"),
        env!("MONTRAY_BUILD_DATE"),
        env!("MONTRAY_BUILT_BY"),
        env!("MONTRAY_BUILD_TARGET"),
    )
}

/// Keeps output formatting independently testable from compile-time variables.
fn format_description(
    version: &str,
    commit: &str,
    date: &str,
    built_by: &str,
    target: &str,
) -> String {
    format!(
        r#"Montray UI {version}
Commit: {commit}
Build time: {date}
Built by: {built_by}
Target: {target}

Written by Dmitry Frank (https://dmitryfrank.com)
"#
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn description_contains_release_identity_and_provenance() {
        assert_eq!(
            format_description(
                "2.0.0",
                "0123456789abcdef",
                "2026-09-12T10:20:30Z",
                "make",
                "x86_64-unknown-linux-gnu",
            ),
            r#"Montray UI 2.0.0
Commit: 0123456789abcdef
Build time: 2026-09-12T10:20:30Z
Built by: make
Target: x86_64-unknown-linux-gnu

Written by Dmitry Frank (https://dmitryfrank.com)
"#
        );
    }
}
