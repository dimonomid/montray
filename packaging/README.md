# Linux packages

This directory contains the files, nFPM manifests, and package scripts used to
build the Montray `.deb` and `.rpm` packages.

`montray-server` and `montray-ui` are separate packages. Neither package
depends on the other.

## Build packages

Install nFPM v2.46.3, then run:

```sh
PACKAGE_VERSION=1.2.3 ARTIFACT_ARCH=amd64 \
  bash util/build_linux_packages.sh montray-server dist/packages

PACKAGE_VERSION=1.2.3 ARTIFACT_ARCH=amd64 \
  bash util/build_linux_packages.sh montray-ui dist/packages
```

The script builds the binary with in-app setup disabled and creates both
package formats. Set `MONTRAY_UI_LINUX_BUILDER=1` to build the UI in the same
container used for release builds.

When making a package without this script, build each binary with
`MONTRAY_BUILD_PACKAGED=1`.

## Package behavior

The server config at `/etc/montray-server.yml` must not be replaced during an
upgrade. A normal Debian package removal must leave it in place.

A first install stops if `/etc/systemd/system/montray-server.service` already
exists. That file would override the packaged service. The package prints the
cleanup commands and does not change the old installation.

The UI creates its config in the user's config directory on first start.
Autostart is also a user setting. Packages must not create either one.

The nFPM manifests and package scripts in this directory are the reference for
installed files, permissions, dependencies, and install and removal behavior.
