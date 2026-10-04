# Durable commit, SQL reads and allocations

[Русский](performance-sql-gc.ru.md).

## Implementation

Redis was removed in `4b4308e`: no broker process, feature/config/image/helper or client dependencies remain in current builds. Previous Redis reports are historical evidence.

`b131b63` changes the following hot paths:

- Already queued message jobs share a SQLite WAL **FULL** commit, up to 16 jobs and 8 MiB of stored payload/metadata. There is no wait timer; a large message runs alone. Each job checks current ACL/epoch/idempotency and runs SQL-only hooks in its own savepoint. A failed/cancelled job rolls back its savepoint; an unrecoverable transaction/commit error fails all tentative successes. Results and notifications follow the successful durable commit. Exact retry resolves an uncertain/lost response. Concurrent messages can share an atomic commit; the HTTP batch API retains per-item results and sequential submission.
- Chat/ACL preflight is one joined read; message projection obtains recipient access and public key in the same fresh SQL snapshot. Transaction checks remain. No cross-request ACL/key cache or trusted-membership bypass is introduced. Full send read-helper calls drop6→4 including HTTP device authentication; nonempty Events drops4→3 and empty Events2→1. Optional projection hooks may add their own reads.
- Single-message projection avoids the ID maps used by batched replay. Typed metadata avoids dynamic-map marshaling; incremental hashing avoids copying the entire envelope JSON into a concatenated buffer. Queued input releases references to plaintext/envelopes; attachments are cloned to preserve metadata ownership and nil/empty wire semantics.
- GC settings and HPKE remain unchanged. Improvements target allocations rather than increasing GC thresholds. Diagnostic read counts, queue/transaction histograms and GC counters exist only with `qg_bench_profile`; production has no histogram state or debug endpoint.

Batch statistics distinguish commit transactions, completed jobs and committed new messages. `commit_count` is not the number of messages. Queue wait is per job, writer service is per batch (including commit), and commit duration includes SQLite/driver work; it does not isolate fsync.

## Method and boundaries

Before: exact `a78ebcf` minimal image, Redis compiled out. After: implementation `b131b63`; each raw artifact records actual immutable build snapshot/image/binary hashes and any overlay. Both server builds use Go 1.26.8. The generator binary and runner bytes stay identical.

Two repeats of each standard/subscribed profile per version:8 primary runs, sequential with at least 60s cooldown,10,000 WebSockets,10s idle,100 messages/s for30s plus1,000/s for5s. Standard has one subscribed recipient; subscribed has5,000 direct chats and10,000 subscription commands (both users in the active chat subscribe). Basic HPKE,17-byte synthetic plaintext and full HTTP responses. TLS, large files and a distributed active-chat workload are not measured.

Hardware: Apple M5 Pro, 18 host logical CPUs, macOS 26.6.2, Docker Desktop Linuxarm64 VM kernel 6.12.76-linuxkit. Server quota 4 CPU/8 GiB; displayed effective RAM about 7.748 GiB. Shared host, Docker volume SQLite, no independently qualified Linux local SSD. Quotas are not dedicated reservations. Redis is absent from both measured binaries.

Delivery means `message.created` arrival at the designated recipient. This generator compares accepted/event/history counts, not exact received ID sets, payload decryption or persistent delivered/read ACKs. Separate protocol/fault tests cover those contracts. Percentiles are per-run nearest rank over successful requests; rejections are published separately. Whole-run percentile includes the burst and its successful-request mixture. CPU is mean phase-labelled samples (100% = one core); RAM is phase sampled maximum, not exact RSS peak. Two-run ranges are not confidence intervals.

Separate instrumented 2,000-user subscribed runs compare SQL-read counts, allocations and GC/commit/queue counters. Identical profiling overlays and forced-GC checkpoints affect timing and are excluded from primary comparisons. Allocation/GC deltas include traffic and history projection, rather than isolating one operation. Histograms give bucket counts/upper bounds, not exact p95/p99. Raw pprof files remain private because profiles can retain sensitive runtime memory.

## Primary results

Ranges across two repeats. Latency is delivery; steady p95/p99 describe the sustained phase. CPU/RAM also refer to steady. All8 runs total **63,908 accepted = events = history**, zero rejections or unexpected errors.

| Profile / version | Steady p95, ms | Steady p99, ms | Whole-run p99, ms | CPU, % one core | RAM, MiB |
|---|---:|---:|---:|---:|---:|
| standard / before | 5.04–5.16 | 7.38–8.01 | 6.99–10.80 | 13.47–13.84 | 271.20–281.60 |
| standard / after | 5.09–5.30 | 7.04–7.52 | 6.52–6.98 | 12.60–14.28 | 272.70–281.60 |
| subscribed / before | 4.77–4.84 | 6.66–7.45 | 6.56–20.88 | 15.50–16.12 | 330.10–347.90 |
| subscribed / after | 4.94–4.95 | 6.88–6.96 | 6.25–31.99 | 14.96–15.41 | 329.90–334.30 |

Arithmetic means of **per-run** values, not pooled percentiles:

- Standard: steady p99 −5.4%, p95 +2.0%; whole-run p99 −24.1%. CPU −1.6%; RAM +0.3%.
- Subscribed: steady p99 −1.9%, p95 +2.8%; whole-run p99 **+39.3%**. CPU −4.0%; RAM −2.0%. Second subscribed repeat worsened whole-run p99 **20.879 → 31.994 ms**; first improved **6.563 → 6.246 ms**.

This is a modest reduction of some costs with mixed latency, not a universal speedup. Two repeats on a shared host cannot establish the cause of each tail or significance of small CPU/RAM differences. This campaign does not isolate the subscribed burst-tail cause; it cannot be attributed automatically to GC or claimed fixed.

[Summary JSON](benchmarks/sqlgc-summary.json)

- standard: [before 1](benchmarks/sqlgc-before-standard-1.json), [before 2](benchmarks/sqlgc-before-standard-2.json), [after 1](benchmarks/sqlgc-after-standard-1.json), [after 2](benchmarks/sqlgc-after-standard-2.json)
- subscribed: [before 1](benchmarks/sqlgc-before-subscribed-1.json), [before 2](benchmarks/sqlgc-before-subscribed-2.json), [after 1](benchmarks/sqlgc-after-subscribed-1.json), [after 2](benchmarks/sqlgc-after-subscribed-2.json)

## Separate commit/GC diagnostic

One subscribed before/after run with **2,000 users**, four identical requested forced-GC checkpoints (idle/steady/burst/final). 7,998/7,989 accepted = events = history, no errors. Final-minus-idle intervals were **45.170 / 43.495s**, including history, background and profiling work; normalization by accepted is not isolated send cost.

| Counter | Before | After |
|---|---:|---:|
| Allocated bytes / accepted¹ | 101,801 | 98,725 (−3.0%) |
| Allocations / accepted¹ | 1,622 | 1,531 (−5.6%) |
| SQL read-helper calls / accepted¹ | 13.385 | 9.701 (−27.5%) |
| Commit transactions / accepted | 7,998 / 7,998 | 7,824 / 7,989 |
| Total commit time | 5.418s | 4.754s (−12.2%) |
| Total writer service time | 6.218s | 5.528s (−11.1%) |
| Sum of job queue waits | 8.800s | 0.551s (−93.7%) |
| Total GC pause time | 28.922ms | 25.998ms (−10.1%) |
| GC cycles, including forced | 48 | 48 |

¹ Whole measured interval including traffic/history/background divided by accepted. Read-helper counts exclude write and driver-internal SQL. Read-helper calls/s fell2,370→1,782 (24.8%); allocation bytes/s stayed18.03→18.13 MB/s because interval durations differ. This is not a throughput benchmark.

Mean commit time per accepted message was **0.677→0.595ms**; per commit transaction **0.677→0.608ms**. Only **1.021 messages share each commit** here: most jobs have no already queued neighbours. No timer artificially grows batches. Commit count falls about2%, not by the maximum batch-size factor16. This is not a count of fsync calls.

Interval histogram buckets show277 waits longer than8ms before and zero after. These are coarse instrumented counters, not exact p95/p99 or an explanation of the worse primary subscribed tail. GC cycles did not decline; GC was not eliminated. Raw pprof remains private.

[Diagnostic summary](benchmarks/sqlgc-diagnostic-summary.json) · [Before run](benchmarks/sqlgc-before-diagnostic.json) / [counters](benchmarks/sqlgc-before-diagnostic-counters.json) · [After run](benchmarks/sqlgc-after-diagnostic.json) / [counters](benchmarks/sqlgc-after-diagnostic-counters.json).

## Next measurements

1. Correlate subscribed burst tails with per-request commit/body/projection/queue/GC time series without forced GC. These aggregates do not establish the cause of31.994ms.
2. Allocation reduction is only3% versus27.5% fewer read-helper calls: focus the next profile on remaining HPKE/JSON/driver allocations while preserving fresh cryptographic contexts and ACL checks. Raising GOGC is not yet supported by this evidence.
3. Measure many active chats and sustained writer saturation separately; opportunistic grouping has little to combine at100/s. Longer soak/dedicated Linux SSD are needed for calibration, without rebranding Docker data as qualified production sizing.

## Verification

`go test -race ./...`, full 13-feature race tests, profile-tag tests, `go vet ./...`, and `go run -race ./examples/basic` passed. The28-profile/7-invalid build matrix passed after Redis removal. Independent code review and inspection passed; the input-slice ownership issue found in review was fixed before final images. Group tests use real SQLite for no early ACK/fan-out, controlled commit failure, actual SQLITE_FULL/recovery, cancellation before/during a job/during commit, savepoint hook failure, dedup and ordering, count/byte bounds. Projection tests cover current member/device/user access, key rotation, tombstone/hooks and wire-compatible digest/metadata.

## Reproduction

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
docker build --build-arg CONFIG=configs/container.toml -t qgramm:sqlgc .
python3 tools/benchmark.py --image qgramm:sqlgc --out work/standard.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s
python3 tools/benchmark.py --image qgramm:sqlgc --out work/subscribed.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s --idle-subscriptions
```

Build compared images in separate checkouts, record source/image/binary hashes and use one generator. Run two repeats sequentially with at least60s cooldown; keep profiler runs separate. Historical JSON records the frozen runner fingerprints; current commands reproduce the workload, without promising bit-identical future builds/toolchains.

For diagnostic checkpoint reproduction, the private wrapper changed only the sampler predicate to `args.profile_dir and phase in ("idle", "steady", "burst") and phase != last_phase`; the final SIGUSR1 followed load completion. Exactly4 runtime snapshots were checked. Wrapper SHA is recorded in JSON. The current runner without that selection can capture additional setup/history checkpoints, so those profiles are not equivalent to this campaign.
