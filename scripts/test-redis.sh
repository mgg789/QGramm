#!/bin/sh
# Explicit real Redis fixture; no system installation or mocked broker.
set -eu
repo=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
if [ -n "${QGRAMM_REDIS_TEST_BINARY:-}" ]; then
  test -x "$QGRAMM_REDIS_TEST_BINARY" || { echo 'QGRAMM_REDIS_TEST_BINARY must be executable' >&2; exit 2; }
elif command -v redis-server >/dev/null 2>&1; then
  QGRAMM_REDIS_TEST_BINARY=$(command -v redis-server)
else
  for tool in curl make cc; do
    command -v "$tool" >/dev/null 2>&1 || { echo "Real Redis fixture needs $tool, or set QGRAMM_REDIS_TEST_BINARY" >&2; exit 2; }
  done
  mkdir -p "$repo/work"
  scratch=$(mktemp -d "$repo/work/redis-tests.XXXXXX")
  trap 'rm -rf "$scratch"' EXIT HUP INT TERM
  curl -fsSL https://download.redis.io/releases/redis-7.2.14.tar.gz -o "$scratch/redis.tar.gz"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$scratch/redis.tar.gz" | cut -d ' ' -f 1)
  else
    actual=$(shasum -a 256 "$scratch/redis.tar.gz" | cut -d ' ' -f 1)
  fi
  test "$actual" = '21326da3f66c0aead4c8204c0ac52ff905337a77cadd169f75ac22835ea30025' || { echo 'Redis archive checksum mismatch' >&2; exit 2; }
  tar -xzf "$scratch/redis.tar.gz" -C "$scratch"
  make -C "$scratch/redis-7.2.14" -j2 MALLOC=libc BUILD_TLS=no redis-server
  QGRAMM_REDIS_TEST_BINARY="$scratch/redis-7.2.14/src/redis-server"
fi
export QGRAMM_REDIS_TEST_BINARY
if [ "$#" -eq 0 ]; then
  set -- go test -race -tags qg_redis ./internal/modules
fi
cd "$repo"
# Do not exec: the EXIT trap must remove a fixture built for this invocation.
"$@"
