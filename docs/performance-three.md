# Independent compact response, HTTP batch and idle replay experiments

[Русский](performance-three.ru.md).

## Changes and isolation

Baseline is the exact `2ce7077` development archive (the prior replay/WAL candidate, not main). `0d42533` extends only the benchmark generator/runner. `ab09b58` reuses replay poll buffers; `30eafab` adds bounded true HTTP batch commits and preserves direct embedding caller-context behavior. Runtime profiles use minimal modules, checkpoint disabled, SQLite WAL FULL and automatic checkpoint unchanged.

1. Compact HTTP acknowledgement uses the **existing optional** `Prefer: return=minimal` header. No server patch or default-response change is involved. Successful sends return durable receipts instead of reading/wrapping the message again for the HTTP sender. WebSocket delivery remains encrypted and ordered.
2. Prepared HTTP batch jobs enter one bounded queue carrier, with real per-message capacity accounting and up to 16 messages / 8 MiB/queue capacity per group. Savepoints isolate item failures, shared commit failure rejects tentative successes, and ACK/Wake follow commit. Repeated operation IDs flush the preceding group before validation. Full responses still project each element separately, isolating the commit change from projection optimization. Oversized single messages retain their configured limit and run alone.
3. Idle replay increments a generation only when the subscribed **chat set** changes. Stable polls reuse the chat list, SQL/args and one scratch map of at most 500 heads. Scan destinations are outside the row loop. Churn refreshes the snapshot, last-unsubscribe releases it on the next poll. No ACL/key caching or reduction in the one-second fallback polling interval occurs; lost-Wake recovery and revocation stay in place.

Images isolate patches: before/compact use exact 2ce; idle uses exact ab09; batch uses 2ce with only messages.go/writer_queue.go/batch tests from 30eaf. The idle patch was reverted in `6d6610c` after individual tail results. Selected combined runs use the same batch-only overlay image with minimal ACK, without idle reuse. The unused all-source30eaf image is not included in primary evidence. Source/overlay/image/binary/generator hashes are recorded.

## Method

Same 10,000 WebSockets,10s idle,100 offered messages/s for30s +1,000/s for5s,17-byte HPKE plaintext and no TLS. Standard has one subscribed recipient. Subscribed has5,000 paired chats/10,000 subscribed peers and one active conversation. No sustained saturation or distributed active-chat workload is added.

Single-message controls/compact are repeated twice for standard and subscribed; idle is repeated twice for subscribed. The true-batch experiment compares identical **ready batches of10** against the old serialized batch endpoint, twice each subscribed. Batch rate counts messages, not requests (10/100 HTTP requests per second in steady/burst). Latency starts before HPKE preparation of the ready batch; forming/waiting to accumulate a real application batch is excluded. All successful elements share HTTP-ACK duration samples, while each WebSocket event has its own delivery timestamp. Batch and single-message latency distributions must not be treated as identical workloads.

The 14 individual runs are sequential with 60 s cooldown; standard controls surround compact runs, subscribed controls surround idle/compact, and batch controls surround batch changes. Generator and runner are frozen once; no builds/source tests overlap the included primary measurements. Four combined runs happen after selection, with the same frozen tooling: single, batch, batch, single; subscribed profile and minimal ACK in all four. CPU 100% means one core; RAM is sampled maximum. Docker stats cadence includes command time plus1s wait, typically about2s. Idle CPU excluding the first phase sample reduces boundary contamination but remains approximate and has few samples. Means of per-run p95/p99 are not pooled percentiles; two repeats are not confidence intervals.

Apple M5 Pro/macOS 26.6.2 Docker Desktop Linux arm64 VM,4 CPU / 8 GiB container quotas, SQLite named volume; generator on the shared host. This is not dedicated Linux/SSD qualification. Counts of offered/accepted/events/history are checked; they are not exact-ID, decryption or persistent-device-receipt acceptance.

## Native poll microbenchmark

Same private fixture with5,000 unchanged subscribed heads, 5 warmups,100 polls ×3 per version, natural GC, no HTTP/WS in the measured loop. Go 1.26.4 darwin/arm64 differs from production Docker Go 1.26.8. The isolated Core has no competing replay worker. Heads and absence of notifications are verified. Timing ranges overlap; the supported local conclusion is lower allocation cost, not an API-speed claim.

| Per poll | Before | Idle variant |
|---|---:|---:|
| Mean time | 5.574ms | 5.406ms |
| Allocated bytes | 1,281,829 | 539,987 |
| Allocations | 35,217.6 | 20,156.5 |

Allocated bytes drop 57.9%, allocations 42.8%; mean time 3.0% with overlapping ranges.

## Reproduction

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
python3 tools/benchmark.py --image qgramm:isolated --out work/run.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s --idle-subscriptions
```

Add `--response-mode minimal` for compact acknowledgements, or `--batch-size 10` for ready batches. Use distinct immutable images/source copies for each patch and keep one generator/runner across all variants. Preserve two repeats, surrounding controls and60s cooldown; do not enable profiling in primary comparisons.

## Verification

`go test -race ./...`, full 13-feature race, profile-tag core/CLI race, `go vet ./...`, `sh scripts/build-matrix.sh` (28 selections/7 invalid configurations), `go run -race ./examples/basic`, generator batch validation, integration smoke and documentation checks passed after the direct embedding fix. Independent code reviews/readiness inspections passed before each source commit. An initial full-profile failure in canceled-background AI embedding fixtures led to restoring caller-owned transaction context only for no-writer fallback; the unchanged AI tests then passed 5 race repetitions and full profile. Tests cover shared-commit failure/no early ACK or Wake, partial hook rollback, duplicate/conflict/malformed retry, low-capacity splitting, real queue rejection, cancellation/shutdown, poll generation/churn/buffer release, revocation and lost notifications.

## Results

18 primary runs; 143,775 offered = accepted = events = history; zero errors, backpressure, generator skips or unexpected disconnects. Each row is the mean of two runs, compared with matching full-response controls on dev `2ce7077`. Negative changes are lower. Ready batches compare with ready batches, single messages with single messages.

| Variant | Steady delivery p95, ms | p99, ms | Δ p99 | Steady CPU, % | Δ CPU | Δ RAM | Whole-run Δ p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| ACK / standard | 5.137 | 12.834 | +76.3% | 12.45 | -3.3% | +0.7% | +8.4% |
| ACK / subscribed | 4.967 | 7.037 | -9.2% | 15.49 | -7.5% | +3.6% | -1.5% |
| Idle / subscribed | 4.941 | 6.860 | -11.5% | 16.41 | -2.0% | +5.0% | +38.8% |
| Batch / subscribed | 15.655 | 17.991 | -23.0% | 11.24 | -12.9% | +1.4% | -24.3% |
| Combined single | 4.855 | 7.932 | +2.4% | 15.30 | -8.6% | +3.2% | +2.2% |
| Combined batch | 15.079 | 17.017 | -27.2% | 11.19 | -13.3% | +3.4% | -27.7% |

## Per-run evidence

| Raw run | Accepted | Steady p95 / p99, ms | Whole p99, ms | Steady CPU, % | RAM max, MiB |
|---|---:|---:|---:|---:|---:|
| [next3-batch-before-subscribed-1.json](benchmarks/next3-batch-before-subscribed-1.json) | 7990 | 19.402 / 22.990 | 23.143 | 12.66 | 327.2 |
| [next3-batch-before-subscribed-2.json](benchmarks/next3-batch-before-subscribed-2.json) | 7980 | 19.694 / 23.747 | 21.167 | 13.14 | 339.0 |
| [next3-batch-subscribed-1.json](benchmarks/next3-batch-subscribed-1.json) | 7980 | 15.762 / 17.326 | 16.268 | 10.84 | 344.9 |
| [next3-batch-subscribed-2.json](benchmarks/next3-batch-subscribed-2.json) | 7980 | 15.547 / 18.656 | 17.296 | 11.64 | 330.4 |
| [next3-before-standard-1.json](benchmarks/next3-before-standard-1.json) | 7996 | 4.770 / 7.109 | 6.641 | 13.05 | 269.5 |
| [next3-before-standard-2.json](benchmarks/next3-before-standard-2.json) | 7993 | 4.880 / 7.446 | 6.671 | 12.71 | 279.7 |
| [next3-before-subscribed-1.json](benchmarks/next3-before-subscribed-1.json) | 7994 | 4.876 / 7.291 | 6.433 | 15.94 | 330.6 |
| [next3-before-subscribed-2.json](benchmarks/next3-before-subscribed-2.json) | 7987 | 5.030 / 8.203 | 7.458 | 17.55 | 322.4 |
| [next3-combined-batch-subscribed-1.json](benchmarks/next3-combined-batch-subscribed-1.json) | 7980 | 15.442 / 17.113 | 16.310 | 11.31 | 340.7 |
| [next3-combined-batch-subscribed-2.json](benchmarks/next3-combined-batch-subscribed-2.json) | 7980 | 14.715 / 16.921 | 15.710 | 11.07 | 348.2 |
| [next3-combined-single-subscribed-1.json](benchmarks/next3-combined-single-subscribed-1.json) | 7989 | 5.142 / 8.501 | 7.633 | 15.62 | 351.3 |
| [next3-combined-single-subscribed-2.json](benchmarks/next3-combined-single-subscribed-2.json) | 7991 | 4.568 / 7.364 | 6.560 | 14.98 | 322.9 |
| [next3-idle-subscribed-1.json](benchmarks/next3-idle-subscribed-1.json) | 7984 | 4.954 / 6.747 | 7.404 | 15.32 | 349.2 |
| [next3-idle-subscribed-2.json](benchmarks/next3-idle-subscribed-2.json) | 7992 | 4.927 / 6.972 | 11.877 | 17.50 | 336.4 |
| [next3-minimal-standard-1.json](benchmarks/next3-minimal-standard-1.json) | 7990 | 5.505 / 18.085 | 7.730 | 13.17 | 276.5 |
| [next3-minimal-standard-2.json](benchmarks/next3-minimal-standard-2.json) | 7992 | 4.770 / 7.582 | 6.695 | 11.73 | 276.8 |
| [next3-minimal-subscribed-1.json](benchmarks/next3-minimal-subscribed-1.json) | 7984 | 4.958 / 6.903 | 6.735 | 14.98 | 344.9 |
| [next3-minimal-subscribed-2.json](benchmarks/next3-minimal-subscribed-2.json) | 7993 | 4.976 / 7.170 | 6.945 | 16.01 | 331.4 |

[Summary](benchmarks/next3-summary.json) · [Image/source provenance](benchmarks/next3-provenance.json) · [Native poll microbenchmark](benchmarks/next3-poll-micro.json).

## Selection and retained behavior

Retain bounded HTTP batch commit. Existing minimal ACK remains opt-in: subscribed individual results improved, standard results did not. Combining batch commit with minimal ACK improves ready-batch steady p95/p99 by 22.9% / 27.2%, ACK p99 by 42.2% and CPU by 13.3%, with sampled RAM 3.4% higher than the old serialized full-response batch. Compared with isolated new batch/full, minimal ACK lowers delivery p99 another 5.4%, ACK p99 by 17.4%; CPU differs only 0.5%, insufficient to claim an additional CPU gain.

Single-message combination lowers CPU 8.6% but delivery p99 rises 2.4% and RAM 3.2%; no universal single-message delivery improvement is claimed. Idle reuse lowers idle CPU 22.2% and native allocations, but whole-run p99 rises 38.8% and RAM 5.0%; it was reverted in `6d6610c`. FULL durability, ACL checks and encrypted WS delivery remain. Default responses stay full.

These are local results against dev `2ce7077`, not proof of superiority over main or alternative servers. Main remains `2917865`; the candidate stays on dev pending qualification against main and dedicated Linux/SSD. Next useful checks are paired runs with interleaved controls to assess single-message variance and the batch-size/ACK tradeoff, then the separately authorized richer load. No new saturated workload was run.
