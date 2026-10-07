#!/bin/sh
set -e

# Read our sysusers file and create the _montray user if it does not exist.
systemd-sysusers /usr/lib/sysusers.d/montray.conf

# Debian uses "configure" after a normal install or upgrade. The other values
# mean that Debian is recovering from an earlier package error.
if [ "${1:-}" = configure ] || [ "${1:-}" = abort-upgrade ] \
    || [ "${1:-}" = abort-deconfigure ] || [ "${1:-}" = abort-remove ]; then
    # We're here when the service files are ready and Debian wants us to finish
    # setting up the service.

    # Undo only a mask previously created by Debian's package helper.
    deb-systemd-helper unmask montray-server.service >/dev/null || true

    # On a first install, was-enabled says yes and the service is enabled. On
    # an upgrade, this keeps the choice already made by the administrator.
    if deb-systemd-helper --quiet was-enabled montray-server.service; then
        deb-systemd-helper enable montray-server.service >/dev/null || true
    else
        # Record the current unit without enabling it.
        deb-systemd-helper update-state montray-server.service >/dev/null || true
    fi

    # The directory exists only when systemd is running on this machine.
    if [ -d /run/systemd/system ]; then
        # Tell systemd that the package installed or replaced a unit file.
        systemctl --system daemon-reload >/dev/null || true

        # Debian passes the old package version as the second argument during
        # an upgrade. Otherwise this is a new installation.
        if [ -n "${2:-}" ]; then
            # Restart an active service. A stopped service stays stopped.
            deb-systemd-invoke try-restart montray-server.service >/dev/null || true
        else
            # Start a new installation unless policy-rc.d forbids it.
            deb-systemd-invoke start montray-server.service >/dev/null || true
        fi
    fi
fi
