# Replay SQL and optional WAL checkpoint

[Русский](performance-tail.ru.md).

## Implementation

`0257fd2`, followed by `bc73c00`, keeps the durable message/event/operation transaction and FULL synchronization unchanged. History reads current membership, device/user status, recipient key and an ordered bounded page in **one** SQL helper call instead of three. Nonempty event replay uses **two** instead of three; empty replay remains one. HTTP authentication and optional hooks are additional reads. The common one-event replay in `bc73c00` uses a bounded range query without window ranking. It returns the nearest available event through the observed head; concurrent appends remain for the next replay/Wake. Backlog page ranking is bounded to 200 events; repeated references fetch/project each message once, preserving current tombstones and hook semantics. Rows close before hooks and HPKE work.

A recipient key is parsed once per authorized projection batch. Every envelope uses a fresh HPKE sender/setup/encapsulation and unchanged binding; no key or ACL cache crosses requests.

`storage.checkpoint_interval_ms=0` is the default and creates no extra worker/pool. The opt-in candidate uses 100ms and `wal_checkpoint_bytes=1048576`: a dedicated writable connection attempts PASSIVE checkpoints, retains FULL and SQLite automatic checkpoint, skips an unchanged completely processed WAL, and retries partial/busy/error outcomes. It cannot bound WAL while readers pin transactions; checkpoint I/O can compete with commits. [Configuration](configuration.md), [SQLite WAL](https://www.sqlite.org/wal.html).

## Method

Baseline is the exact previous `b131b63` minimal image; after is an immutable `0257fd2` archive. Both use Go 1.26.8 and the same frozen generator binary. Three variants: before, SQL with checkpoint disabled, SQL plus optional checkpoint. Two repeats each of standard/subscribed profiles: 12 uninstrumented primary runs, sequential with 60s cooldown. The first two baseline attempts overlapped host-side source checks; they were excluded and repeated after the campaign, giving 14 primary attempts and 12 included results. No source checks/builds run alongside the included measurements. Unrelated shared-host services remain a limitation.

Load remains 10,000 WebSockets,10s idle,100 messages/s for30s and1,000/s for5s; 17-byte HPKE payload and full HTTP responses. Standard has one subscribed recipient; subscribed has5,000 paired chats/10,000 subscriptions and one active conversation, with both peers subscribed. This does not add the deferred sustained writer-saturation workload.

Host: Apple M5 Pro,18 logical host CPUs, macOS 26.6.2, Docker Desktop Linuxarm64 VM. Server quotas 4 CPU/8 GiB; generator on the host. SQLite is on a Docker named volume, without dedicated Linux/SSD qualification. TLS is not measured.

Percentiles are successful-request nearest-rank per run. Means of per-run percentiles are not pooled percentiles. CPU is phase-labelled Docker stats mean (100%=one core); RAM is sampled maximum, not exact RSS peak. Two repeats are not confidence intervals. Accepted/event/history counts do not verify exact received ID sets, recipient decryption or persistent device receipts; separate protocol tests cover those contracts.

## First candidate and correction

The first 12-run campaign is retained in [raw summary](benchmarks/tail-summary.json). Its consolidated SQL reduced reads but **regressed** steady delivery p99 by 14.2% standard and 7.8% subscribed; CPU rose 4.3% and 12.2%. It was not merged on those results. Optional checkpoint changed p99 versus that SQL candidate by -6.9% standard and +3.8% subscribed, with no consistent CPU benefit. The default stays disabled.

A separate local replay microbenchmark isolated the cost of the common one-event call: three repetitions of 10,000 calls after 100 warmups, one stored HPKE message, no HTTP or measured writes, natural GC. Native Go1.26.4/macOS arm64 differs from primary Docker Go1.26.8. The same private fixture was copied into separate immutable source copies. [Recorded samples and fixture hash](benchmarks/tail-replay-cost.json).

| Replay implementation | Mean µs/call | Bytes/call | Allocations/call |
|---|---:|---:|---:|
| Previous SQL | 58.88 | 16,416 | 255.05 |
| First ranked query (`0257fd2`) | 108.29 | 15,647 | 235.05 |
| One-event range path (`bc73c00`) | 54.10 | 15,647 | 235.05 |

The range path reduces replay time 8.1%, allocated bytes 4.7% and allocations 7.8% against the previous implementation in this microbenchmark. This is not an end-to-end latency or maximum-throughput claim. Independent recipient parsing tests additionally save 304 bytes/five allocations for each envelope after the first in a batch; their timing ranges overlap, so no HPKE speedup is claimed.

## Final same-load follow-up

Four further uninstrumented runs measured exact `bc73c00`, checkpoint disabled: standard1, subscribed1, subscribed2, standard2, each after60s cooldown. They reuse the four fresh `b131b63` controls from the first campaign, the identical frozen runner/generator, quota and workload. These later runs were not interleaved with randomized new controls; shared-host drift remains a limitation. Total included primary runs:16; two excluded attempts remain private. [Final comparison and raw links](benchmarks/tail-final-summary.json).

| Profile | Steady delivery p95, before→final ms | p99, before→final ms | CPU, before→final % | RAM, before→final MiB |
|---|---:|---:|---:|---:|
| standard | 4.841→5.055 | 7.055→7.685 | 13.73→13.86 | 279.8→273.9 |
| subscribed | 4.688→4.912 | 6.249→7.399 | 15.72→16.64 | 330.9→337.6 |

Final results are mixed/negative: standard steady p99 +8.9%, CPU +0.9%, RAM -2.1%; subscribed p99 +18.4%, CPU +5.9%, RAM +2.0%. Whole-run p99 means rise7.9%/4.1%. The local replay savings do not establish an end-to-end improvement. The reviewed implementation and evidence stay on **dev**; the performance candidate is **not merged into main**. Saturation and dedicated Linux/SSD qualification remain deferred until separately requested. PASSIVE checkpoint remains opt-in; no final range-path checkpoint campaign was run.

The four final runs have 31,963 offered=accepted=events=history; all16 included primary runs have 127,836. Each reports zero failures, backpressure, skipped generator slots, unexpected disconnects and WebSocket errors. These are counts, with the protocol verification limits above.

| Run | Delivery steady p95/p99, ms | Whole-run p99, ms | Steady CPU % | RAM max sample, MiB |
|---|---:|---:|---:|---:|
| [before standard 1](benchmarks/tail-before-standard-1.json) | 4.363/6.289 | 5.756 | 13.560 | 280.0 |
| [before standard 2](benchmarks/tail-before-standard-2.json) | 5.319/7.821 | 7.408 | 13.901 | 279.5 |
| [fast standard 1](benchmarks/tail-fast-standard-1.json) | 4.940/7.677 | 6.514 | 13.295 | 276.7 |
| [fast standard 2](benchmarks/tail-fast-standard-2.json) | 5.170/7.693 | 7.693 | 14.419 | 271.1 |
| [before subscribed 1](benchmarks/tail-before-subscribed-1.json) | 4.927/6.427 | 6.576 | 16.365 | 324.0 |
| [before subscribed 2](benchmarks/tail-before-subscribed-2.json) | 4.450/6.071 | 6.263 | 15.075 | 337.8 |
| [fast subscribed 1](benchmarks/tail-fast-subscribed-1.json) | 4.869/7.677 | 6.905 | 16.684 | 335.0 |
| [fast subscribed 2](benchmarks/tail-fast-subscribed-2.json) | 4.956/7.121 | 6.455 | 16.597 | 340.2 |

## Diagnostics

Separate 10,000-user subscribed runs use the same frozen generator and a private `qg_bench_profile` timeline-only build. Every 100ms, plus requested phase/calibration snapshots, records aggregate SQL query lifetime, queue/body/commit/service/projection/WS timings, natural GC pauses and WAL size. No forced GC or CPU/block/mutex sampling profiler is enabled. The observer itself changes costs, so these timings are excluded from primary comparisons.

The baseline diagnostic image has the identical diagnostic/checkpoint infrastructure overlaid on `27cc7e5`, with checkpoint disabled and original message SQL/HPKE retained. The sole messages.go overlay times the original post-SQL projection loop. Source/image/binary and runner hashes are recorded; both diagnostic images additionally overlay the identical signal-marker profiler for clock alignment.

A 64-entry ring retains stage durations >=8ms and all optional checkpoint attempts. The summarizer reports detected overwrite between 100ms snapshots. Phase boundaries come from host polling the frozen generator phase file every 100ms. Three signal-marked host/container timestamp pairs are captured before and after each run; the minimum-round-trip pair on each side aligns the phase boundaries to container time. The summarizer rejects clock uncertainty or observed offset drift above 100ms. Query lifetime includes preparation, pool waits and consumption until Scan/Close; it excludes direct non-helper SQL. Stage spans overlap and must not be summed. Automatic checkpoint is not separately identified inside commit. Temporal GC overlap does not prove causation or measure GC assist/scheduler stalls. Raw timelines stay private; published summaries contain only aggregate timings/counters.

The three diagnostic runs describe the **first candidate**, not the later range-path correction. [Before](benchmarks/tail-before-timeline-summary.json), [SQL](benchmarks/tail-sql-timeline-summary.json), [SQL + checkpoint](benchmarks/tail-checkpoint-timeline-summary.json).

| Sampled interval counters | Before | Ranked SQL | Ranked SQL + checkpoint |
|---|---:|---:|---:|
| Committed messages in interval | 7,961 | 7,965 | 7,991 |
| Read-helper calls | 77,019 | 61,148 | 62,814 |
| Allocated bytes, MiB | 684.5 | 665.7 | 682.6 |
| Natural GC cycles | 9 | 9 | 10 |
| Total GC pause, ms | 5.39 | 5.75 | 6.18 |
| Largest recorded GC pause, ms | 1.75 | 1.88 | 1.50 |
| Largest sampled WAL, MiB | 4.32 | 4.05 | 4.47 |

The old 31.994ms subscribed whole-run p99 was not reproduced in fresh controls. One ranked diagnostic run recorded six commits >=8ms (maximum55.44ms), with no temporal overlap with recorded stop-the-world GC pauses. The before and checkpoint runs had no commits above that threshold. This does not identify fsync or checkpoint as the cause: scheduler/I/O contention and automatic checkpoint remain unseparated. The optional worker made351 attempts, average2.20ms, maximum4.62ms, with no failed attempts. WAL physical size was not reduced consistently; PASSIVE does not truncate it. No slow-ring overwrites were detected. GC settings remain unchanged.

## Reproduction

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
docker build --build-arg CONFIG=configs/container.toml -t qgramm:tail .
python3 tools/benchmark.py --image qgramm:tail --out work/sql.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s --idle-subscriptions
```

Use separate immutable before/after checkouts and one frozen generator. Repeat each standard/subscribed variant twice, with 60s cooldown. For the optional worker add `--checkpoint-interval-ms 100 --checkpoint-bytes 1048576`. For private diagnostics compile the minimal server with `-tags=qg_bench_profile`, then add `--profile-dir work/private-timeline --timeline-only`. Summarize using:

```sh
python3 tools/performance/timeline.py --result work/diagnostic.json \
  --timeline work/private-timeline/timeline.jsonl --out work/summary.json
```

## Verification

Minimal/full 13-feature `go test -race`, profile-tag race checks, `go vet ./...`, the Ed25519/WS/HPKE/receipt example and 28-build/7-invalid matrix passed. Independent review and readiness inspection passed before the source commit. Regressions cover empty/ordered/bounded/join-filtered history, current ACL/device/key rotation, tombstone/hook-once and cross-chat denial, independently encapsulated HPKE envelopes, FULL/automatic checkpoint/zero busy timeout after reconnect, pinned readers, active writers, worker shutdown and disabled-resource absence.
