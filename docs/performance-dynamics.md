# Performance changes, 2026-10-04

[Русский](performance-dynamics.ru.md). These are repeated measurements on one
Mac/Docker Desktop host, not dedicated Linux/SSD qualification.

## Changes

Original core: `658346e`; optimized core: `31d31b2`.

1. Separate development builds capture CPU/heap/block/mutex profiles and SQL
   pool statistics. The normal container has no profiling/debug HTTP endpoints.
2. Idle, unsubscribed sockets no longer wake every second or independently
   query device validity. Device checks and event notifications are batched.
   The HTTP handler returns after upgrade; core owns the socket lifetime,
   releasing HTTP handler stacks/buffers. Direct revocation, periodic validity
   checks and durable replay remain. Subscribed sockets retain a one-second
   fallback replay timer.
3. Message projection is batched. Optional `Prefer: return=minimal` returns a
   durable ID/sequence receipt without rereading/re-encrypting content for the
   sender. The full response remains the default; both modes are measured.
4. SQLite WAL now has a bounded read pool and a separate single writer.
   `synchronous=FULL`, message/event/idempotency atomicity and transactional ACL
   checks remain.

## Method

- Apple M5 Pro, macOS 26.6.2; Docker Desktop Linux arm64 VM. Each server has
  4 CPU/8 GiB container limits; generators run on the host outside that quota.
- 10,000 distinct users/sockets, one active direct chat, remaining connections
  idle. The plaintext payload is 17 bytes before application encryption.
- After connections: 10 seconds idle, 30 seconds at 100 messages/s, then
  5 seconds at 1,000/s. At least 60 seconds between successful load runs allows
  host socket state to recover.
- Both QGramm timing binaries use Go 1.26.8 and the minimal feature profile;
  evidence records image ID, server binary SHA256 and source commit. Generators
  use Go 1.26.4. Separate before/after profiling builds both use Go 1.26.4.
- Percentiles use nearest rank over successful requests. ACK means durable
  acceptance; delivery means a test receiver obtained the event. The stored
  history count is also checked.
- Docker stats samples roughly every two seconds carry phase labels. Idle CPU
  is the median sample; steady CPU is the arithmetic mean. 100% is one CPU.
  RAM is a phase sampled maximum, not exact peak RSS. Burst has only 2–3 CPU
  samples, insufficient for confident average estimates.
- A phase's first sample can contain preceding work. After/round2 has a 46.1%
  idle sample crossing setup; raw evidence retains it. Idle medians are used
  consistently for every service. The helper also reports a mean excluding
  the first sample of each phase for every service.
- Runs are sequential, not randomized, with no confidence intervals. Two
  repetitions show variation, not a universal ranking. TLS, 10,000 active
  senders, large fan-out and large-file traffic are outside this workload.

## Two repetitions: original and optimized QGramm

Steady includes only the 100/s phase. The last column is the p99 for the
**whole** run including burst, not a separate burst percentile.

| Variant / repetition | Steady ACK p95/p99 ms | Steady delivery p95/p99 ms | Idle CPU median | Steady CPU mean | Steady/burst RAM MiB | 503 | Whole-run delivery p99 ms |
|---|---:|---:|---:|---:|---:|---:|---:|
| Before / 1 | 5.098 / 7.479 | 5.309 / 7.654 | 9.32% | 20.204% | 583.0 / 664.9 | 0 | 71.491 |
| After, full / 1 | 5.159 / 6.757 | 5.260 / 7.021 | 0.08% | 14.767% | 282.4 / 284.0 | 83 | 71.159 |
| After, receipt / 1 | 4.793 / 6.949 | 5.262 / 7.178 | 0.07% | 14.094% | 271.3 / 275.6 | 0 | 7.621 |
| Before / 2 | 5.080 / 7.180 | 5.309 / 7.341 | 10.84% | 19.533% | 587.6 / 665.2 | 0 | 25.275 |
| After, full / 2 | 4.968 / 7.162 | 5.128 / 7.257 | 0.11% | 14.252% | 271.9 / 281.1 | 20 | 29.157 |
| After, receipt / 2 | 4.862 / 6.763 | 5.192 / 7.193 | 0.07% | 13.542% | 271.4 / 274.5 | 0 | 25.412 |

Full responses reduced steady RAM by **51.6% / 53.7%**, burst RAM by
**57.3% / 57.7%**, and mean steady CPU by **26.9% / 27.0%** in the corresponding
repetitions. Median idle CPU fell about 99%.

Steady latency is similar. Whole-run p99 did not consistently improve: full
response repetition 2 increased from 25.275 to 29.157 ms. Managed burst
backpressure appeared: 83 and 20 rejections. Round 2 identifies
`request capacity reached`; round 1 did not record the public reason. Every
accepted message was delivered and found in history: 7993/7990 before,
7901/7965 after/full, 7982/7993 after/receipt. No loss of accepted messages or
unexpected disconnections was observed. 503 requests were not retried in this
fixture and are separate from successful-request percentiles. The QGramm ticker
can under-offer the nominal 8,000 operations; `offered` records actual attempts.

Receipt mode had no backpressure in either repetition. Relative to after/full,
whole-run delivery p99 improved 89.3% / 12.8%. Relative to the original core,
receipt repetition 2 was effectively unchanged: 25.412 vs 25.275 ms. This is
not a promise of a uniform tail improvement at every load.

## Two repetitions against alternatives

These are ranges across two runs, not percentiles calculated by pooling or
averaging runs. CPU steady uses 14–15 samples per run; the JSON retains all
samples and phase/burst metrics. All alternative runs accepted/delivered all
8,000 operations with zero publish, receive or duplicate errors.

| Service / mode | Steady ACK p95 range ms | Steady ACK p99 range ms | Steady delivery p95 range ms | Steady delivery p99 range ms | Mean steady CPU range | Steady sampled RAM max range MiB |
|---|---:|---:|---:|---:|---:|---:|
| QGramm before | 5.080–5.098 | 7.180–7.479 | 5.309–5.309 | 7.341–7.654 | 19.533–20.204% | 583.0–587.6 |
| QGramm after/full | 4.968–5.159 | 6.757–7.162 | 5.128–5.260 | 7.021–7.257 | 14.252–14.767% | 271.9–282.4 |
| QGramm after/receipt | 4.793–4.862 | 6.763–6.949 | 5.192–5.262 | 7.178–7.193 | 13.542–14.094% | 271.3–271.4 |
| NATS 2.11.3 JetStream, always fsync | 4.263–4.889 | 6.524–6.712 | 4.299–4.971 | 6.523–6.746 | 3.994–4.399% | 458.6–465.9 |
| Centrifugo 6.2.3 memory history | 3.146–3.520 | 5.064–5.211 | 3.131–3.544 | 5.184–5.211 | 7.789–8.152% | 424.6–479.4 |

QGramm now uses less sampled RAM in this workload, but still uses more CPU and
has higher steady latency. The complete application paths differ: QGramm
verifies Ed25519 identity/ACL, idempotency, chat/event transactions and HPKE.
Its latency includes host-side HPKE/token creation and HTTP publication.
NATS uses a shared connection token, plaintext WebSocket publication and a
file stream with explicit consumer ACK, replicas=1, `sync_interval=always`.
Centrifugo uses connection HS256 JWT and bounded memory history without durable
chat storage. No TLS is used in any isolated fixture. These differences prevent
interpreting the table as equal-security/equal-function protocol performance.

Evidence: [NATS/1](benchmarks/perf-nats-round1.json),
[NATS/2](benchmarks/perf-nats-round2.json),
[Centrifugo/1](benchmarks/perf-centrifugo-round1.json),
[Centrifugo/2](benchmarks/perf-centrifugo-round2.json),
[all phase summaries](benchmarks/perf-dynamics-summary.json).
The [original comparison methodology](comparison-benchmark.en.md) describes
durability and authentication differences in more detail. Original historical
single-run results remain separately dated; they are not substituted for the
fresh before/after measurements here.

## Profiles

Profiling forces GC and serializes large stack collections; its latency is
excluded from the table. Summaries: [before](benchmarks/perf-profile-before-summary.json),
[after](benchmarks/perf-profile-after-summary.json).

| After 10,000 connections, forced GC | Before | After |
|---|---:|---:|
| Live Go heap MiB | 190.14 | 67.16 |
| Stack memory MiB | 196.62 | 79.09 |
| Goroutines | 20,024 | 20,026 |
| Aggregate writer SQL wait, post-setup to late-load snapshot | 50.97 s | 6.67 s |
| Writer SQL wait count over that interval | 52,837 | 2,234 |

Heap fell 64.7%, stacks 59.8%. Goroutine count barely changed; retained HTTP
stacks/buffers were released. SQL wait sums all waiting goroutines and can exceed
wall-clock time. The new reader pool recorded only eight waits; adding reader
workers is not an evidenced first priority.

Across the entire after CPU profile, SQL VM accounts for 21.3% cumulative and
SQL parsing for 14.1% cumulative. These overlapping stacks cannot be added.
The profile also includes setup. Filtering to `sendMessage` shows 0.88 CPU-s
in parser `_yy_reduce`, 1.24 CPU-s in SQL VM and 1.08 CPU-s in syscalls. HPKE
computation remains; the profile does not support blaming all overhead on crypto.

## Reproduction and provenance limits

Commands/private profiles: [tooling README](../tools/performance/README.md).
Original evidence: [before/1](benchmarks/perf-before-round1.json),
[before/2](benchmarks/perf-before-round2.json),
[after/1](benchmarks/perf-after-round1.json),
[after/2](benchmarks/perf-after-round2.json),
[receipt/1](benchmarks/perf-receipt-round1.json),
[receipt/2](benchmarks/perf-receipt-round2.json).

[Failed connection setup](benchmarks/perf-before-setup-failure.json) is retained
separately and excluded from latency tables. Receipt/2's generator source changed
after the initial fingerprint/build window. The legacy runner hashed source at
the end, so that hash is not guaranteed to identify the compiled generator.
Server commit/binary remained fixed; the evidence states the limit. The new
runner captures initial source hash, immutable generator binary SHA256 and a
source-change flag.

## Next changes

1. Prepare hot SQL statements; measure parser/VM CPU and allocations while
   preserving current transactions and ACL.
2. Diagnose burst admission using writer wait, fsync, GC and occupied HTTP
   slots in ordinary builds. Then test a bounded writer queue or small commit
   groups; ACK only after durable commit. Increasing in-flight limits without
   measurement may merely increase p99.
3. Batch fallback replay for subscribed sockets, which still have a one-second
   timer. Verify wake overflow recovery and fairness, then benchmark many
   concurrently active subscriptions.
4. Add active-chat/group fan-out workloads, a long soak and dedicated Linux/SSD
   qualification. Mostly idle 10,000 sockets do not establish throughput for
   10,000 active senders.
