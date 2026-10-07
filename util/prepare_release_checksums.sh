#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: $0 RELEASE_DIRECTORY" >&2
  exit 2
fi

release_dir="$1"

cd "$release_dir"
shopt -s nullglob
artifacts=( *.tar.gz *.zip *.deb *.rpm *.sbom.json )

if [[ "${#artifacts[@]}" -eq 0 ]]; then
  echo "no release archives or SBOMs found in $release_dir" >&2
  exit 1
fi

printf '%s\0' "${artifacts[@]}" | LC_ALL=C sort -z | xargs -0 sha256sum >checksums.txt
