# Private performance instrumentation

The normal service has no profiling HTTP routes. `qg_bench_profile` includes
development-only signal instrumentation when `QGRAMM_BENCH_PROFILE_DIR` is set.
The feature manifest remains the minimal messaging profile; this tag does not
enable a product module. Never deploy an instrumented build for real messages.

```sh
docker build -f tools/performance/Dockerfile -t qgramm:profile .
python3 tools/benchmark.py --image qgramm:profile \
  --image-source-commit '<commit>+benchmark-instrumentation' \
  --profile-dir /absolute/private/synthetic-profiles \
  --idle 15s --out /absolute/private/profile-run.json
go tool pprof -top /absolute/private/synthetic-profiles/cpu.pprof
go tool pprof -top /absolute/private/synthetic-profiles/<timestamp>-heap.pprof
```

CPU profiling covers setup and load. `SIGUSR1` forces GC and records sampled
heap, goroutines, block/mutex profiles and exact runtime memory/SQL pool stats.
`SIGUSR2` flushes the CPU profile. The runner takes snapshots at observed phase
transitions. Snapshots and Docker stats have sampling/boundary uncertainty.
Forced GC and profile collection interrupt service: **profile-run percentiles
must not be presented as uninstrumented performance measurements**.

Use an empty private output directory. Container writes go to a temporary leaf
inside a private 0700 parent; the runner copies completed synthetic profiles to
the output directory and never changes its permissions to world-writable.

For measurements, use a normal image and omit `--profile-dir`. The default
response is unchanged (`--response-mode full`). `--response-mode minimal`
explicitly requests a compact durable receipt with `Prefer: return=minimal`;
report that mode separately from the full-response comparison.

```sh
python3 tools/benchmark.py --image qgramm:measurement \
  --image-source-commit '<commit>' --out /absolute/result.json
python3 tools/performance/summarize.py /absolute/result.json
```

Resource samples carry setup/idle/steady/burst/drain/history phase labels.
Means are arithmetic averages of periodic Docker samples, not continuous CPU
accounting; 100% CPU means one core. A phase change during a Docker sample may
include work from the preceding phase. Give the sample count, and do not rank
services by a single sampled maximum.

Idle CPU is especially sensitive to setup crossing the first sample. The helper
also reports the median and the mean after excluding the first sample of every
phase; these are additional estimates, never replacements for the raw evidence.
Keep at least 60 seconds between 10,000
connection runs so host socket state can recover. Retain failed setup evidence
separately; it has no latency result.
