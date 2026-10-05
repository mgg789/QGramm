# Retention and group fan-out follow-up — 2026-10-05

Final source `c951bec` retains indexed, bounded cleanup. The merged replay query was rejected and reverted; the production HPKE backend is unchanged. There is no unconditional latency or CPU-speedup claim. TOML presets/offline commands landed separately in `148de04`; see [configuration](configuration.md).

## Cause and changes

Before the fix, a 300 s diagnostic recorded 31 HTTP admission rejections, zero writer-queue rejections. The fixed 100 ms admission wait expired while writer transactions stalled 150–177 ms at minute cleanup ticks. Retention DELETEs scanned operations/events without expiry indexes on the same one-connection writer pool. Schema migration 2 atomically adds expiry indexes; cleanup deletes at most 256 rows per statement, yields 2 ms between batches, and drains within a 20 s context before continuing at a later minute tick. FULL commits, bounded queues, HTTP100ms wait and Retry-After remain unchanged.

The post-fix diagnostic recorded zero HTTP rejections/wait and zero writer rejects. Maximum observed sampled slow-record transaction-body duration was 11.75 ms versus 175.91 ms before; commit 31.06 ms. These are diagnostic ring samples, not exhaustive maxima or primary latency measurements. [Before](benchmarks/retention-fanout/diagnostic-before.json), [after](benchmarks/retention-fanout/diagnostic-after.json). Twenty 503s in the first ordinary intermediate soak were not reproduced in the timeline: their underlying stall source remains unconfirmed. Do not call every rejection resolved.

Group profiling showed X25519 arithmetic, syscalls and SQLite (_sqlite3VdbeExec 24.33% cumulative); cumulative stacks overlap. [CPU aggregate](benchmarks/retention-fanout/diagnostic-cpu.txt). A stdlib HPKE experiment did not improve native sealing time and was discarded: [microbenchmark](benchmarks/retention-fanout/hpke-experiment.json). Combining the two one-event SQL reads saved 3.4% mean CPU against refreshed controls, but group p99 rose to 25.80/251.85 ms versus 14.87/15.16 ms. That query fusion was reverted, with [the exact rejected patch](benchmarks/retention-fanout/rejected-replay-fusion.patch) preserved. Correctness tests alone did not qualify its performance.

## Measurements

| Run / measured phase | Accepted / planned | 503 / skipped | ACK p95 / p99 ms | Delivery p95 / p99 ms | CPU mean % | RAM max MiB |
|---|---:|---:|---:|---:|---:|---:|
| Group baseline r1 | 2000 / 2000 | 0 / 0 | 4.61 / 11.49 | 8.21 / 14.87 | 154.1 | 102.7 |
| Group baseline r2 | 2000 / 2000 | 0 / 0 | 5.53 / 9.83 | 9.10 / 15.16 | 159.0 | 93.0 |
| Group rejected r1 | 1997 / 2000 | 0 / 3 | 5.76 / 23.34 | 8.07 / 25.80 | 148.7 | 94.5 |
| Group rejected r2 | 2000 / 2000 | 0 / 0 | 123.77 / 214.89 | 134.63 / 251.85 | 153.6 | 92.5 |
| Group final r1 | 2000 / 2000 | 0 / 0 | 6.31 / 58.96 | 10.76 / 79.20 | 149.8 | 94.4 |
| Group final r2 | 2000 / 2000 | 0 / 0 | 4.48 / 8.06 | 7.51 / 11.30 | 153.4 | 96.6 |
| Soak historical | 149887 / 150000 | 62 / 51 | 3.67 / 10.20 | 3.91 / 10.35 | 31.4 | 152.6 |
| Soak intermediate r1 | 149873 / 150000 | 20 / 107 | 4.87 / 10.39 | 5.00 / 10.36 | 32.5 | 134.1 |
| Soak intermediate r2 | 149946 / 150000 | 0 / 54 | 4.10 / 9.56 | 4.24 / 9.66 | 31.8 | 127.0 |
| Soak final | 149859 / 150000 | 0 / 141 | 4.33 / 9.96 | 4.62 / 10.20 | 35.3 | 131.9 |

The final group CPU change versus the arithmetic mean of refreshed controls is -3.2%; it is an observation in this shared VM, not an established CPU optimization. Compare individual tails above; removing query fusion does not prove that all remaining ACK/delivery tails are fixed. Tradeoff: final soak mean CPU 35.3% versus 31.4% (+12.5%), delivery p95 rose 3.91→4.62 ms and sampled RAM fell 152.6→131.9 MiB. Complete schedule acceptance did not improve because generator skips increased.

Final soak: 149859 accepted of 150000 planned, 0 server rejections, 141 generator skips. Historical soak had 62 rejections/51 skips. All nine primary attempts verified 469,475 accepted records in history and 2,429,378 live deliveries, with zero missing/corrupt/unexpected/duplicate/out-of-order records. Failed SLO attempts remain in the table/raw JSON; diagnostic runs are excluded from these totals.

## Method and limits

Hardware: macOS 26.6.2, M5 Pro, 18 logical CPUs, 48 GiB host RAM. Docker Linux6.12.76/aarch64 VM has 18 vCPU and 8319213568 bytes RAM; each server requests 4 CPU/8 GiB, sharing the VM with 31 other containers. Local Docker volume, not dedicated Linux/SSD. Loopback HTTP/WebSocket transport without TLS, trusted-proxy benchmark configuration. Basic HPKE + storage AEAD + SQLite WAL/FULL, groups only, full HTTP response; no persistent device receipt POST. One synthetic device per user.

Both workloads use 2000 WebSockets and 256-byte plaintext. Soak: 1000 direct chats, 500 original messages/s for 300 s. Group: 10 chats with 100 recipients, rates 10/50/100/5 messages/s for 20 s each, 10 s idle before load; high phase is 10000 deliveries/s. Extra group users remain idle. Fresh volume/container per run; 60 s cooldown; sequential runs, no tests/builds during primary loads. The soak repeats use frozen generator SHA a8638dba7fa6e1dc21a77d60de3128109010897ab4945fbcebf2cbabf961cbef; groups use e5bcf3983a97fefa916978221f4de31f249d823ba39eaa36b4c89d2cb44887b2, matching their historical controls.

The baseline image is 1754e400cbc73e4cda3dbee2c8bda64f657e2a69dd5a2e93a1745ecd4a470f53 (source a16e26b); rejected image source `8b77c3b`; final source `c951bec`, Go 1.26.8, linux/arm64, no profiling tags. [Exact image/binary hashes and source](benchmarks/retention-fanout/final-provenance.json), [candidate provenance](benchmarks/retention-fanout/provenance.json). Configuration commit 148de04 is included in the final image. Runtime TOML and offered load remain unchanged. No competitor reruns.

CPU 100%=one core; CPU is the arithmetic mean of phase-labelled samples, RAM the sampled maximum. Delivery percentiles describe successful individual recipient deliveries from offer time, not last-recipient confirmation; scheduler lateness, rejected and skipped operations are separate. Repeats/ranges are exploratory, not confidence intervals. History is verified after timing. Admission SLO and accepted-message integrity are different: a skip, rejection or p99 > 250 ms fails the relevant SLO even when accepted data are intact.

Full steps and resource samples: [raw results](benchmarks/retention-fanout/results/soak-final.json), [all summary rows](benchmarks/retention-fanout/summary.json). Candidate/control/final run names in the summary identify every raw file. Two intermediate soaks, two intermediate groups, two refreshed groups, one final soak and two final groups were retained, plus three separate diagnostics. Shared storage and host scheduling remain limitations; an independent Linux/SSD qualification is deferred.

```sh
python3 tools/scenarios/run.py --service qgramm --image qgramm:retention-final \
  --generator /absolute/private-work/generator-v2 --env-generator /absolute/private-work/env-generator \
  --users 2000 --chats 1000 --fanout 1 --payload-bytes 256 --steps 500:300s \
  --idle 10s --out /absolute/private-work/soak.json
# Group: use the frozen group generator; --chats 10 --fanout 100
# --steps 10:20s,50:20s,100:20s,5:20s
python3 tools/scenarios/summarize_followup.py --out /absolute/private-work/summary.json
```

The image is built from an immutable source archive with the [group TOML/build instructions](../tools/scenarios/README.md); locally retained binary hashes identify the exact frozen generators. A future rebuilt generator can differ even from the same source/toolchain. Keep synthetic fixture secrets private. Profiling is a separate run with qg_bench_profile and timeline-only mode; no public pprof or real message content.

## Checks

Minimal/full 13-tag race, profile-tag core tests, 35 build/runtime profiles and 9 invalid configurations, the disposable Ed25519/WebSocket/HPKE example, vet, license inventory and documentation checks passed. Config change commands validate structure only; they do not establish readiness or production sizing. No AI expansion was implemented in this work.
