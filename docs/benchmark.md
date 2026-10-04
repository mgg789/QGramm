# Load measurements

[Latest first-three/embedded-Redis campaign](performance-iteration.md): 12 runs, two repeats of standard and fully subscribed profiles per variant. Earlier results below retain their original image/provenance and workload scopes.

[Measured alternative baselines](comparison-benchmark.md): NATS JetStream default/always fsync and Centrifugo memory history, with the same container quotas, connection count and nominal load. Their application semantics differ; the comparison records those differences.

## Current p95 / p99 run

[Machine-readable result](benchmarks/minimal-10000-p99.json), 2026-10-04, exit 0:

| Phase | Accepted | Acceptance p95 / p99, ms | Recipient delivery p95 / p99, ms |
|---|---:|---:|---:|
| 100/s for 30 seconds | 3,000 | 4.364 / 6.657 | 4.676 / 6.898 |
| Steady + 1,000/s for 5 seconds | 7,988 | 5.354 / 19.578 | 9.368 / 87.170 |

10,000 distinct users/device sockets; one active two-member basic chat. Offered = accepted = delivered = history = 7,988, with no unexpected failures, backpressure, semaphore skips or observed connection drops. Idle connections are monitored as well as the recipient. The nominal schedule is 8,000; host ticker scheduling produced 7,988 submissions. Sampled Docker maxima across setup/load: 666.3 MiB, 61.05% of one CPU; these are not exact peaks. Percentiles cover successful requests, not rejected operations.

Hardware: Apple M5 Pro, 18 host logical CPUs, macOS 26.6.2; Linux arm64 Docker Desktop VM, kernel 6.12.76-linuxkit. Server container quota: 4 CPUs / 8 GiB; Docker reports approximately 7.748 GiB effective memory. SQLite uses a named Docker volume, WAL and FULL synchronous commits. SSD/fsync performance was not independently qualified. The host also runs other workloads; limits are quotas, not dedicated reservations. The generator runs on macOS, Go 1.26.4. Image ID, generator dirty-tree digest and existing server-image provenance are recorded separately in JSON: the reused service image matches the earlier final run; this is not a fresh build of the current documentation commit.

### Method and reproduction

```sh
python3 tools/benchmark.py --out work/minimal-10000-p99.json
# Or reuse an existing matching minimal image, retaining it afterwards:
python3 tools/benchmark.py --image qgramm:dev-final --image-source-commit 3147dc9 \
  --out work/minimal-10000-p99.json
```

The first command builds the current service; the second was used for the published run after a Docker build encountered Go proxy download EOFs. An existing image must match the selected features. The runner generates private temporary credentials, provisions users/devices, opens all sockets before traffic, and samples container resources approximately every two seconds. It creates an owned loopback-only container and volume and removes them afterwards. It does not change host settings or restart other services. Failed runs produce diagnostic evidence and a nonzero exit status.

Acceptance timing starts before client HPKE sealing and includes JWT generation, HTTP request, the SQLite commit and response. Delivery timing uses the same start and ends when the generator reads the recipient WebSocket event; it does not include recipient decryption/rendering. Both measurements use one generator clock, so cross-host clock synchronization is not needed for latency. The synthetic device JWT is issued five seconds in the past with a fourteen-minute future expiry to tolerate small host/VM clock differences without changing server validation. Setup/authentication attempts that failed during harness development are not included in this passing run.

Percentiles use nearest rank: sort `n` samples and select `ceil(n*q)-1` for `q=0.95` and `0.99`. Steady deliveries are classified by their originating send phase, including arrivals after the phase boundary. Counts of acceptance/delivery samples are published. History is paginated and compared with accepted count; delivery count must equal accepted × recipient count. A failed integrity check fails the command. The generator's bounded semaphore skips and HTTP 429/503 responses are separate from successful latency samples.

TLS cost, heterogeneous device keys, real message distributions, many simultaneously active chats, long soak and concurrent large-file loads are outside this run. Earlier artifacts retain their original p95 estimator `floor((n-1)*0.95)` and do not contain p99; no p99 was reconstructed from them. A dedicated Linux/SSD reference run is deferred until that host is available.

## Earlier runs

Final minimal-image rerun ([raw evidence](benchmarks/minimal-10000-final.json), image/source recorded): 10,000 sockets, 100/s for30 seconds plus1000/s for5 seconds; 7,987 accepted = delivered = stored, no errors/backpressure/skipped submissions. Steady p95 acceptance/delivery: 4.206/4.388 ms; aggregate4.570/7.181 ms. Twenty-eight Docker stats samples across setup/load reached657 MiB and94.31% of one CPU (out of four allowed); these are sampled maxima, not exact process peaks. Backup restore retained all7,987 messages, SQLite integrity_check=ok and readiness passed. The earlier run below retains evidence of explicit burst backpressure; variation between runs is not a sizing guarantee.

[Raw minimal-profile evidence](benchmarks/minimal-10000.json), 2026-10-04:

| Workload | Result |
|---|---|
| Distinct users / WebSocket connections | 10,000 / 10,000 |
| Steady offered rate / duration | 100 messages/s / 20 s |
| Steady accepted / backpressure | 1,999 / 0 |
| Steady p95 acceptance / delivery | 5.422 / 5.596 ms |
| Burst offered rate / duration | 1,000 messages/s / 5 s |
| Total accepted / explicit 503 backpressure | 6,813 / 171 |
| Accepted messages in recipient events / stored history | 6,813 / 6,813 |
| Unexpected errors / generator skips | 0 / 0 |
| Aggregate p95 acceptance / delivery | 11.356 / 71.844 ms |

Server: Linux arm64 Docker Desktop, Go 1.26.8, four CPU limit, eight GiB memory limit, persistent Docker volume. One sampled resource reading was 513.3 MiB and 15.76% of one CPU; this is **not a peak measurement**. Generator runs on the host. All users have registered devices and short JWT/ticket authentication, but only one direct chat is active. Synthetic HPKE text, shared synthetic recipient key, isolated HTTP with a trusted-proxy header: TLS cost and heterogeneous clients are not measured. Image digest/source snapshot are in the raw evidence; the run precedes retired-key support.

This is a short smoke/load acceptance, not a long soak, separately qualified local-SSD reference environment, full-feature capacity guarantee or cryptographic audit. It validates no loss of accepted events in that run and explicit backpressure during the burst. Group fan-out, large files, AI latency and multiple devices change resource needs. Resource recommendations remain conservative heuristics; this single workload does not justify promising 2,000 active users per CPU.

The server did run on Linux: Docker Desktop supplies a Linux VM on the macOS host. Four CPUs/eight GiB are container limits, not dedicated hardware reservations. SQLite lives in a Docker volume backed by the VM's virtual disk; SSD latency/IOPS and fsync behavior were not characterized independently. A reproducible reference run should record the Linux kernel, CPU model/budget, SSD/filesystem, effective memory, container image and co-running workloads; run sustained traffic longer, measure replay and sample resources. The remaining work validates hardware sizing; Linux runtime support was already exercised.

[Group evidence](benchmarks/group-100.json): 1,000 sockets, one 100-member group, 10/s for 10 seconds then 100/s for 3 seconds. All 398 accepted messages produced 39,402 recipient deliveries (99 each), no unexpected failures or backpressure. Steady p95 acceptance/delivery: 13.478/21.328 ms; aggregate: 43.129/188.992 ms. Same Docker CPU/RAM limits; no peak resource sample. This measures group fan-out separately from the 10,000 idle-socket workload.

File microbenchmark, macOS arm64 Apple M5 Pro, Go1.26.4: `go test -tags qg_files ./internal/modules -run '^$' -bench BenchmarkEncryptedFileUpload64KiB -benchtime 100x -benchmem` passed: 8,404,570 ns/op, 7.80 MB/s, 567,063 B/op, 753 allocations/op. Includes HPKE file-key admission, AEAD chunk/fsync and checksum completion; SQLite fixture is in-memory and local files are real. This is neither Linux HTTP upload throughput nor a maximum-size/file concurrency test.

Reproduce on an isolated disposable instance (never production):

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
work/qgramm-bench -init -env work/load.env
# Supply that private 0600 environment file to the test container only.
# Configure expected_concurrent_users=10000, max_connections=12000.
work/qgramm-bench -env work/load.env -url http://127.0.0.1:8080 \
  -users 10000 -duration 20s -rate 100 -burst 5s -burst-rate 1000 \
  -out work/minimal-10000.json
```

The generator exits nonzero for partial socket setup, unexpected send failures or mismatch between acceptance, events and history. It reports HTTP backpressure separately and does not silently count rejected messages as accepted. `-group-size` above two selects the management groups route and subscribes all recipients; the server must have the groups module compiled. Environment files contain test signing/storage/management secrets and must never be committed.
