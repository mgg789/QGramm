# QGramm

Self-hosted Go messaging core for support conversations, user chats and protected AI channels. Integrate the HTTP/WebSocket protocol into your application; QGramm supplies the backend, not a client UI or an account-registration system.

[Русский](README.ru.md) · [Integration](docs/integration.md) · [Security](docs/security.md) · [Configuration](docs/configuration.md) · [API](docs/openapi.json)

The original Swift messenger and its backend are preserved on [`messenger`](https://github.com/mgg789/QGramm/tree/messenger). Core development takes place on `dev`; `main` is reserved for reviewed stages. Original Git history is retained.

## Why QGramm?

Choose QGramm when your application already owns users and UI, and needs persistent chats, device permissions, encrypted payloads and optional AI recipients in one service. Optional features are compiled out. There is no federation or horizontal cluster in this release; clients implement the encryption and WebRTC contracts. QGramm is a pre-release, not a claim of greater security or maturity than established alternatives.

| Project | Best fit | Storage / deployment | Encryption scope | What your application still supplies |
|---|---|---|---|---|
| **QGramm** | Embedded support, user and AI chats | One Go container; SQLite + files volume | HPKE to container; optional MLS E2EE with [public member commits](docs/e2ee.md) | Identity backend, UI, client crypto/media; external TURN |
| [Matrix / Synapse](https://element-hq.github.io/synapse/latest/setup/installation.html) | Federated messaging and an existing client ecosystem | Homeserver; PostgreSQL recommended for production; media storage | Client-side [Olm/Megolm E2EE](https://spec.matrix.org/v1.18/olm-megolm/) | Client integration, deployment and optional media infrastructure |
| [Centrifugo](https://centrifugal.dev/docs/server/history_and_recovery) | Real-time pub/sub transport | Go server; memory or external broker; bounded recovery cache | Application payload encryption is your responsibility | Durable chat database, chat ACL, attachments, calls and AI |
| [Zulip](https://github.com/zulip/zulip/blob/main/docs/production/deployment.md) | Ready-to-use team chat with topics and search | PostgreSQL, RabbitMQ, Redis, Memcached; upload storage | [Mobile push E2EE](https://docs.zulip.com/security/); ordinary chat is server-readable | Product customization; external call-provider integration |
| [NATS + JetStream](https://docs.nats.io/learn/core-nats/) | Service messaging and durable event streams | NATS server; JetStream disk storage | TLS / optional storage encryption; chat E2EE is application-level | User/device model, chat API/ACL, client encryption, calls and AI |

These are different scopes, not comparative performance measurements. Matrix and Zulip provide broader communication platforms; Centrifugo and NATS provide infrastructure. QGramm packages chat persistence and protocol semantics for integration, with [explicit acceptance limits](docs/verification.md).

### Measured latency and resources

Same Apple M5 Pro / Linux arm64 Docker VM, 4 CPU / 8 GiB container quotas, 10,000 WebSockets, one active sender/recipient. Steady load: 100 messages/s for 30 seconds, followed by a five-second 1,000/s burst. The table reports steady-phase latency and sampled resource maxima across setup/load.

| Configuration | Acceptance p95 / p99, ms | Delivery p95 / p99, ms | Sampled CPU¹ / RAM |
|---|---:|---:|---:|
| QGramm: HPKE + JWT/ACL + SQLite FULL | 4.364 / 6.657 | 4.676 / 6.898 | 61.05% / 666.3 MiB |
| NATS JetStream: fsync every publication | 3.945 / 6.641 | 4.018 / 6.621 | 23.50% / 469.3 MiB |
| NATS JetStream: default deferred fsync | 2.759 / 5.161 | 2.788 / 5.186 | 23.66% / 489.5 MiB |
| Centrifugo: in-memory history | 2.781 / 4.719 | 2.800 / 4.952 | 28.48% / 450.1 MiB |

¹ 100% means one CPU. All submitted messages were delivered in these runs. Baselines carry plaintext application data and do not perform QGramm's per-send crypto, device/chat ACL and chat transactions. This measures those configurations, not maximum throughput or equivalent feature sets. [Method, versions and raw results](docs/comparison-benchmark.en.md) · [QGramm hardware/provenance](docs/benchmark.md). Matrix/Synapse and Zulip require separate application-level scenarios and are not included in these measurements.

## Small integration example

```sh
go run ./examples/basic
```

The executable example starts a disposable loopback core, issues Ed25519 tokens, provisions a direct chat, connects through a one-use WebSocket ticket, sends an HPKE message and decrypts the recipient event. It prints no secrets. See the [short code walkthrough](docs/integration-example.md) before adapting it to your backend and clients.

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

This is a pre-release. Race/build matrix, independent OpenMLS for the documented profile, Pion direct/TURN, live DeepSeek and a 10,000-connection load run passed. Linux Chromium decoded audio/video and TURN, plus live DeepSeek with independent MLS and restart/replay, also passed. Dedicated Linux/SSD qualification and independent audit are deferred; other scope limits are documented. See [verification](docs/verification.md) and [benchmarks](docs/benchmark.md) for exact scopes; no audited-cryptography or full production-release claim.

## License

Apache-2.0. `NOTICE` and `THIRD_PARTY_LICENSES.txt` retain dependency notices and license texts; cryptographic limitations are in the security documentation.
