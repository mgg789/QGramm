# QGramm core

- Go backend only. The original Swift application lives on `messenger`.
- Work on `dev`; preserve existing changes. Review and check before commits.
- `cmd/qgramm` runs the service; `cmd/qgramm-build` validates TOML, selects tags and generates deployment files.
- `internal/core` owns SQLite, authentication, chats and durable delivery. `internal/modules` installers are selected with `qg_*` tags. `internal/cryptoenc` supplies standard HPKE/storage primitives and tagged MLS.
- `go test -race ./...` checks the minimal profile. `sh scripts/build-matrix.sh` checks selectable build/runtime profiles and module exclusion without an external broker.
- Use TOML references for secret names; never store real secrets in TOML, fixtures, logs, commits or documentation.
- No client UI/SDK, user registration, PostgreSQL or bundled TURN. Clients implement MLS/WebRTC and verify peer identity.
- Config feature changes require rebuilding; runtime verifies the compiled manifest.
- Configuration resolves defaults, then an optional curated preset, then explicit TOML overrides. `qgramm-build init` refuses existing files; `validate` and `explain` inspect configuration offline without reading secret values or asserting readiness. Build and runtime use the same loader.
- Store schema changes must use versioned migrations. Preserve durable operation/event transaction boundaries.
- Do not claim runtime/provider/crypto interoperability or load acceptance without recorded results. Outstanding checks belong in `docs/verification.md`.
- Call an independent inspector before commits and after substantial security changes.
- `go run -race ./examples/basic` checks the disposable HTTP/WebSocket integration example. `python3 scripts/check-docs.py` checks local documentation links, UTF-8 and JSON evidence.
- Performance evidence distinguishes ACK durability, image provenance and successful-request percentiles. Never mix Docker VM measurements with dedicated Linux/SSD qualification or reconstruct p99 from p95-only artifacts.

- Message jobs already queued may share a FULL commit (max16 jobs/8MiB, no delay timer). Each job uses a savepoint; ACK/wake happen only after commit. InTransaction hooks must be SQL-only and must not manage transactions or savepoints.

- HTTP batch submits bounded groups (up to16 messages/8MiB and queue capacity) together; savepoints isolate elements, duplicate IDs flush prior groups, and results follow FULL commit. Preserve per-item207 and retry semantics.

- `tools/scenarios` is a separate benchmark Go module: `(cd tools/scenarios && go test -race ./... && go vet ./... && python3 -m unittest test_run.py)`. Keep competitor dependencies out of the production module; compare exact accepted IDs/payloads with admission/skips, and distinguish NATS consumer-state controls from cross-service guarantees.
