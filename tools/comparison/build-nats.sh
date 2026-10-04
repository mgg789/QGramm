#!/bin/sh
# Official module source fallback; only a fresh temporary build context is written.
set -eu
cd "$(dirname "$0")/servers/nats"
context=$(mktemp -d "${TMPDIR:-/tmp}/qgramm-comparison-nats.XXXXXX")
trap 'rm -rf "$context"' EXIT
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o "$context/nats-server" github.com/nats-io/nats-server/v2
cp Dockerfile "$context/Dockerfile"
docker build --pull=false -t qgramm-comparison-nats:2.11.3 "$context"
