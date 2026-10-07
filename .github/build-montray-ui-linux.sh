#!/usr/bin/env bash

set -euo pipefail

# Building against Ubuntu 20.04 keeps the distributed binary compatible with
# its older glibc instead of inheriting the moving ubuntu-latest baseline.
container_runtime="${CONTAINER_RUNTIME:-docker}"
builder_image="montray-ui-ubuntu-20.04-builder"

"${container_runtime}" build \
  --tag "${builder_image}" \
  .github/montray-ui-linux-builder
"${container_runtime}" run \
  --rm \
  --env MONTRAY_BUILD_PACKAGED="${MONTRAY_BUILD_PACKAGED:-}" \
  --volume "${PWD}:/workspace" \
  "${builder_image}"
