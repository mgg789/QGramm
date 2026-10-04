# Further performance changes and embedded Redis — 2026-10-04

This is historical evidence for the measured revisions. The Redis experiment has since been removed; its build, configuration and runner commands below require that historical checkout.

[Русский](performance-iteration.ru.md).

## Implemented changes

1. Hot SQL uses bounded, per-pool prepared statement caches (128 shapes), with
   transaction statements prepared before acquiring the sole writer connection.
   Cold transactional queries bypass the pool cache to avoid self-deadlock.
   SQLite driver changed from modernc1.38.2 to1.46.1 with matching libc1.67.6:
   the former reparses each execution; the latter reuses native statement handles
   for single SQL statements. [Upstream implementation](https://gitlab.com/cznic/sqlite/-/blob/v1.46.1/stmt.go).
2. A bounded message writer queue retains individual FULL-synchronous commits,
   transaction ACL/epoch/dedup checks, definitive outcomes on cancellation and
   response projection outside the writer. HTTP retains its active-request limit
   and adds a bounded pending queue with at most100 ms wait before Retry-After.
   Queue wait, service, commit and HTTP admission counters are available to the
   private benchmark instrumentation; there is no production debug endpoint.
3. A single fallback loop checks subscribed chat heads once per second in batches
   of500 instead of every subscribed socket issuing an empty replay query.
   Only changed/missing/new heads wake clients; reconnect still flushes its cursor
   immediately. Event delivery checks ACL, management revocations disconnect
   immediately, and a missing wake is recovered from the SQLite journal.

Implementation commits: `9cf77c2` (first three), `f4d4fd5` (Redis module),
`2871485` (empty child environment). No group commit or asynchronous durability
was introduced. Redis is an optional same-container Pub/Sub notification broker,
not a replacement for SQLite storage. Its healthy path adds IPC before fan-out;
failures fall back locally and the same one-second SQLite replay remains active.
[Lifecycle, configuration and limitations](redis.md).

## Method

- Same Apple M5 Pro/macOS26.6.2 shared host, Docker Desktop Linuxarm64
  kernel6.12.76-linuxkit. Every server container has a requested4CPU/8GiB quota;
  Docker's effective displayed memory ceiling is about7.748GiB. This is not a
  dedicated Linux/SSD qualification. Existing unrelated containers are untouched.
- Each variant has two runs of each profile, with10000 sockets, 10 seconds idle,
  100 messages/s for30 seconds and1000/s for5 seconds; cooldown at least60 seconds.
  Basic HPKE, full responses, 17-byte synthetic payload; no TLS work is measured.
  Host-side generator has fixed source/binary SHA and server Go1.26.8 is equal.
- Standard profile: one active direct chat and one subscribed recipient;9999
  sockets have no subscription. Subscribed profile:5000 direct chats,10000
  subscription commands,4999 empty inactive chats. Both active-chat users subscribe;
  delivery latency/count uses the designated recipient only. The sender also
  receives events, adding a small active fan-out cost in this second profile.
- Redis runs inside the same container quota as Go. Docker stats count both
  processes; version and process count are recorded. Child environment is empty.
  Only chat IDs pass through Redis. No external broker service is added.
- Per-run nearest-rank p95/p99 include successful requests. Steady percentiles
  and whole-run percentiles are separate; changing the mixture of successful
  steady/burst requests can change a whole-run percentile. Rejections are retained
  explicitly rather than classified as delivered messages. Delivery means arrival
  of a `message.created` WebSocket event; this load client does not decrypt it or
  submit persistent delivered/read ACKs. Integrity compares event/history counts,
  not a per-message received-ID set. Decryption, exact retries, cursor ACKs and
  replay have separate integration/fault coverage.
- CPU is mean phase-labelled approximately2-second Docker samples;100%=one CPU.
  Idle CPU uses median to reduce boundary contamination. RAM is sampled maximum
  within that phase, not exact process RSS or a true peak. Both repeats are shown.
- One functionally successful baseline run overlapped inspection tests. It is
  preserved with an exclusion reason and replaced by a clean repeat; it is absent
  from all comparative tables. No profiling instrumentation runs in timing comparisons.

## Results

Ranges are the minimum and maximum of **two repeats**, not confidence intervals. CPU and RAM refer to the steady phase; whole-run p99 includes the burst. Accepted, delivered-event and history counts matched in all 12 runs, with no unexpected errors or disconnections.

### One subscribed recipient

| Variant | Steady delivery p95, ms | Steady delivery p99, ms | Whole-run delivery p99, ms | CPU, % of one core | RAM, MiB | Burst rejections (1/2) |
|---|---:|---:|---:|---:|---:|---:|
| Before | 4.96–5.27 | 7.35–7.62 | 7.30–8.91 | 14.83–15.99 | 272.10–279.70 | 0/0 |
| First three | 5.01–5.08 | 7.20–7.46 | 7.32–14.55 | 14.31–14.76 | 273.20–281.00 | 0/0 |
| First three + Redis | 5.12–5.12 | 7.31–7.91 | 7.17–7.63 | 15.68–15.98 | 273.40–274.30 | 0/0 |

### All 10,000 sockets subscribed

| Variant | Steady delivery p95, ms | Steady delivery p99, ms | Whole-run delivery p99, ms | CPU, % of one core | RAM, MiB | Burst rejections (1/2) |
|---|---:|---:|---:|---:|---:|---:|
| Before | 4.54–4.70 | 6.40–6.54 | 70.37–85.77 | 78.60–80.39 | 497.90–501.00 | 47/161 |
| First three | 4.82–5.13 | 6.94–8.54 | 6.51–7.43 | 17.38–18.66 | 328.50–329.90 | 0/0 |
| First three + Redis | 4.90–4.93 | 6.74–6.91 | 6.99–37.97 | 17.05–17.85 | 328.60–344.20 | 0/0 |

### Interpretation

- With all sockets subscribed, the mean of the two CPU means fell **77.3%** and mean of RAM maxima fell **34.1%**. Idle CPU medians fell **82.53/82.88% → 1.83/1.90%** (97.7% by their means). Whole-run p99 fell **70.37/85.77 → 6.51/7.43 ms** (91.1% by mean per-run p99); burst rejections fell 47/161 → 0/0.
- Resource and mixed-run tail improvements do not mean every request became faster: mean steady p95/p99 rose 7.7%/19.6%. Two repeats do not establish statistical significance; the changed mixture of successful burst requests affects whole-run percentiles.
- Standard CPU fell about 5.7%; memory and steady latency remained close. One optimized repeat had a worse whole-run p99 of14.55 ms, so there is no universal latency improvement.
- Redis increased standard CPU **8.9%** versus the first three. Subscribed CPU changed −3.1% and RAM +2.2% by two-run means, without separating these small differences from noise. Whole-run p99 was37.97/6.99 ms versus6.51/7.43 ms without Redis: no repeatable benefit.

**SQLite with local notifications is the retained implementation.** The Redis experiment was removed after this comparison. This measures one same-container Pub/Sub wake broker design, not every possible Redis integration.

### Next changes to evaluate

1. Measure writer wait/service/commit and GC during longer runs before changing queue sizes.
2. Measure and reduce repeated ACL/projection SQL reads while preserving access checks and atomicity.
3. Exercise distributed chat activity, fan-out and longer soak, then repeat on dedicated Linux/SSD. This campaign does not calibrate production sizing.

## Writer diagnostic

Separate instrumented run: **2,000 subscribed users / 1,000 chats**, same 100/s + 1,000/s load. 7,991 accepted = events = history; zero commit errors, cancellations or writer rejections. Mean writer queue wait **0.491 ms**, service **0.725 ms**, commit **0.626 ms**. Commit accounts for **86.4% of writer service**, not end-to-end latency. It includes SQLite commit/driver work and does not isolate fsync.

HTTP admission did not wait for slots; writer DB pool wait delta during traffic was zero. Writer queue capacity was32. These counters do not provide queue p95/p99; profiling and forced GC affect timing, so this run is excluded from the 10,000-user comparison. Next measure phase-specific commit/queue distributions and correlate tails with GC/disk before enlarging queues.

[Diagnostic run](benchmarks/iteration-native-diagnostic.json) · [Counters and scope](benchmarks/iteration-native-diagnostic-counters.json). Raw pprof remains private and unpublished.

## Raw evidence

[Summary JSON](benchmarks/iteration-summary.json) · [Redis environment/process smoke](benchmarks/iteration-redis-env-smoke.json) · [Excluded overlapping run](benchmarks/iteration-baseline-standard-excluded-inspection-overlap.json)

- baseline: [standard 1](benchmarks/iteration-baseline-standard-1.json), [standard 2](benchmarks/iteration-baseline-standard-2.json), [subscribed 1](benchmarks/iteration-baseline-subscribed-1.json), [subscribed 2](benchmarks/iteration-baseline-subscribed-2.json)
- optimized: [standard 1](benchmarks/iteration-optimized-standard-1.json), [standard 2](benchmarks/iteration-optimized-standard-2.json), [subscribed 1](benchmarks/iteration-optimized-subscribed-1.json), [subscribed 2](benchmarks/iteration-optimized-subscribed-2.json)
- redis: [standard 1](benchmarks/iteration-redis-standard-1.json), [standard 2](benchmarks/iteration-redis-standard-2.json), [subscribed 1](benchmarks/iteration-redis-subscribed-1.json), [subscribed 2](benchmarks/iteration-redis-subscribed-2.json)

## Artifact provenance and reproduction

The JSON files record actual image IDs, server binary SHA256, Go version and
immutable source snapshot fingerprints. Archive/overlay build snapshots differ
from final exact Git trees (for example unused generator files/Go sums); their
implementation commits above identify the code, not a claimed exact build tree.
The native image received a license-only final layer without changing its server
binary. Redis was rebuilt after the environment fix before any primary Redis run.

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
# Build separate images at the compared revisions/profiles.
python3 tools/benchmark.py --image qgramm:minimal --out work/standard.json \
  --generator-binary work/qgramm-bench
python3 tools/benchmark.py --image qgramm:minimal --out work/subscribed.json \
  --generator-binary work/qgramm-bench --idle-subscriptions
python3 tools/benchmark.py --image qgramm:redis --redis --out work/redis.json \
  --generator-binary work/qgramm-bench --idle-subscriptions
```

Keep the same generator executable throughout, freeze runtime image/config and
run sequentially with cooldown. Runtime secret fixtures stay outside the image
and reports. This is a comparison of these concrete implementation profiles,
not a maximum throughput result, production capacity calibration, crypto audit,
or proof that every possible Redis integration behaves similarly.
