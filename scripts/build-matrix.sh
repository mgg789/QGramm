#!/bin/sh
# Isolated output/configuration, no production configuration mutations.
set -eu
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
for profile in minimal full; do
  cp "configs/$profile.toml" "$scratch/$profile.toml"
  go run ./cmd/qgramm-build build -config "$scratch/$profile.toml" -out "$scratch/qgramm-$profile"
done
go test ./internal/config ./cmd/qgramm-build
go test -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_mcp,qg_http_tools ./...
# Disabled implementations must be excluded at the Go file and dependency levels.
go list -deps ./cmd/qgramm > "$scratch/minimal-deps"
if grep 'github.com/thomas-vilte/mls-go' "$scratch/minimal-deps"; then
  echo 'optional implementation leaked into minimal build' >&2
  exit 1
fi
go list -f '{{range .GoFiles}}{{println .}}{{end}}' ./internal/modules > "$scratch/minimal-files"
if grep -E '^(groups|files|calls|delete|edit|reply|forward|reactions|ai_openai|ai_anthropic|ai_mcp|ai_http_tools)\.go$' "$scratch/minimal-files"; then
  echo 'optional module file leaked into minimal build' >&2
  exit 1
fi
echo 'build matrix passed; no calibrated capacity claim'
