use std::fmt::Write as _;
use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow, bail};
use base64::Engine as _;
use ring::rand::SecureRandom as _;

use crate::config;

const TOKEN_BYTES: usize = 32;

/// Creates a new client credential and prints both client and server setup instructions.
///
/// The raw token is written only to the owner-only file and is never echoed;
/// output contains the SHA-256 verifier expected by Montray Server. Existing files are
/// never replaced, which makes accidental credential rotation impossible.
pub fn generate(
    output: &mut dyn Write,
    config_filename: &Path,
    server_id: &str,
    output_filename: Option<&Path>,
) -> Result<PathBuf> {
    config::validate_server_id(server_id).context("invalid server ID")?;
    let config_filename =
        std::path::absolute(config_filename).context("resolve Montray UI config path")?;
    let default_output;
    let output_filename = match output_filename {
        Some(path) => path,
        None => {
            default_output = config_filename
                .parent()
                .context("Montray UI config filename has no parent directory")?
                .join("tokens")
                .join(format!("{server_id}.token"));
            &default_output
        }
    };
    let output_filename =
        std::path::absolute(output_filename).context("resolve bearer token path")?;

    let mut token_bytes = [0_u8; TOKEN_BYTES];
    ring::rand::SystemRandom::new()
        .fill(&mut token_bytes)
        .map_err(|_| anyhow!("generate bearer token: operating system randomness failed"))?;
    let token = base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(token_bytes);
    write_new_token_file(&output_filename, token.as_bytes())?;

    let token_hash = ring::digest::digest(&ring::digest::SHA256, token.as_bytes());
    let mut hash_hex = String::with_capacity(token_hash.as_ref().len() * 2);
    for byte in token_hash.as_ref() {
        write!(&mut hash_hex, "{byte:02x}").expect("writing to a String cannot fail");
    }
    let quoted_token_filename = serde_json::to_string(&output_filename.to_string_lossy())?;

    writeln!(
        output,
        r#"Created bearer token file:

    {}

In {}, find:

wsClient:
  servers:
    - id: {}

And add this block inside that server entry, alongside id and addr:

      auth:
        bearerTokenFile: {}

In the corresponding Montray Server configuration, find:

core:
  messengers:
    - webserver:

And add this block inside the webserver messenger, alongside listenAddress:

        auth:
          - id: my-laptop # Identifies this credential; adjust as needed.
            bearerTokenHash: "sha256:{}"

If auth already exists in the webserver messenger, add only the new list entry to it."#,
        output_filename.display(),
        config_filename.display(),
        server_id,
        quoted_token_filename,
        hash_hex
    )?;
    Ok(output_filename)
}

/// Creates the secret with no-clobber semantics and removes partial writes.
///
/// Unix modes apply when directories/files are newly created. Pre-existing
/// parent directories retain their existing permissions.
fn write_new_token_file(filename: &Path, token: &[u8]) -> Result<()> {
    let parent = filename
        .parent()
        .context("bearer token filename has no parent directory")?;
    let mut directory = fs::DirBuilder::new();
    directory.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt as _;
        directory.mode(0o700);
    }
    directory
        .create(parent)
        .context("create bearer token directory")?;

    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let mut file = match options.open(filename) {
        Ok(file) => file,
        Err(error) if error.kind() == io::ErrorKind::AlreadyExists => bail!(
            "bearer token file already exists at {}; refusing to overwrite it",
            filename.display()
        ),
        Err(error) => return Err(error).context("create bearer token file"),
    };
    if let Err(error) = file.write_all(token) {
        drop(file);
        let _ = fs::remove_file(filename);
        return Err(error).context("write bearer token file");
    }
    if let Err(error) = file.flush() {
        drop(file);
        let _ = fs::remove_file(filename);
        return Err(error).context("write bearer token file");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generates_secure_token_and_exact_instructions_without_leaking_secret() {
        let directory = tempfile::tempdir().unwrap();
        let config = directory.path().join("config/montray.yml");
        let mut output = Vec::new();
        let token_filename = generate(&mut output, &config, "my-server", None).unwrap();
        let token = fs::read(&token_filename).unwrap();
        let decoded = base64::engine::general_purpose::URL_SAFE_NO_PAD
            .decode(&token)
            .unwrap();
        assert_eq!(decoded.len(), TOKEN_BYTES);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            assert_eq!(
                fs::metadata(&token_filename).unwrap().permissions().mode() & 0o777,
                0o600
            );
            assert_eq!(
                fs::metadata(token_filename.parent().unwrap())
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o700
            );
        }

        let hash = ring::digest::digest(&ring::digest::SHA256, &token);
        let hash_hex = hash
            .as_ref()
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>();
        let output = String::from_utf8(output).unwrap();
        let expected = format!(
            r#"Created bearer token file:

    {}

In {}, find:

wsClient:
  servers:
    - id: my-server

And add this block inside that server entry, alongside id and addr:

      auth:
        bearerTokenFile: {:?}

In the corresponding Montray Server configuration, find:

core:
  messengers:
    - webserver:

And add this block inside the webserver messenger, alongside listenAddress:

        auth:
          - id: my-laptop # Identifies this credential; adjust as needed.
            bearerTokenHash: "sha256:{}"

If auth already exists in the webserver messenger, add only the new list entry to it.
"#,
            token_filename.display(),
            std::path::absolute(&config).unwrap().display(),
            token_filename.to_string_lossy(),
            hash_hex
        );
        assert_eq!(output, expected);
        assert!(!output.contains(std::str::from_utf8(&token).unwrap()));

        let error = generate(&mut Vec::new(), &config, "my-server", None).unwrap_err();
        assert!(error.to_string().contains("refusing to overwrite"));
        assert_eq!(fs::read(token_filename).unwrap(), token);
    }

    #[test]
    fn supports_output_override_and_rejects_unsafe_server_id() {
        let directory = tempfile::tempdir().unwrap();
        let config = directory.path().join("montray.yml");
        let output = directory.path().join("custom.token");
        generate(&mut Vec::new(), &config, "remote", Some(&output)).unwrap();
        assert!(output.exists());

        let error = generate(&mut Vec::new(), &config, "../escape", None).unwrap_err();
        assert!(error.to_string().contains("invalid server ID"));
    }
}
