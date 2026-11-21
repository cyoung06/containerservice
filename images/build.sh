#!/bin/sh
# Builds and pushes the images: base first, the others on top of it. Usage: images/build.sh [tag]
set -eu
cd "$(dirname "$0")"
repo=oci.syeyoung.dev/containers
# A new tag each build: nodes keep the image of a tag they have pulled.
tag=${1:-$(date +%Y.%m.%d-%H%M)}
docker buildx build --platform linux/amd64 -t "$repo/base:$tag" -t "$repo/base:latest" --push base
for image in pwn claude; do
  docker buildx build --platform linux/amd64 --build-arg "BASE=$repo/base:$tag" -t "$repo/$image:$tag" -t "$repo/$image:latest" --push "$image"
done
