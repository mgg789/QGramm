#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
project="qgramm-independent-mls-$(date +%s)-$$"
compose="tools/mls-interop/compose.yml"
cleanup() { docker compose -p "$project" -f "$compose" down; }
trap cleanup EXIT INT TERM
docker compose -p "$project" -f "$compose" build
docker compose -p "$project" -f "$compose" up -d --wait openmls
docker compose -p "$project" -f "$compose" run --rm runner
