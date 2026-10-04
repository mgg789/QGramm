# QGramm core

- Go backend only. The original Swift application lives on `messenger`.
- Work on `dev`; preserve existing changes. Review and check before commits.
- `cmd/qgramm` runs the service; `cmd/qgramm-build` validates TOML, selects tags and generates deployment files.
- `internal/core` owns SQLite, authentication, chats and durable delivery. `internal/modules` installers are selected with `qg_*` tags. `internal/cryptoenc` supplies standard HPKE/storage primitives and tagged MLS.
- `go test -race ./...` checks the minimal profile. `sh scripts/test-redis.sh sh scripts/build-matrix.sh` builds 30 profiles and checks module exclusion with a real optional Redis fixture. Stock full tags excluding `qg_redis` need no Redis process. The helper uses an existing executable or builds pinned source inside ignored `work/`; requires curl/make/C compiler.
- Use TOML references for secret names; never store real secrets in TOML, fixtures, logs, commits or documentation.
- No client UI/SDK, user registration, PostgreSQL or bundled TURN. Clients implement MLS/WebRTC and verify peer identity.
- Config feature changes require rebuilding; runtime verifies the compiled manifest.
- Store schema changes must use versioned migrations. Preserve durable operation/event transaction boundaries.
- Do not claim runtime/provider/crypto interoperability or load acceptance without recorded results. Outstanding checks belong in `docs/verification.md`.
- Call an independent inspector before commits and after substantial security changes.
- `go run -race ./examples/basic` checks the disposable HTTP/WebSocket integration example. `python3 scripts/check-docs.py` checks local documentation links, UTF-8 and JSON evidence.
- Performance evidence distinguishes ACK durability, image provenance and successful-request percentiles. Never mix Docker VM measurements with dedicated Linux/SSD qualification or reconstruct p99 from p95-only artifacts.
