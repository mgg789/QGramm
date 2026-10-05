# Measured alternative baselines

Newer versions and broader active-chat/group/reconnect/idle/soak/file workloads are measured in the [scenario campaign](scenario-benchmark.md). This historical baseline remains tied to its original workload and versions.

[Русский](comparison-benchmark.md) · [QGramm measurements](benchmark.md)

All final runs used the same Apple M5 Pro (18 host logical CPUs), macOS 26.6.2,
Linux arm64 Docker Desktop VM (6.12.76-linuxkit), and 4 CPU / 8 GiB container
quotas. Docker reports about 7.748 GiB effective memory. The host is shared;
the SSD and fsync behavior of its virtual disk were not independently qualified.
Generators run on the host outside the server quota.

Each service opens 10,000 WebSocket connections, with one sender and one
recipient active. The workload is 100 messages/s for 30 seconds and a
five-second 1,000/s burst. Payloads contain 17 plaintext bytes before any
application encryption. TLS is excluded. QGramm publishes over HTTP and delivers
over WebSocket; the baselines publish and deliver over WebSocket.

| Configuration | Steady acceptance p95 / p99, ms | Steady delivery p95 / p99, ms | Total accepted / delivered | Sampled CPU / RAM maxima |
|---|---:|---:|---:|---:|
| QGramm minimal | 4.364 / 6.657 | 4.676 / 6.898 | 7,988 / 7,988 | 61.05% / 666.3 MiB |
| NATS JetStream 2.11.3, `sync_interval=always` | 3.945 / 6.641 | 4.018 / 6.621 | 8,000 / 8,000 | 23.50% / 469.3 MiB |
| NATS JetStream 2.11.3, default sync | 2.759 / 5.161 | 2.788 / 5.186 | 8,000 / 8,000 | 23.66% / 489.5 MiB |
| Centrifugo 6.2.3, memory history | 2.781 / 4.719 | 2.800 / 4.952 | 8,000 / 8,000 | 28.48% / 450.1 MiB |

CPU 100% denotes one CPU. Samples cover setup/load approximately every two
seconds, not exact peaks. These are individual final runs, not a maximum
throughput study or statistically ranked protocols. All final runs have zero
publish/receive errors and zero observed connection drops. The baselines also
check unique delivery and reject duplicates. A failed NATS setup attempt is
recorded separately; later runs use at least 60 seconds between connection
teardown and the next setup. No host network settings were changed.

QGramm performs per-request Ed25519 JWT/device/chat ACL checks, client-to-container
HPKE and recipient HPKE wrapping, and a SQLite WAL transaction with
`synchronous=FULL`. NATS uses a shared connection token, a single-replica file
stream and an explicit-ACK durable consumer; `always` calls file sync on the
normal publication path before PubAck. Default JetStream sync is deferred.
Centrifugo uses distinct HS256 connection JWTs and in-memory bounded recovery
history, with no durable chat database. The baselines do not supply QGramm's chat
transactions or application encryption. Crash/power-loss durability is not tested
in these comparative runs.

Latency starts at the generator before sending and ends at server ACK or the
recipient callback/read. QGramm includes sender HPKE sealing and JWT generation;
delivery excludes recipient decryption/rendering. Percentiles use nearest rank
`sorted[ceil(n*q)-1]` on successful observations. All runs have a bounded
64-request concurrency budget. QGramm's ticker submitted 7,988 operations;
baseline deadline scheduling submitted 8,000. Actual counts and phase durations
are published, rather than assumed from the nominal rate.

Reproduce from the repository root:

```sh
python3 tools/comparison/run.py --service nats --out work/nats.json
python3 tools/comparison/run.py --service nats --nats-sync always --out work/nats-sync.json
python3 tools/comparison/run.py --service centrifugo --out work/centrifugo.json
```

Docker Hub downloads failed on this host. The measured NATS/Centrifugo images
were built from official, pinned Go modules for Linux arm64 using
`tools/comparison/build-nats.sh` and `build-centrifugo.sh`, then selected through
`--image`. See the [Russian method](comparison-benchmark.md) for full source-build
commands, normal-path fsync source references and setup-failure details. Root
dependencies are unaffected; fixture SDKs and server sources have separate
`go.mod`/`go.sum`. Runners keep credentials out of evidence, use loopback-only
ports, clean up owned containers/volumes, and enforce a configurable overall
timeout and nonzero exit for failed integrity.

Raw results include versions, image IDs, generator hashes, resource samples and
phase metrics: [QGramm](benchmarks/minimal-10000-p99.json),
[NATS default](benchmarks/comparison-nats-10000.json),
[NATS sync always](benchmarks/comparison-nats-fsync-10000.json),
[Centrifugo](benchmarks/comparison-centrifugo-10000.json),
[failed setup](benchmarks/comparison-setup-failures.json).

Matrix/Synapse and Zulip are not measured here: their application/storage stacks
need separate workloads. Dedicated Linux/SSD qualification, longer runs, many
active chats, group fan-out and concurrent file workloads remain separate
measurements. Security comparisons await independent audit.
