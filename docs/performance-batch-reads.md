# Full batch projection and preliminary read experiments

[Русский](performance-batch-reads.ru.md).

## Changes

Baseline is exact dev `20c3932`, including bounded shared FULL HTTP batch commits, without reverted idle-buffer reuse. This series uses full HTTP responses throughout. Minimal ACK is not another variable; main `2917865` is not the control.

1. Full response projection (`cf2f1ed`) groups up to 16 successful distinct message IDs into the existing ACL-aware query and parses the recipient key once per group. Results return in original input order. If Project hooks exist, IDs repeat or group projection fails, the old individual path preserves per-item errors, hook effects and independent HPKE envelopes. Single Send and minimal ACK are unchanged.
2. Preliminary reads use a bounded request-local snapshot of operation hash/results and one chat/device ACL/mode/epoch/pending check per window of at most16 items and queue capacity. A flush invalidates the snapshot; same-ID jobs flush before retry lookup. Prepare or InTransaction hooks disable this path and retain their original prepare/commit ordering. The writer transaction still checks fresh ACL, device/user access, epoch/pending, operation hash and conflict. The snapshot never grants permission to commit and is not cached across requests.

Tests establish 19 distinct full outputs → 2 read queries, and 34 new minimal-ACK inputs → 6 preliminary reads. These counts are targeted tests, not measured end-to-end CPU savings. FULL, automatic checkpoint, cryptographic bindings and per-item 207 semantics remain; PASSIVE checkpoint and Redis are absent from the benchmark runtime.

## Measurement method

Immutable exact baseline and isolated source overlays are built before the individual campaign. Same frozen prior generator and runner, 60s cooldown, sequential order: control1, projection1, prepare1, prepare2, projection2, control2. Two repeats per cohort are arithmetic means of per-run p95/p99/phase CPU means and sampled RAM maxima, not pooled percentiles or confidence intervals. Combination is tested only after both individual candidates justify it.

Every run: 10,000 distinct-user WebSockets ; 10s idle ; 100 offered messages/s for 30s plus 1,000/s for 5s; subscribed profile with 5,000 paired chats/10,000 subscriptions and one active chat; ready batches of 10, full responses, 17-byte HPKE plaintext. Rates count messages (10/100 HTTP requests/s). Delivery starts before HPKE preparation of already-ready batches; waiting to accumulate application batches is excluded. No new saturation/distributed-chat workload is added. Successful-request percentiles exclude rejected requests; rejection counts remain visible.

Apple M5 Pro/macOS 26.6.2, host generator, Docker Desktop Linux arm64 VM/4 CPU / 8 GiB quota, SQLite named volume; shared host, no TLS. Dedicated Linux/SSD and crypto audit are not qualified. CPU 100%=one core; RAM is sampled maximum. Docker stats roughly every 2s; phase boundaries may mix samples. No builds, race tests or profilers overlap primary measurements. Equal offered/accepted/event/history counts are checked but do not establish exact-ID/decryption/persistent-device-ACK acceptance; targeted tests cover those contracts separately.

## Reproduction

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
python3 tools/benchmark.py --image qgramm:isolated --out work/run.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s \
  --idle-subscriptions --batch-size 10 --response-mode full
```

Use immutable images of each isolated patch, one generator/runner and surrounding controls. Keep source/image hashes and exact run order. No profiler tags in primary images.

## Results

6 primary runs; 47,890 offered = accepted = events = history; zero errors, backpressure, generator skips or unexpected disconnects. Each row is the mean of two runs, compared with matching full-response controls on dev `20c3932`. Negative changes are lower. Ready batches compare with ready batches, single messages with single messages.

| Variant | Steady delivery p95, ms | p99, ms | Δ p99 | Steady CPU, % | Δ CPU | Δ RAM | Whole-run Δ p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Control / full | 15.159 | 17.988 | — | 11.71 | — | — | — |
| Grouped full response | 15.201 | 18.669 | +3.8% | 12.04 | +2.8% | -0.7% | +5.2% |
| Preliminary reads | 15.130 | 18.400 | +2.3% | 11.43 | -2.3% | -2.1% | +1.5% |

## Per-run evidence

| Raw run | Accepted | Steady p95 / p99, ms | Whole p99, ms | Steady CPU, % | RAM max, MiB |
|---|---:|---:|---:|---:|---:|
| [batchreads-before-subscribed-1.json](benchmarks/batchreads-before-subscribed-1.json) | 7980 | 15.002 / 17.069 | 16.162 | 11.74 | 345.5 |
| [batchreads-before-subscribed-2.json](benchmarks/batchreads-before-subscribed-2.json) | 7980 | 15.316 / 18.907 | 16.797 | 11.67 | 337.4 |
| [batchreads-prepare-subscribed-1.json](benchmarks/batchreads-prepare-subscribed-1.json) | 7980 | 14.652 / 18.211 | 16.462 | 11.06 | 334.5 |
| [batchreads-prepare-subscribed-2.json](benchmarks/batchreads-prepare-subscribed-2.json) | 7990 | 15.609 / 18.589 | 17.007 | 11.80 | 334.2 |
| [batchreads-projection-subscribed-1.json](benchmarks/batchreads-projection-subscribed-1.json) | 7980 | 14.783 / 19.489 | 18.020 | 12.13 | 336.5 |
| [batchreads-projection-subscribed-2.json](benchmarks/batchreads-projection-subscribed-2.json) | 7980 | 15.620 / 17.850 | 16.660 | 11.94 | 341.3 |

[Summary](benchmarks/batchreads-summary.json) · [Image/source provenance](benchmarks/batchreads-provenance.json).

## Selection

Neither candidate demonstrated a convincing speed gain in this load. Projection mean steady delivery p99 rises 3.8%, whole-run p99 rises 5.2%, CPU rises 2.8%, while RAM falls 0.7%. Preliminary reads lower CPU 2.3% and RAM 2.1%, but steady delivery p99 rises 2.3%, whole-run p99 rises 1.5%, and ACK p95 rises 4.0%. Control steady p99 ranges 17.069–18.907ms; these small deltas are not proven causal regressions or statistically significant differences. Lower targeted query counts alone do not justify retaining complexity.

The projection experiment `cf2f1ed` is reverted; preliminary-read code was isolated and is not applied to the retained core. Both source variants remain reproducible: projection from that commit, preliminary reads via [archived patch](benchmarks/batchreads-prepare.patch) applied to exact `20c3932`. [Provenance](benchmarks/batchreads-provenance.json) and [summary](benchmarks/batchreads-summary.json) record the snapshots and patch hash. The retained core matches `20c3932`; main remains `2917865`.

No combined primary run was performed: the user condition that both isolated candidates be good was not satisfied. This does not prove that combining them could never help. More active load is deferred as requested; no saturation benchmark or weakening of FULL, ACL, retries or encryption was added.

## Verification

Minimal and full 13-feature `go test -race ./...`, `go vet ./...` and the 28-selection/7-invalid build matrix passed for projection. Isolated preliminary reads passed narrow default/profile race, full core race, full 13-feature suite, and a full core rerun after the hook-boundary fix. Tests cover bounded reads, ordered/decrypted full responses, independent envelopes on duplicate IDs, partial failures, hooks/revocation, concurrent operation insertion/conflicts, pending/epoch changes, corrupt saved results, SQL failure and exact prepare-before-flush hook ordering. Independent review approved both before the primary campaign. Final core is restored rather than accepting either experiment.
