#!/bin/bash

DOCKER_TAGS=""

for TAG in "$@"
do
	DOCKER_TAGS="$DOCKER_TAGS -t stashapp/stash:$TAG"
done

echo "$DOCKER_PASSWORD" | docker login -u "$DOCKER_USERNAME" --password-stdin

# the build context is dist/, so the entrypoint must be staged there
cp docker/entrypoint.sh dist/

# must build the image from dist directory
# linux/arm/v6 is no longer built: the image is debian-based and debian has no armv6 port
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 --push $DOCKER_TAGS -f docker/ci/x86_64/Dockerfile dist/

