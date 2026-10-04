# Configuration

`qgramm.toml` is strict: unknown keys, invalid combinations and embedded secret values are rejected. `.env` supplies only environment values at runtime; the service does not automatically parse .env locally. Docker Compose receives it using `--env-file`.

| Section | Fields and meaning |
|---|---|
| `server` | `listen`, TLS certificate/key paths, `allow_insecure_loopback`, `trusted_proxy`, allowed browser `origins` |
| `storage` | SQLite `path`, encrypted chunk directory `files`, optional `checkpoint_interval_ms` and `wal_checkpoint_bytes`; container paths should be beneath `/data` |
| `security` | JWT `issuer`/`audience`; references `token_public_key_env`, `management_secret_env`, `master_key_env`, `hpke_key_env`; bounded retired-key reference lists `previous_master_key_envs`, `previous_hpke_key_envs` |
| `features` | Boolean `groups`, `files`, `e2ee`, `calls`, `delete`, `edit`, `reply`, `forward`, `reactions`, `openai`, `anthropic`, `mcp`, `http_tools` |
| `capacity` | `expected_concurrent_users`; optional explicit `max_connections`, `queue_depth`, `workers` |
| `policy` | `history` since_join/all; `delete_mode` global/author_only; reaction type dictionary; event/dedup/upload retention; message/batch/file/chunk/storage limits |
| `ai` | `openai_url`, `anthropic_url`, `openai_key_env`, `anthropic_key_env`, `model`, `max_steps`, `max_context_turns`, `max_context_bytes`, `timeout_seconds`, `max_response_bytes`, `tools` |
| `calls` | TURN URLs, shared-secret env reference, credential TTL |

The authoritative field definitions/defaults are in `internal/config/config.go`; examples are in `configs/`. Secret references must be environment variable identifiers, never credentials. Calls require external TURN settings. Tools require an enabled AI provider, and each configured connector must have its feature compiled.

Each `[[ai.tools]]` defines `name`, `kind`, `url`, `methods`, `secret_env`, `allow_private`, `schema`, `timeout_seconds`, `max_response_bytes`. Zero tool limits inherit globals; positive values narrow them. See [AI bounds and schema vocabulary](ai.md). AI defaults: 20 turns, 262144 context bytes, 45 seconds and 1048576 response bytes. These are also absolute safety ceilings.

## Optional WAL checkpoint

`storage.checkpoint_interval_ms` defaults to **0**: no additional pool or worker is created. Values **100..60000** enable a periodic `PASSIVE` checkpoint on one separate writable connection with a zero busy timeout. This is an opt-in tuning experiment; it retains SQLite automatic checkpoint and `synchronous=FULL` on every writer connection. A restart applies the setting; no rebuild is needed.

`storage.wal_checkpoint_bytes` is the WAL file-size trigger, **65536..1073741824** bytes; zero or omission selects **4194304** (4 MiB). A nonzero threshold requires an enabled interval. An unchanged WAL is skipped after a complete checkpoint; partial/busy/failed attempts retry on a later tick. `PASSIVE` does not truncate the allocated WAL. The threshold is neither a disk quota nor a hard WAL bound: long-lived read transactions can prevent checkpoint progress. Database `:memory:` cannot enable this worker.

Example tested candidate, to benchmark against the default on your storage:

```toml
[storage]
path = "/data/qgramm.db"
files = "/data/files"
checkpoint_interval_ms = 100
wal_checkpoint_bytes = 1048576
```

Checkpoint I/O can contend with commits even on a separate connection; this setting does not guarantee lower latency. See [SQLite WAL checkpoint behavior](https://www.sqlite.org/wal.html).

**RU:** по умолчанию `checkpoint_interval_ms=0`, дополнительный пул и worker отсутствуют. Интервал 100–60000 мс включает фоновый `PASSIVE` checkpoint, сохраняя `FULL` и автоматический checkpoint SQLite. `wal_checkpoint_bytes` задает порог размера WAL (64 КиБ–1 ГиБ; 0/отсутствие — 4 МиБ), а не ограничение дискового пространства. Удерживаемые читателями транзакции могут задерживать обработку WAL. Настройку следует сравнить с режимом по умолчанию на своем хранилище; достаточно перезапуска.

## Build selection

`qgramm-build build` derives `qg_*` tags from enabled features and injects the binary manifest. Direct untagged `go build ./cmd/qgramm` creates a minimal binary. Direct tagged builds without the manifest intentionally fail configuration verification; use the builder.

Changing build features requires rebuilding. A TOML with missing/extra features is rejected at startup, even if the extra module is never used. Ordinary configuration changes need a restart. Disabled modules have no routes, tables or workers in a new deployment. Tables from a previously fuller deployment are not destructively deleted when features change.

## Sizing

`expected_concurrent_users` means concurrent connected people, initially assuming one socket per person. Automatic limits allow 20% connection headroom and bound queues/workers by local resources. Active traffic, multiple devices, group fanout, files, AI latency and retention alter capacity substantially.

`qgramm-build plan` prints chosen settings and assumptions; `compose` explicitly writes a local deployment file with estimated CPU/RAM limits. These estimates are heuristic until calibration and do not reserve hardware. Runtime uses CPU/cgroup limits; operators can set explicit limits after measurements. The first benchmark target is 10,000 connections, separate from sustained message rate.

## Backup/restore

Stop the container. Run `qgramm -config ... -backup /data/backup.db` using the same build/config/environment; the command must run against a stopped instance and writes a consistent SQLite backup. Back up the file directory and all relevant keys separately with the service stopped. Restore database, files and matching keys together before starting **one** instance. Do not restore old AI MLS state and resume sending without a fresh cryptographic identity/epoch: counter rollback can invalidate security.

The initial schema is version 1, tracked by `schema_versions`. Future schema upgrades must preserve transaction boundaries and use explicit versioned migrations; old messenger data is not imported.
