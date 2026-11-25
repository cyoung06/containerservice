#!/bin/sh
# Builds and pushes the images, base first and the others on top of it, all with one semver version:
#   images/build.sh 0.2.0
# A version is never built twice (nodes keep the image of a tag they have pulled), so one that is
# in the registry already is refused.
set -eu
cd "$(dirname "$0")"
repo=oci.syeyoung.dev/containers
version=${1:?usage: build.sh VERSION (semver, e.g. 0.2.0)}
# Semver: X.Y.Z, each 0 or without leading zeros, the major below 1000 (so a date like 2026.11.25
# is not taken for one).
echo "$version" | grep -Eq '^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$' && [ "${version%%.*}" -lt 1000 ] \
    || { echo "build.sh: $version isn't a semver version (X.Y.Z)" >&2; exit 2; }
if docker buildx imagetools inspect "$repo/base:$version" >/dev/null 2>&1; then
    echo "build.sh: $repo/base:$version exists already: pick the next version" >&2
    exit 1
fi
docker buildx build --platform linux/amd64 -t "$repo/base:$version" -t "$repo/base:latest" --push base
for image in pwn claude; do
    docker buildx build --platform linux/amd64 --build-arg "BASE=$repo/base:$version" -t "$repo/$image:$version" -t "$repo/$image:latest" --push "$image"
done
