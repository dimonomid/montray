#!/bin/sh
set -e

# This package does not support dpkg's chrootless alternate-root mode.
if [ -n "${DPKG_ROOT:-}" ]; then
    echo "ERROR: Installing Montray with DPKG_ROOT is not supported." >&2
    exit 1
fi

# Debian passes "install" for a new installation. RPM passes 1 when one
# version will remain installed. Do not repeat this warning during upgrades.
case "${1:-}" in
    install|1) ;;
    *) exit 0 ;;
esac

binary=/usr/local/bin/montray-ui

# The -L check also finds a broken symlink, which -e misses.
if [ ! -e "$binary" ] && [ ! -L "$binary" ]; then
    exit 0
fi

cat >&2 <<'EOF'
WARNING: Found a possible standalone Montray UI installation:
  /usr/local/bin/montray-ui

It was not removed. It may override the packaged binary at /usr/bin/montray-ui.

If this is the old Montray UI binary, remove it after the package is installed:
  sudo rm /usr/local/bin/montray-ui

If you previously ran "montray-ui setup", remove or update these files in your
home directory:
  ~/.local/share/applications/montray-ui.desktop
  ~/.config/autostart/montray-ui.desktop
  ~/.local/share/icons/hicolor/scalable/apps/montray-ui.svg

Custom desktop entries can instead change their Exec path from
/usr/local/bin/montray-ui to /usr/bin/montray-ui.

Keep your config:
  ~/.config/montray-ui/montray-ui.yml
EOF
