#!/bin/sh
set -e

# After a normal removal, tell a running systemd that the unit file is gone.
# Do not fail package removal if systemd cannot reload.
if [ "${1:-}" = remove ] && [ -d /run/systemd/system ]; then
    systemctl --system daemon-reload >/dev/null || true
fi

# A purge means "remove everything", so forget the saved enable setting too.
# A normal removal keeps it for a possible reinstall.
if [ "${1:-}" = purge ] && [ -x /usr/bin/deb-systemd-helper ]; then
    deb-systemd-helper purge montray-server.service >/dev/null || true
fi
