# Shared messaging scenarios

This separate Go module measures QGramm, NATS JetStream and Centrifugo using the same open-loop workload, plaintext payload bytes, conversation topology and client integrity checks. It adds group fan-out, many simultaneously active conversations, larger payloads, overload/recovery phases and recipient reconnects. Resumable attachment transfers are a separate QGramm-only scenario.

These services have different contracts. This harness compares defined delivery paths; it does not establish protocol equivalence, security equivalence or production capacity.

## Build inputs

From the repository root:

```sh
cd tools/scenarios
go test -race ./...
go build -trimpath -o /absolute/private-work/scenarios .
cd ../..
python3 tools/scenarios/build-servers.py --help
python3 tools/scenarios/build-servers.py --work-dir /absolute/private-work/server-images --build
```

The Python command downloads the official Linux/arm64 NATS `v2.15.0` and Centrifugo `v6.9.7` release archives, verifies pinned archive and ELF binary SHA-256 hashes, selects one regular binary without extracting archive paths, and builds the small images used by this harness. Without `--build`, it only prepares verified inputs. `--offline` requires those same archives already present in the selected work directory and makes no network request. Each service directory contains a `provenance.json` with hashes and, after a build, the actual image ID.

Official sources: [NATS release](https://github.com/nats-io/nats-server/releases/tag/v2.15.0), [Centrifugo release](https://github.com/centrifugal/centrifugo/releases/tag/v6.9.7). The server binaries are pinned exactly. `alpine:3.22` remains a mutable base tag, so record the generated base/image IDs rather than claiming byte-identical future images.

QGramm uses the repository Dockerfile and a TOML-selected feature manifest. For group scenarios, save the following as `configs/scenario-group.toml`:

```toml
[server]
listen = "0.0.0.0:8080"
trusted_proxy = true
[storage]
path = "/data/qgramm.db"
files = "/data/files"
[features]
groups = true
[capacity]
expected_concurrent_users = 10000
```

For attachment scenarios, save a separate `configs/scenario-files.toml` with the same settings and retain `groups = true` and add `files = true`. Build each profile independently:

```sh
docker build --platform linux/arm64 --build-arg CONFIG=configs/scenario-group.toml -t qgramm:scenario-group .
docker build --platform linux/arm64 --build-arg CONFIG=configs/scenario-files.toml -t qgramm:scenario-files .
```

The insecure HTTP listener is for an isolated disposable benchmark network. It is not a production deployment example. The normal deployment requires HTTPS ingress.

## Authentication and storage contracts

| Service | Authentication and receive path | Accepted publication and retained history |
|---|---|---|
| QGramm | External Ed25519 JWT; one device per user; ticket-authenticated WebSocket; basic HPKE encryption/decryption | SQLite FULL/WAL commit; encrypted persistent messages and replay journal |
| NATS JetStream | Shared synthetic token; one WebSocket connection per user; distinct durable explicit-ACK consumer per recipient | `sync_interval=always`, FILE stream, one replica, server PubAck; retained stream messages. PubAck must not be equated with QGramm's FULL commit or a replicated quorum |
| Centrifugo | Distinct user HS256 JWT; multiplexed JSON WebSocket commands and channel subscriptions | In-memory history and publication reply; no durable storage or crash-recovery guarantee |

QGramm fixtures are synthetic: initialize a private env file with `go run ./cmd/qgramm-bench -init -env /absolute/private-work/fixture.env`. The generator needs `QGRAMM_BENCH_SIGNING_KEY` and `QGRAMM_MANAGEMENT_SECRET` from that file. Pass only the server's env values to the container; exclude the benchmark signing private key. Keep fixture files mode `0600`, never publish them, and use fresh disposable volumes per repeat.

NATS and Centrifugo read their synthetic shared authentication secret from `BENCH_SECRET`, never a command-line argument. Their server configuration must match it. NATS needs JetStream, WebSocket support, FILE storage and capacity for the harness's 20,000-consumer stream limit. Centrifugo's `bench.<chat>` channels need client subscribe/publish/history permission, `force_recovery=true`, history size 1,000,000 and TTL 900 seconds; `client.history_max_publication_limit` must allow 1,000,000. Use a fresh server for every repeat so its history starts empty. Do not expose these permissive benchmark settings publicly.

## Workload examples

The generator endpoint is HTTP for QGramm, `ws://...` for NATS, and `ws://.../connection/websocket` for Centrifugo. Invoke the same flags for each service, changing only its endpoint and credential source:

```sh
/absolute/private-work/scenarios -service qgramm -url http://127.0.0.1:8080 \
  -env-file /absolute/private-work/fixture.env -users 1000 -chats 100 -fanout 1 \
  -payload-bytes 256 -steps 500:20s,1500:20s,3000:20s,100:20s \
  -idle 10s -drain 30s -out /absolute/private-work/qgramm.json

# Substitute -service nats or centrifugo and its WebSocket URL;
# configure BENCH_SECRET securely in the process environment.
/absolute/private-work/scenarios -service nats -url ws://127.0.0.1:8081 \
  -users 1000 -chats 100 -fanout 1 -payload-bytes 256 \
  -steps 500:20s,1500:20s,3000:20s,100:20s -idle 10s -drain 30s \
  -out /absolute/private-work/nats.json

# Group fan-out: 20 independent senders, 25 recipients per conversation.
/absolute/private-work/scenarios -service qgramm -url http://127.0.0.1:8080 \
  -env-file /absolute/private-work/fixture.env -users 1000 -chats 20 -fanout 25 \
  -payload-bytes 256 -steps 100:20s,300:20s,100:20s -out /absolute/private-work/groups.json

# Disconnect real recipients during the second load phase, then recover.
/absolute/private-work/scenarios -service qgramm -url http://127.0.0.1:8080 \
  -env-file /absolute/private-work/fixture.env -users 1000 -chats 100 -fanout 1 \
  -steps 100:10s,300:10s,100:10s -reconnect 100 -offline 5s \
  -out /absolute/private-work/reconnect.json

# Files are QGramm-only: two 16 MiB resumable attachments.
/absolute/private-work/scenarios -service qgramm -url http://127.0.0.1:8080 \
  -env-file /absolute/private-work/fixture.env -users 2 -chats 1 -fanout 1 \
  -file-bytes 16777216 -file-count 2 -out /absolute/private-work/files.json
```

Topology is disjoint: each conversation has one sender and `fanout` recipients. All configured users connect; any extra users remain idle. Publications rotate across active conversations. Message rate counts original messages; expected receive rate is accepted messages multiplied by fan-out. Payloads contain the same 17-byte unique decimal ID prefix and filler for every service.

## Measurement and acceptance

The generator schedules work against an open-loop clock with bounded concurrent workers. It reports planned, scheduled, offered, accepted, rejected, uncertain and skipped counts separately. ACK and delivery latency begin at the actual offer timestamp, before dispatching the send worker. Scheduler lateness relative to the planned timestamp is reported independently; it is not added to these latency percentiles. p50/p95/p99 use nearest-rank percentiles over each phase's samples. An overload rejection is not silently treated as an accepted message.

Delivery means the receiving test client has checked the event and plaintext. It does **not** mean a persisted QGramm delivery receipt, read receipt or delivery to every device of a real user. NATS ACKs after that callback; QGramm checks HPKE plaintext. Every accepted ID must reach every intended recipient with exact payload, channel/chat and monotonic per-recipient sequence. At-least-once duplicates are counted explicitly. History validation runs after workload timing and checks each accepted payload rather than only comparing counts; truncated/expired history fails. Centrifugo retained history is limited to the live process.

Reconnect retains durable NATS consumer state and Centrifugo offset/epoch. Recovery must succeed. Replay origin latency includes the intentional offline duration; `resume_drain_ms` measures from reconnect initiation until the missing deliveries recorded at that boundary have arrived, including new connection handshakes. The highest tested SLO rate is a tested phase with p99 delivery ≤250 ms and complete acceptance/delivery without duplicates, skips or uncertain requests; it is not a proven maximum capacity.

Record server commit/image and generator hashes, architecture, CPU/memory limits, storage, transport/TLS mode, rate phases and per-repeat raw JSON. Sample server CPU/RAM by phase, keep host generator consumption separate, run services sequentially, cool down between repeats, and rotate ordering. Preserve unsuccessful runs. Publish individual repeats before aggregates; two repeats are exploratory evidence, not confidence intervals.

An arm64 Docker VM on a shared macOS host, loopback transport without TLS and a named volume qualifies only that environment. It cannot establish dedicated Linux/SSD capacity, cross-host network behavior, TLS cost, independent cryptographic audit or a head-to-head security ranking. Attachment capabilities and persistence guarantees are not comparable when the alternative has no equivalent feature. This README defines reproducible scenarios; measurement results belong in the campaign's evidence report.

## Disposable runner and complete campaign

The runner creates only UUID-named disposable containers/volumes, binds ports to
loopback, probes readiness and removes its own resources. It sets a 4 CPU / 8 GiB
quota, keeps fixture secrets in a private temporary directory, and records the
image, generator hash, configuration and phase-labelled resource samples.
Build the fixture initializer once:

```sh
go build -o /absolute/private-work/env-generator ./cmd/qgramm-bench
python3 tools/scenarios/campaign.py --generator /absolute/private-work/scenarios \
  --env-generator /absolute/private-work/env-generator --out-dir /absolute/private-work/results
# Same arguments, independently:
# --mode idle  : 10,000 connections, 20 seconds idle, then one message
# --mode files : two concurrent 16/64 MiB files, two repeats each
# --mode soak  : five minutes at the highest phase passing both distributed repeats,
#               or an explicitly exploratory 100/s fallback if none qualifies
```

Comparison mode runs distributed 1,000-chat/256-byte and 100-chat/4-KiB steps
500/1,500/3,000/100 messages per second for 20 seconds each; ten groups with
100 recipients at 10/50/100/5 original messages per second; and 100 chats at
200/s with 100 recipient reconnects after five seconds offline. All four profiles
use two repeats and reversed service order. Default cooldown is 15 seconds.
History verification has a separate 120-second budget outside latency timing.
`campaign.py` preserves failures and continues, so its exit code alone is not
an acceptance verdict; inspect each JSON's integrity, load and setup fields.

Runner checks: `(cd tools/scenarios && python3 -m unittest test_run.py)`.
