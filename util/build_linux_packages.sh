#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 montray-server|montray-ui OUTPUT_DIRECTORY" >&2
  exit 2
fi
if [[ "$1" != "montray-server" && "$1" != "montray-ui" ]]; then
  echo "usage: $0 montray-server|montray-ui OUTPUT_DIRECTORY" >&2
  exit 2
fi

package_name="$1"
output_directory="$2"
package_version="${PACKAGE_VERSION:-$(git describe --tags --always --dirty)}"
package_version="${package_version#v}"
package_arch="${ARTIFACT_ARCH:-$(go env GOARCH)}"

mkdir -p "$output_directory"

if [[ "$package_name" == "montray-ui" && "${MONTRAY_UI_LINUX_BUILDER:-}" == 1 ]]; then
  MONTRAY_BUILD_PACKAGED=1 bash .github/build-montray-ui-linux.sh
else
  MONTRAY_BUILD_PACKAGED=1 make "$package_name"
fi

export ARTIFACT_ARCH="$package_arch"
export PACKAGE_VERSION="$package_version"

nfpm package \
  --config "packaging/$package_name/nfpm.yaml" \
  --packager deb \
  --target "$output_directory/"
nfpm package \
  --config "packaging/$package_name/nfpm.yaml" \
  --packager rpm \
  --target "$output_directory/"
