# QGramm

<p align="center">
  <a href="https://github.com/mgg789/QGramm"><img src="docs/assets/qgramm-wordmark.png" alt="QGramm" width="800"></a>
</p>

<p align="center">
  <a href="https://github.com/mgg789/QGramm/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/mgg789/QGramm?display_name=tag"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
  <a href="go.mod"><img alt="Go 1.26+" src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white"></a>
  <a href="Dockerfile"><img alt="Docker" src="https://img.shields.io/badge/deploy-Docker-2496ED?logo=docker&logoColor=white"></a>
  <a href="docs/openapi.json"><img alt="HTTP API v1" src="https://img.shields.io/badge/API-v1-6f42c1"></a>
</p>

**Add messaging to your product without adopting somebody else’s chat application.** QGramm is a self-hosted Go service for persistent conversations, delivery and access rules. Connect it to the users, interface and identity system you already have.

[Русский](README.ru.md) · [Integration](docs/integration.md) · [Security](docs/security.md) · [Configuration](docs/configuration.md) · [API](docs/openapi.json)

The original Swift messenger and its backend are preserved on [`messenger`](https://github.com/mgg789/QGramm/tree/messenger). Core development takes place on `dev`; `main` is reserved for reviewed stages. Original Git history is retained.

## Why teams choose QGramm

Your product keeps its account system, user experience and client apps. QGramm supplies the messaging service behind them: versioned HTTP commands, WebSocket events, durable history and reconnect recovery, per-device receipts, chat permissions, and optional files, groups, MLS encryption and AI participants.

Run it as one container with SQLite and a persistent volume. Select the features you use in TOML; build tags remove unused modules from the binary. Your backend provisions identities and access, while your clients implement the API and any client-side cryptography.

## Where it fits best

- **Customer support:** add a conversation between a customer and your support team inside an existing app.
- **User communities:** provide direct or group chats while keeping your own accounts, roles and interface.
- **AI-enabled products:** let AI participants join conversations, with optional tool approvals, scoped knowledge retrieval or a separate MLS endpoint.

QGramm fits teams that want messaging infrastructure they can operate and integrate. For a ready-to-use collaboration product or federated network, Matrix or Zulip may fit better; for a live event bus without chat semantics, use Centrifugo or NATS. The table below compares these roles.

## Deploy

Build a Compose file from the container profile, add the configured secret values to `.env`, then start the service:

```sh
go run ./cmd/qgramm-build compose -config configs/container.toml -out compose.yaml
docker compose --env-file .env up -d --build
```

The generated deployment keeps its port on loopback and stores SQLite data and files in a persistent volume. Put an HTTPS reverse proxy in front of it. Your backend issues Ed25519 tokens and provisions users and chats; see the [integration guide](docs/integration.md). For a local end-to-end example, run `go run ./examples/basic`.

| Project | Best fit | Storage / deployment | Encryption scope | What your application still supplies |
|---|---|---|---|---|
| **QGramm** | Embedded support, user and AI chats | One Go container; SQLite + files volume | HPKE to container; optional MLS E2EE with [public member commits](docs/e2ee.md) | Identity backend, UI, client crypto/media; external TURN |
| [Matrix / Synapse](https://element-hq.github.io/synapse/latest/setup/installation.html) | Federated messaging and an existing client ecosystem | Homeserver; PostgreSQL recommended for production; media storage | Client-side [Olm/Megolm E2EE](https://spec.matrix.org/v1.18/olm-megolm/) | Client integration, deployment and optional media infrastructure |
| [Centrifugo](https://centrifugal.dev/docs/server/history_and_recovery) | Real-time pub/sub transport | Go server; memory or external broker; bounded recovery cache | Application payload encryption is your responsibility | Durable chat database, chat ACL, attachments, calls and AI |
| [Zulip](https://github.com/zulip/zulip/blob/main/docs/production/deployment.md) | Ready-to-use team chat with topics and search | PostgreSQL, RabbitMQ, Redis, Memcached; upload storage | [Mobile push E2EE](https://docs.zulip.com/security/); ordinary chat is server-readable | Product customization; external call-provider integration |
| [NATS + JetStream](https://docs.nats.io/learn/core-nats/) | Service messaging and durable event streams | NATS server; JetStream disk storage | TLS / optional storage encryption; chat E2EE is application-level | User/device model, chat API/ACL, client encryption, calls and AI |

These projects serve different needs. Matrix and Zulip are broader messaging products; Centrifugo and NATS are infrastructure. QGramm packages chat persistence and protocol semantics for integration. The table describes roles and contracts, not a performance ranking. See the [acceptance limits](docs/verification.md).

## Benchmark reports

We publish the full measurements and rejected runs, including workload, hardware, p95/p99 latency, CPU/RAM sampling, admission failures, and message integrity checks. Results come from a shared Docker Desktop host; they are not a dedicated Linux/SSD qualification or a universal performance ranking.

- [Cross-service scenarios: chats, groups, reconnect, idle sockets and files](docs/scenario-benchmark.md)
- [QGramm and NATS/Centrifugo comparison runs](docs/comparison-benchmark.en.md)
- [Latency and resource measurements](docs/performance-dynamics.md)
- [Group fan-out and retention follow-up](docs/performance-retention-fanout.md)
- [Commit, GC, SQL and replay investigations](docs/performance-sql-gc.md), [replay/WAL](docs/performance-tail.md)
- [Selected optimizations](docs/performance-three.md), [batch-read experiments](docs/performance-batch-reads.md), and [Redis evaluation](docs/performance-iteration.md)

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
- Optional OpenAI/Anthropic participants and explicitly permitted remote MCP/HTTP tools. The [named AI network](docs/ai-network.md) gives each bot a permanent user/device identity for BASIC chats, while the legacy direct MLS AI path remains available. AI recipients decrypt inside the container; providers receive plaintext. Optional [stage-2 AI policy](docs/ai-policy.md) adds backend-signed approvals, per-tool permissions, encrypted usage/budgets and durable notices before external calls. Optional [stage-3 scoped storage](docs/ai-stage3.md) adds bounded encrypted resource/document/graph retrieval and a separate external MLS endpoint contract; local LLM runtime acceptance remains pending.

The integrated vault stores resource payloads, document text/files/vectors and
graph edges encrypted at rest in the trusted container. Retrieval uses supplied
vectors or bounded lexical scans; it does not start an embedding model or ANN
service. Storage tools require both the generic AI policy approval and an
independent resource grant bound to the exact request hash, destination,
resource and action. The administrative storage MCP endpoint uses the
management bearer and is never a model credential.

Features are selected in `qgramm.toml` **at build time**. Disabled implementations are excluded by Go build tags; changing the feature set requires a rebuild. Runtime rejects a TOML that disagrees with the binary manifest. A full standard profile is provided in `configs/full.toml`.

## Verification and release status

```sh
go test -race ./...
sh scripts/build-matrix.sh
```

This is a pre-release. Race/build matrix, independent OpenMLS for the documented profile, Pion direct/TURN, live DeepSeek and a 10,000-connection load run passed. Linux Chromium decoded audio/video and TURN, plus live DeepSeek with independent MLS and restart/replay, also passed. The AI build matrix includes named participants, policy, scoped storage and external endpoints; local HTTP/SSE and Core-to-endpoint evidence is listed separately. Dedicated Linux/SSD qualification and independent audit are deferred; other scope limits are documented. See [verification](docs/verification.md) and [benchmarks](docs/benchmark.md) for exact scopes; no audited-cryptography or full production-release claim.

## License

Apache-2.0. `NOTICE` and `THIRD_PARTY_LICENSES.txt` retain dependency notices and license texts; cryptographic limitations are in the security documentation.
