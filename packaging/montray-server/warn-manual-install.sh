#!/bin/sh
set -e

# This package does not support dpkg's chrootless alternate-root mode.
if [ -n "${DPKG_ROOT:-}" ]; then
    echo "ERROR: Installing Montray with DPKG_ROOT is not supported." >&2
    exit 1
fi

# Debian passes "install" for a new installation. RPM passes 1 when one
# version will remain installed. Do not check for old files during upgrades.
case "${1:-}" in
    install|1) ;;
    *) exit 0 ;;
esac

unit=/etc/systemd/system/montray-server.service
binary=/usr/local/bin/montray-server
sysusers=/usr/local/lib/sysusers.d/montray.conf

# The -L checks also find broken symlinks, which -e misses.
if [ -e "$unit" ] || [ -L "$unit" ]; then
    cat >&2 <<'EOF'
ERROR: Found a system service that would override the packaged Montray Server:
  /etc/systemd/system/montray-server.service

Nothing was changed. Check that these are old standalone Montray files, then
remove them before installing the package:
  sudo systemctl disable --now montray-server.service
  sudo rm -f /etc/systemd/system/montray-server.service
  sudo rm -f /usr/local/bin/montray-server
  sudo rm -f /usr/local/lib/sysusers.d/montray.conf
  sudo systemctl daemon-reload

Keep your config:
  /etc/montray-server.yml

Then install the package again.
EOF
    exit 1
fi

# The old files do not override the packaged service, so a warning is enough.
if [ ! -e "$binary" ] && [ ! -L "$binary" ] \
    && [ ! -e "$sysusers" ] && [ ! -L "$sysusers" ]; then
    exit 0
fi

cat >&2 <<'EOF'
WARNING: Found possible standalone Montray Server files.

They were not changed. The packaged service uses /usr/bin/montray-server, so
these files are not used by it. Remove them if they belong to an old install:
  sudo rm -f /usr/local/bin/montray-server
  sudo rm -f /usr/local/lib/sysusers.d/montray.conf
EOF
