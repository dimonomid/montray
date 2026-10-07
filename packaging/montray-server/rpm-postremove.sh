#!/bin/sh
set -e

# Unit files may have changed during an upgrade or disappeared during removal.
# Ask systemd to read them again. Do not fail when systemd is not running.
systemctl daemon-reload || true
