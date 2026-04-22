#!/bin/sh

set -e

docker build -f Dockerfile.local \
    --build-arg SKY_GIT_BRANCH="$(git rev-parse --abbrev-ref HEAD | tr / -)" \
    --build-arg SKY_GIT_SHA="$(git rev-parse --short HEAD)" \
    -t anzel/sky:local .
