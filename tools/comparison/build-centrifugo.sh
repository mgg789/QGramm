#!/bin/sh
# Official module source fallback; only a fresh temporary build context is written.
set -eu
cd "$(dirname "$0")/servers/centrifugo"
context=$(mktemp -d "${TMPDIR:-/tmp}/qgramm-comparison-centrifugo.XXXXXX")
trap 'rm -rf "$context"' EXIT
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o "$context/centrifugo" github.com/centrifugal/centrifugo/v6
cp Dockerfile "$context/Dockerfile"
docker build --pull=false -t qgramm-comparison-centrifugo:6.2.3 "$context"
