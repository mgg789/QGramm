CONFIG ?= qgramm.toml
OUT ?= bin/qgramm
.PHONY: build plan compose test matrix
build:
	go run ./cmd/qgramm-build build -config $(CONFIG) -out $(OUT)
plan:
	go run ./cmd/qgramm-build plan -config $(CONFIG)
compose:
	go run ./cmd/qgramm-build compose -config $(CONFIG) -out compose.yaml
test:
	go test ./...
matrix:
	sh scripts/build-matrix.sh
