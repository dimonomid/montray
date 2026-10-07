#!/usr/bin/env bash

# Test installation over an old manual install with the Debian and Fedora
# packages. Check installed files, safe refusal of a conflicting server unit,
# warnings about old files, and config handling.
# Clean package installation is tested by test-linux-packages-e2e.sh.

set -euo pipefail

release_dir="$(realpath "${1:?release directory is required}")"
repo_dir="$(git rev-parse --show-toplevel)"

# Check that Debian starts a new service, RPM only applies its preset, and
# upgrades restart a service only when appropriate.
test_postinstall_scripts() (
  local test_dir
  test_dir="$(mktemp -d)"
  trap 'rm -rf "$test_dir"' EXIT

  mkdir -p "$test_dir/bin"
  cat > "$test_dir/bin/systemd-sysusers" <<'EOF'
#!/bin/sh
printf 'sysusers %s\n' "$*" >> "$COMMAND_LOG"
EOF
  cat > "$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$COMMAND_LOG"
EOF
  cat > "$test_dir/bin/deb-systemd-helper" <<'EOF'
#!/bin/sh
printf 'deb-systemd-helper %s\n' "$*" >> "$COMMAND_LOG"
if [ "$1" = --quiet ] && [ "$2" = was-enabled ] \
    && [ "${MOCK_WAS_ENABLED:-1}" = 0 ]; then
  exit 1
fi
exit 0
EOF
  cat > "$test_dir/bin/deb-systemd-invoke" <<'EOF'
#!/bin/sh
printf 'deb-systemd-invoke %s\n' "$*" >> "$COMMAND_LOG"
EOF
  chmod 0755 "$test_dir/bin/systemd-sysusers" "$test_dir/bin/systemctl" \
    "$test_dir/bin/deb-systemd-helper" "$test_dir/bin/deb-systemd-invoke"

  : > "$test_dir/deb-install.log"
  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/deb-install.log" \
    sh "$repo_dir/packaging/montray-server/deb-postinstall.sh" configure
  grep -Fxq "sysusers /usr/lib/sysusers.d/montray.conf" "$test_dir/deb-install.log"
  grep -Fxq "deb-systemd-helper unmask montray-server.service" "$test_dir/deb-install.log"
  grep -Fxq "deb-systemd-helper --quiet was-enabled montray-server.service" "$test_dir/deb-install.log"
  grep -Fxq "deb-systemd-helper enable montray-server.service" "$test_dir/deb-install.log"
  grep -Fxq "systemctl --system daemon-reload" "$test_dir/deb-install.log"
  grep -Fxq "deb-systemd-invoke start montray-server.service" "$test_dir/deb-install.log"

  : > "$test_dir/deb-upgrade.log"
  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/deb-upgrade.log" \
    MOCK_WAS_ENABLED=0 \
    sh "$repo_dir/packaging/montray-server/deb-postinstall.sh" configure 1.2.3
  grep -Fxq "sysusers /usr/lib/sysusers.d/montray.conf" "$test_dir/deb-upgrade.log"
  grep -Fxq "deb-systemd-helper --quiet was-enabled montray-server.service" "$test_dir/deb-upgrade.log"
  grep -Fxq "deb-systemd-helper update-state montray-server.service" "$test_dir/deb-upgrade.log"
  ! grep -Fxq "deb-systemd-helper enable montray-server.service" "$test_dir/deb-upgrade.log"
  grep -Fxq "deb-systemd-invoke try-restart montray-server.service" "$test_dir/deb-upgrade.log"
  ! grep -Fxq "deb-systemd-invoke start montray-server.service" "$test_dir/deb-upgrade.log"

  # Refuse dpkg's unsafe chrootless alternate-root mode before inspecting or
  # changing anything on the host.
  for preinstall in montray-server montray-ui; do
    if DPKG_ROOT="$test_dir/root" \
      sh "$repo_dir/packaging/$preinstall/warn-manual-install.sh" install \
      2> "$test_dir/deb-root-warning.log"; then
      echo "$preinstall accepted an unsupported DPKG_ROOT install" >&2
      return 1
    fi
    grep -Fq "Installing Montray with DPKG_ROOT is not supported" \
      "$test_dir/deb-root-warning.log"
  done

  : > "$test_dir/deb-remove.log"
  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/deb-remove.log" \
    sh "$repo_dir/packaging/montray-server/deb-preremove.sh" remove
  grep -Fxq "deb-systemd-invoke stop montray-server.service" "$test_dir/deb-remove.log"
  ! grep -Fq "disable" "$test_dir/deb-remove.log"

  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/deb-remove.log" \
    sh "$repo_dir/packaging/montray-server/deb-postremove.sh" remove
  grep -Fxq "systemctl --system daemon-reload" "$test_dir/deb-remove.log"

  : > "$test_dir/rpm-install.log"
  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/rpm-install.log" \
    sh "$repo_dir/packaging/montray-server/rpm-postinstall.sh" 1
  grep -Fxq "sysusers /usr/lib/sysusers.d/montray.conf" "$test_dir/rpm-install.log"
  grep -Fxq "systemctl preset montray-server.service" "$test_dir/rpm-install.log"
  ! grep -Fxq "systemctl start montray-server.service" "$test_dir/rpm-install.log"

  : > "$test_dir/rpm-upgrade.log"
  PATH="$test_dir/bin:$PATH" COMMAND_LOG="$test_dir/rpm-upgrade.log" \
    sh "$repo_dir/packaging/montray-server/rpm-postinstall.sh" 2
  grep -Fxq "sysusers /usr/lib/sysusers.d/montray.conf" "$test_dir/rpm-upgrade.log"
  grep -Fxq "systemctl try-restart montray-server.service" "$test_dir/rpm-upgrade.log"
  ! grep -Fq "preset " "$test_dir/rpm-upgrade.log"
  ! grep -Fxq "systemctl start montray-server.service" "$test_dir/rpm-upgrade.log"
)

test_postinstall_scripts

# Test the .deb packages on a small Debian system.
docker run --rm \
  --volume "${release_dir}:/release:ro" \
  debian:bookworm-slim \
  bash -euo pipefail -c '
    # Update package metadata. The package manager will install the declared
    # dependencies when the real installation happens below.
    apt-get update

    # Some libraries are present in many base images, so also check that the
    # UI package declares them instead of getting them by accident.
    ui_dependencies="$(dpkg-deb --field /release/montray-ui_*.deb Depends)"
    for dependency in libc6 libgcc-s1 libwayland-client0 libwayland-egl1; do
      tr "," "\n" <<<"$ui_dependencies" \
        | grep -Eq "^[[:space:]]*$dependency([[:space:]]|$)"
    done

    # Create files left by the old setup commands, including a customized
    # server config that must survive the move to the package.
    mkdir -p /etc/systemd/system /usr/local/lib/sysusers.d
    printf "%s\\n" "# Generated by \`montray-server setup\`; replaced only with \`--reinstall\`." "ExecStart=\"/usr/local/bin/montray-server\" --config \"/etc/montray-server.yml\"" > /etc/systemd/system/montray-server.service
    printf "%s\\n" "u _montray - \"Montray Server monitoring daemon\"" > /usr/local/lib/sysusers.d/montray.conf
    printf "%s\\n" "#!/bin/sh" "exit 0" > /usr/local/bin/montray-server
    chmod 0755 /usr/local/bin/montray-server
    printf "%s\\n" "standalone-config" > /etc/montray-server.yml
    printf "%s\\n" "#!/bin/sh" "exit 0" > /usr/local/bin/montray-ui
    chmod 0755 /usr/local/bin/montray-ui

    # The server package must refuse to install while the old unit would
    # override its unit. It must not change any standalone files.
    if dpkg -i /release/montray-server_*.deb > /tmp/package-refused.log 2>&1; then
      echo "server package unexpectedly installed over the standalone unit" >&2
      exit 1
    fi
    grep -Fq "would override the packaged Montray Server" /tmp/package-refused.log
    test -e /etc/systemd/system/montray-server.service
    test -e /usr/local/bin/montray-server
    test -e /usr/local/lib/sysusers.d/montray.conf
    grep -Fxq "standalone-config" /etc/montray-server.yml

    # Simulate the cleanup printed by the package, while keeping the config.
    rm /etc/systemd/system/montray-server.service
    rm /usr/local/bin/montray-server
    rm /usr/local/lib/sysusers.d/montray.conf

    # Keep the existing config without an interactive dpkg question. An
    # interactive installation asks the user which version to keep.
    apt-get -o Dpkg::Options::=--force-confold install --yes --no-install-recommends \
      /release/montray-server_*.deb /release/montray-ui_*.deb \
      2>&1 | tee /tmp/package-install.log

    # Check that the packages installed all expected files and created the
    # server user.
    test -x /usr/bin/montray-server
    test -x /usr/bin/montray-ui
    test -f /etc/montray-server.yml
    test -f /usr/lib/systemd/system/montray-server.service
    grep -Fxq "ExecStart=/usr/bin/montray-server" /usr/lib/systemd/system/montray-server.service
    test -f /usr/share/doc/montray-server/copyright
    test -f /usr/share/doc/montray-ui/copyright
    grep -Fq "Copyright 2026 Dmitry Frank" /usr/share/doc/montray-server/copyright
    grep -Fq "Copyright 2026 Dmitry Frank" /usr/share/doc/montray-ui/copyright
    test -f /usr/share/applications/montray-ui.desktop
    test -f /usr/share/icons/hicolor/scalable/apps/montray-ui.svg
    grep -Fxq "standalone-config" /etc/montray-server.yml
    id _montray

    # The package must warn about the old UI binary without removing it.
    test -e /usr/local/bin/montray-ui
    grep -Fq "Found a possible standalone Montray UI installation" /tmp/package-install.log

    # Reinstall both packages. The server config must stay unchanged, and the
    # standalone warnings must not repeat during an upgrade.
    echo user-change >> /etc/montray-server.yml
    dpkg -i /release/montray-server_*.deb /release/montray-ui_*.deb 2>&1 | tee /tmp/package-reinstall.log
    grep -q user-change /etc/montray-server.yml
    ! grep -Fq "possible standalone Montray Server" /tmp/package-reinstall.log
    ! grep -Fq "possible standalone Montray UI" /tmp/package-reinstall.log

    # A normal Debian package removal must leave the server config in place.
    dpkg --remove montray-ui montray-server
    test -f /etc/montray-server.yml
  '

# Run the same basic checks for the .rpm packages on Fedora.
docker run --rm \
  --volume "${release_dir}:/release:ro" \
  fedora:44 \
  bash -euo pipefail -c '
    # Some libraries are present in many base images, so also check that the
    # UI package declares them instead of getting them by accident.
    ui_dependencies="$(rpm -qp --requires /release/montray-ui-*.rpm)"
    for dependency in glibc libgcc libwayland-client libwayland-egl; do
      grep -Fxq "$dependency" <<<"$ui_dependencies"
    done
    for package in montray-server montray-ui; do
      test "$(rpm -qp --queryformat "%{LICENSE}" /release/$package-*.rpm)" = BSD-2-Clause
    done

    # Create files left by the old setup commands, including a customized
    # server config that must survive the move to the package.
    mkdir -p /etc/systemd/system /usr/local/lib/sysusers.d
    printf "%s\\n" "# Generated by \`montray-server setup\`; replaced only with \`--reinstall\`." "ExecStart=\"/usr/local/bin/montray-server\" --config \"/etc/montray-server.yml\"" > /etc/systemd/system/montray-server.service
    printf "%s\\n" "u _montray - \"Montray Server monitoring daemon\"" > /usr/local/lib/sysusers.d/montray.conf
    printf "%s\\n" "#!/bin/sh" "exit 0" > /usr/local/bin/montray-server
    chmod 0755 /usr/local/bin/montray-server
    printf "%s\\n" "standalone-config" > /etc/montray-server.yml
    printf "%s\\n" "#!/bin/sh" "exit 0" > /usr/local/bin/montray-ui
    chmod 0755 /usr/local/bin/montray-ui

    # The server package must refuse to install while the old unit would
    # override its unit. It must not change any standalone files.
    if rpm -U --nodeps /release/montray-server-*.rpm > /tmp/package-refused.log 2>&1; then
      echo "server package unexpectedly installed over the standalone unit" >&2
      exit 1
    fi
    grep -Fq "would override the packaged Montray Server" /tmp/package-refused.log
    test -e /etc/systemd/system/montray-server.service
    test -e /usr/local/bin/montray-server
    test -e /usr/local/lib/sysusers.d/montray.conf
    grep -Fxq "standalone-config" /etc/montray-server.yml

    # Simulate the cleanup printed by the package, while keeping the config.
    rm /etc/systemd/system/montray-server.service
    rm /usr/local/bin/montray-server
    rm /usr/local/lib/sysusers.d/montray.conf

    # Let dnf resolve the dependencies declared by both packages. Keep the
    # output so we can also check the UI warning.
    dnf install --assumeyes /release/montray-server-*.rpm \
      /release/montray-ui-*.rpm 2>&1 | tee /tmp/package-install.log

    # Check that the packages installed all expected files and created the
    # server user.
    test -x /usr/bin/montray-server
    test -x /usr/bin/montray-ui
    test -f /etc/montray-server.yml
    test -f /usr/lib/systemd/system/montray-server.service
    grep -Fxq "ExecStart=/usr/bin/montray-server" /usr/lib/systemd/system/montray-server.service
    test -f /usr/share/licenses/montray-server/LICENSE
    test -f /usr/share/licenses/montray-ui/LICENSE
    grep -Fq "Copyright 2026 Dmitry Frank" /usr/share/licenses/montray-server/LICENSE
    grep -Fq "Copyright 2026 Dmitry Frank" /usr/share/licenses/montray-ui/LICENSE
    test -f /usr/share/applications/montray-ui.desktop
    test -f /usr/share/icons/hicolor/scalable/apps/montray-ui.svg
    grep -Fxq "standalone-config" /etc/montray-server.yml
    test -f /etc/montray-server.yml.rpmnew
    id _montray

    # The package must warn about the old UI binary without removing it.
    test -e /usr/local/bin/montray-ui
    grep -Fq "Found a possible standalone Montray UI installation" /tmp/package-install.log

    # Reinstall both packages and make sure the standalone warnings do not
    # repeat during an upgrade.
    rpm -U --replacepkgs /release/montray-server-*.rpm /release/montray-ui-*.rpm 2>&1 | tee /tmp/package-reinstall.log
    ! grep -Fq "possible standalone Montray Server" /tmp/package-reinstall.log
    ! grep -Fq "possible standalone Montray UI" /tmp/package-reinstall.log

    # Check that both packages can be removed normally.
    rpm -e montray-ui montray-server
  '
