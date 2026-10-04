# Embedded Redis experiment

**Archived experiment: removed from the current core.** The files, feature flag, dependencies and commands below describe the previous revision only. See [recorded comparison](performance-iteration.md); do not use these deployment/test commands on the current checkout.

`docker build -f Dockerfile.redis -t qgramm:redis .` builds a separate optional
profile. `configs/redis.toml` enables `features.redis`; the build tool selects
`qg_redis` and includes the Go Redis client. The default Dockerfile and minimal
profile contain neither Redis server nor its compiled Go dependencies. Generated
Compose chooses `Dockerfile.redis` when this feature is enabled.

The core starts Redis 7.2.14 as a child process under the same unprivileged UID
and container CPU/RAM limits. The source archive SHA-256 is verified against the
[official hashes](https://github.com/redis/redis-hashes/blob/master/README).
Redis 7.2.14 uses the [BSD 3-Clause license](https://github.com/redis/redis/blob/7.2.14/COPYING);
server and bundled library notices are shipped in `/app/redis-licenses`.

Redis listens only on a private Unix socket (directory mode 0700, socket 0600).
TCP, snapshots and AOF are disabled. `redis.max_memory_mb` defaults to 64 and
`redis.queue_depth` to 256; configure these in TOML, with no new secrets needed.
Memory limits cover Redis data, not its entire RSS or Pub/Sub buffers. Redis
contains only chat IDs in transient Pub/Sub notifications, never message content,
keys, identities, ACLs, deduplication or receipt state.

Healthy wakeups travel through Redis Pub/Sub before the local fan-out. Publishing
runs in a bounded queue with a 10 ms timeout and no retries. A full queue, unavailable
subscriber, or failed publish immediately falls back to local notification. A
separate SQLite head poll still runs every second, including during Redis failures.
Acceptance still follows the SQLite FULL-synchronous transaction. Redis does not
change the durable delivery contract and does not remove SQLite writes/fsync.

Missing Redis or failed initial subscription blocks startup of this explicitly
enabled profile. If Redis fails after startup, QGramm degrades to local wakeups
and SQLite replay; restart the container to restore Redis. Readiness continues
to report the working SQLite-backed service. Graceful shutdown closes client
connections, interrupts Redis, kills it after one second if necessary, reaps the
child and removes its private directory. Redis is an experimental cost/latency
comparison, not a prerequisite or a claim of improved performance.

`GET /v1/capabilities` reports optional `redis.status`: `ready` or
`degraded_local_fallback`, plus `durable=false`. A missing field means the module
is absent. Compose adds a heuristic Redis budget: configured maxmemory + 32 MiB
for Pub/Sub/buffers + 16 MiB process baseline; this is not an RSS guarantee.

Real-process tests require Redis, without silently skipping other module tests:

```sh
sh scripts/test-redis.sh sh scripts/build-matrix.sh
```

The helper uses an existing executable or downloads the pinned, checksum-verified
source and builds it in ignored project `work/` (requires curl, make and a C
compiler), then removes the temporary build. It never installs a host service.
Alternatively set `QGRAMM_REDIS_TEST_BINARY` to a real local executable. This is
only a test-fixture setting; production uses TOML `redis.binary`.

The Redis child receives an empty environment; storage/provider secrets are not inherited. It still shares the container UID and trust boundary with Go, so this is not a process security sandbox.
