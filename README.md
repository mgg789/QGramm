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

### Active conversations, groups and recovery

The expanded campaign measures 2,000 connections across 1,000 independent chats, 4 KiB payloads, 100-recipient groups, reconnect, 10,000 idle connections, five-minute load and concurrent attachments. Two-repeat ranges on the shared M5 Pro / Docker Desktop host, 4 CPU / 8 GiB quotas:

| Service/profile | 1,000 chats, 500/s: delivery p99, ms | 100 recipients × 100/s: delivery p99, ms | RAM at 500/s, MiB |
|---|---:|---:|---:|
| QGramm, HPKE + SQLite FULL | 4.89–5.56 | 13.43–16.09 | 89–97 |
| NATS 2.15.0, FILE stream/FILE consumers | 9.11–51.99 | 3,960–4,598 | 262–265 |
| Centrifugo 6.9.7, memory history | 1.26–1.74 | 3.77–3.79 | 129–170 |

Separate NATS control: FILE stream/MEMORY consumers gives group p99 **3.16–3.28 ms**, retaining disk-synchronized publications but changing consumer-state restart guarantees. QGramm's fixture does not persist device delivery receipts. These are different crypto/ACK/storage contracts, not a universal protocol ranking. Five-minute results preserve admission failures/skips; Centrifugo 4 KiB history OOM and QGramm's initial 10k setup failure remain visible. [Method, p95/p99, all attempts, resource windows and files](docs/scenario-benchmark.md).

### Measured latency and resources

Two repetitions on Apple M5 Pro / Docker Desktop Linux arm64, 4 CPU / 8 GiB limits, 10,000 sockets and one active chat. The table shows ranges across the two runs: steady-phase delivery latency, mean sampled CPU and phase sampled maximum RAM. Load: 100/s for 30 seconds, then 1,000/s for five seconds.

| Configuration | Delivery p95 / p99, ms | Mean CPU¹ | RAM, MiB |
|---|---:|---:|---:|
| QGramm before optimization | 5.31 / 7.34–7.65 | 19.5–20.2% | 583–588 |
| QGramm after, full response | 5.13–5.26 / 7.02–7.26 | 14.3–14.8% | 272–282 |
| QGramm after, compact receipt | 5.19–5.26 / 7.18–7.19 | 13.5–14.1% | 271–272 |
| NATS JetStream, fsync each publication | 4.30–4.97 / 6.52–6.75 | 4.0–4.4% | 459–466 |
| Centrifugo, in-memory history | 3.13–3.54 / 5.18–5.21 | 7.8–8.2% | 425–479 |

¹ 100% means one CPU. QGramm steady RAM fell 52–54% and CPU about 27%. Full-response burst runs returned 83/20 managed 503 rejections; compact receipts had none. All accepted messages were delivered and stored. Whole-run delivery p99 was 29–71 ms for full responses and 7.6–25.4 ms for compact receipts. Baselines omit per-send HPKE/device/chat ACL and chat transactions; Centrifugo history is volatile. This is neither maximum throughput nor an equivalent-feature ranking. [Method, per-run p95/p99, profiles and raw evidence](docs/performance-dynamics.md). [Earlier baseline](docs/comparison-benchmark.en.md).

Previous iteration: with all 10,000 sockets subscribed, the first three further changes reduced steady CPU 77% and RAM 34%; whole-run delivery p99 was6.5–7.4 ms. Same-container Redis showed no repeatable gain and has been removed. [Two-repeat comparison, limits and raw data](docs/performance-iteration.md).

Previous commit/GC/SQL iteration: Redis removed. Diagnostic read-helper calls/accepted fell27.5% and allocations/accepted3%; primary CPU fell2–4%, but p95 rose slightly and one subscribed burst tail worsened. [Before/after results and limits](docs/performance-sql-gc.md).

Experimental replay/WAL work on dev adds one-query history, bounded single-event replay and a request-scoped parsed HPKE key. PASSIVE checkpoint is opt-in; FULL and automatic checkpoint remain. Local replay improved8.1%, but final steady p99 rose8.9%/18.4%; the candidate remains on dev. Initial SQL regression, correction and natural-GC diagnostics are published separately. [Measurements and limitations](docs/performance-tail.md).

Next three experiments (dev): bounded HTTP batch commits and optional compact ACK together lowered ready-batch steady p99 by27.2% and CPU13.3%, with RAM3.4% higher. Idle-buffer reuse was reverted after a38.8% whole-run p99 regression; single-message speed gains were not consistent. [All18 runs, controls and selection](docs/performance-three.md).

Further isolated batch-read experiments did not show a convincing gain: grouped full responses had steady p99+3.8%/CPU+2.8%; preliminary reads CPU−2.3% but p99+2.3%. Neither was retained; no conditional combination was run. [Six controlled runs and archived experiments](docs/performance-batch-reads.md).

## Small integration example

```sh
go run ./examples/basic
```

The executable example starts a disposable loopback core, issues Ed25519 tokens, provisions a direct chat, connects through a one-use WebSocket ticket, sends an HPKE message and decrypts the recipient event. It prints no secrets. See the [short code walkthrough](docs/integration-example.md) before adapting it to your backend and clients.

## Deployment

Requires Go 1.26 to build locally, or Docker to build the image. One instance serves one application. SQLite and encrypted file chunks live in a persistent volume. No PostgreSQL, Redis, registration server or bundled TURN is required.

Start with a compact TOML preset and override only what your application needs:

```sh
go run ./cmd/qgramm-build init -preset support -target local -users 1000 -out my-qgramm.toml
go run ./cmd/qgramm-build validate -config my-qgramm.toml
go run ./cmd/qgramm-build explain -config my-qgramm.toml
```

Presets are `minimal`, `support`, `community`, `ai-openai` and `ai-anthropic`. Explicit TOML values override the preset, including `false` to exclude a module. `explain` reports the effective settings, their sources, derived limits and secret environment-variable names; it never reads secret values. These commands validate configuration offline; startup still checks secrets and the compiled feature manifest. See the [configuration guide](docs/configuration.md) and [Russian reference](docs/reference.ru.md).

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

Features are selected in `qgramm.toml` **at build time**. Disabled implementations are excluded by Go build tags; changing the feature set requires a rebuild. Runtime rejects a TOML that disagrees with the binary manifest. A full standard profile is provided in `configs/full.toml`.

## Verification and release status

```sh
go test -race ./...
sh scripts/build-matrix.sh
```

This is a pre-release. Race/build matrix, independent OpenMLS for the documented profile, Pion direct/TURN, live DeepSeek and a 10,000-connection load run passed. Linux Chromium decoded audio/video and TURN, plus live DeepSeek with independent MLS and restart/replay, also passed. Dedicated Linux/SSD qualification and independent audit are deferred; other scope limits are documented. See [verification](docs/verification.md) and [benchmarks](docs/benchmark.md) for exact scopes; no audited-cryptography or full production-release claim.

## License

Apache-2.0. `NOTICE` and `THIRD_PARTY_LICENSES.txt` retain dependency notices and license texts; cryptographic limitations are in the security documentation.
