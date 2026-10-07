#!/bin/sh
set -e

# Debian passes "remove" when the package is really being removed, not during
# an upgrade. DPKG_ROOT is set when working on another root filesystem; in that
# case we must not stop a service on the host machine.
if [ -z "${DPKG_ROOT:-}" ] && [ "${1:-}" = remove ] \
    && [ -d /run/systemd/system ]; then
    # This helper checks policy-rc.d before stopping the service.
    deb-systemd-invoke stop montray-server.service >/dev/null || true
fi
