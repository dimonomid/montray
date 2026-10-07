#!/bin/sh
set -e

# RPM passes zero only when the last installed version is being removed. During
# an upgrade, the old package is removed while one newer version remains.
if [ "${1:-}" = 0 ] 2>/dev/null; then
    # Stop and disable the service. Do not fail package removal when systemd is
    # not running.
    systemctl disable --now montray-server.service || true
fi
