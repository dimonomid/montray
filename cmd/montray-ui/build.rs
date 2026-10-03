use std::env;

fn main() {
    export_build_metadata("MONTRAY_BUILD_VERSION", "dev");
    export_build_metadata("MONTRAY_BUILD_COMMIT", "none");
    export_build_metadata("MONTRAY_BUILD_DATE", "unknown");
    export_build_metadata("MONTRAY_BUILT_BY", "cargo");
    println!(
        "cargo:rustc-env=MONTRAY_BUILD_TARGET={}",
        env::var("TARGET").expect("Cargo did not provide TARGET")
    );
    slint_build::compile("ui/app.slint").expect("failed to compile Slint UI");
}

/// Exports caller-supplied release metadata with a useful direct-Cargo fallback.
fn export_build_metadata(name: &str, fallback: &str) {
    println!("cargo:rerun-if-env-changed={name}");
    println!(
        "cargo:rustc-env={name}={}",
        env::var(name).unwrap_or_else(|_| fallback.into())
    );
}
