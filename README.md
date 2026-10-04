# QGramm

Self-hosted Go messaging core for support conversations, user chats and protected AI channels. Integrate the HTTP/WebSocket protocol into your application; QGramm supplies the backend, not a client UI or an account-registration system.

[Русский](README.ru.md) · [Integration](docs/integration.md) · [Security](docs/security.md) · [Configuration](docs/configuration.md) · [API](docs/openapi.json)

The original Swift messenger and its backend are preserved on [`messenger`](https://github.com/mgg789/QGramm/tree/messenger). Core development takes place on `dev`; `main` is reserved for reviewed stages. Original Git history is retained.

## Deployment

Requires Go 1.26 to build locally, or Docker to build the image. One instance serves one application. SQLite and encrypted file chunks live in a persistent volume. No PostgreSQL, Redis, registration server or bundled TURN is required.

```sh
go run ./cmd/qgramm-build plan -config qgramm.toml
go run ./cmd/qgramm-build build -config qgramm.toml -out bin/qgramm
bin/qgramm -config qgramm.toml
```

Set the environment variables named in `[security]`: the application's Ed25519 token verification public key (base64 32 bytes), a random management secret (at least 32 characters), and separate random master/HPKE keys (base64 32 bytes each). Retain keys securely: loss of the master key loses access to stored basic messages and AI state. Do not put secrets into TOML or an image.

The default TOML is **loopback development only**. For a container, copy `configs/container.toml`, configure TLS or an isolated trusted reverse proxy, then:

```sh
go run ./cmd/qgramm-build compose -config configs/container.toml -out compose.yaml
docker compose --env-file .env up -d --build
```

Compose binds the host port to loopback. Supply your own HTTPS reverse proxy and trust boundary. `expected_concurrent_users` derives internal limits and deployment estimates; current sizing is heuristic until benchmark calibration, not a guaranteed throughput claim.

## Features and contracts

- Atomic message/event persistence, operation deduplication, offline replay and per-device delivery/read receipts.
- HPKE client-to-container envelopes and encrypted storage; optional MLS E2EE with client-owned keys.
- Optional groups and per-user send permissions, resumable attachment batches, edit/delete/reply/forward and numeric reactions.
- Optional 1:1 WebRTC signaling and external TURN credentials. Clients supply media and peer verification.
- Optional OpenAI/Anthropic participants and explicitly permitted remote MCP/HTTP tools. AI recipients decrypt inside the container; providers receive plaintext.

Features are selected in `qgramm.toml` **at build time**. Disabled implementations are excluded by Go build tags; changing the feature set requires a rebuild. Runtime rejects a TOML that disagrees with the binary manifest. A full profile is provided in `configs/full.toml`.

## Verification and release status

```sh
go test -race ./...
sh scripts/build-matrix.sh
```

This is a new core implementation under validation. See [verification](docs/verification.md) for exact evidence and outstanding release gates. An implemented endpoint is not evidence of audited cryptography, browser WebRTC acceptance, real AI provider acceptance or the 10,000-connection target.

## License

Apache-2.0. Third-party licenses and cryptographic-library limitations are listed in `NOTICE` and the security documentation.
