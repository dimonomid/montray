#!/bin/sh
set -e

# Read our sysusers file and create the _montray user if it does not exist.
systemd-sysusers /usr/lib/sysusers.d/montray.conf

# Tell systemd that the package installed or replaced a unit file. Do not fail
# package installation when systemd is not running.
systemctl daemon-reload || true

# RPM passes the number of installed Montray Server package versions. One means
# a new installation; more than one means an upgrade is in progress.
if [ "${1:-}" = 1 ] 2>/dev/null; then
    # The preset decides whether this service should start on boot. RPM
    # packages do not start a new service during installation.
    systemctl preset montray-server.service || true
elif [ "${1:-}" -gt 1 ] 2>/dev/null; then
    # During an upgrade, restart only if the service is already running.
    systemctl try-restart montray-server.service || true
fi
