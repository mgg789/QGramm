# Configuration

`qgramm.toml` is strict: unknown keys, invalid combinations and embedded secret values are rejected. `.env` supplies only environment values at runtime; the service does not automatically parse .env locally. Docker Compose receives it using `--env-file`.

## Quick start

Generate a new configuration, validate it offline, inspect its effective values, then build or restart as appropriate:

```sh
go run ./cmd/qgramm-build init -preset minimal -target local -users 100 -out app-qgramm.toml
go run ./cmd/qgramm-build validate -config app-qgramm.toml
go run ./cmd/qgramm-build explain -config app-qgramm.toml
go run ./cmd/qgramm-build build -config app-qgramm.toml -out bin/qgramm
```

`init` refuses to overwrite an existing path and writes mode `0600`. Container output uses `0.0.0.0:8080`, `/data/qgramm.db`, `/data/files` and `trusted_proxy = true`; terminate TLS at an HTTPS reverse proxy and keep the container port private. `compose` keeps the existing loopback-only host mapping (`127.0.0.1:8080:8080`).

The optional top-level `preset` is applied after defaults and before explicit TOML keys. Explicit `false`, `0` and empty lists therefore disable or replace preset values.

| Preset | Enabled features and values |
|---|---|
| `minimal` | none |
| `support` | `groups`, `files`, `delete`, `edit`, `reply`, `reactions` |
| `community` | support plus `forward` |
| `ai-openai` | `openai`; `ai.openai_key_env = "QGRAMM_OPENAI_KEY"` |
| `ai-anthropic` | `anthropic`; `ai.anthropic_key_env = "QGRAMM_ANTHROPIC_KEY"` |

AI presets require an explicit `ai.model`; the loader never guesses a provider or model. Calls, includes and environment interpolation are never implicit. Use `validate` for schema and semantic checks only: `secrets_presence` is always `not_checked`. `explain` reports effective configuration, field sources (`default`, `preset`, `explicit`, `derived`), feature tags, current resource inputs, derived capacity and secret reference names; it does not read secret values and does not establish service readiness.

For automatic capacity fields, `sources` reports the final value as `derived`; `declared_sources` distinguishes an explicit `0` from an omitted default. `declared_capacity` and `derived_capacity` show both values. CPU/RAM inputs come from the machine running the command; the service derives its own limits again on startup inside its container.

| Section | Fields and meaning |
|---|---|
| `server` | `listen`, TLS certificate/key paths, `allow_insecure_loopback`, `trusted_proxy`, allowed browser `origins` |
| `storage` | SQLite `path`, encrypted chunk directory `files`, optional `checkpoint_interval_ms` and `wal_checkpoint_bytes`; container paths should be beneath `/data` |
| `security` | JWT `issuer`/`audience`; references `token_public_key_env`, `management_secret_env`, `master_key_env`, `hpke_key_env`; bounded retired-key reference lists `previous_master_key_envs`, `previous_hpke_key_envs` |
| `features` | Boolean `groups`, `files`, `e2ee`, `calls`, `delete`, `edit`, `reply`, `forward`, `reactions`, `openai`, `anthropic`, `ai_streaming`, `ai_policy`, `mcp`, `http_tools` |
| `capacity` | `expected_concurrent_users`; optional explicit `max_connections`, `queue_depth`, `workers` |
| `policy` | `history` since_join/all; `delete_mode` global/author_only; reaction type dictionary; event/dedup/upload retention; message/batch/file/chunk/storage limits |
| `ai` | Legacy provider URLs/key references plus `model`, `max_steps`, `max_context_turns`, `max_context_bytes`, `timeout_seconds`, `max_response_bytes`, `max_output_tokens`, `tools`; named profiles use `endpoints`, `bots` and `default_bot` |
| `calls` | TURN URLs, shared-secret env reference, credential TTL |

The authoritative field definitions/defaults are in `internal/config/config.go`; examples are in `configs/`. Secret references must be environment variable identifiers, never credentials. Calls require external TURN settings. Tools require an enabled AI provider, and each configured connector must have its feature compiled.

Each `[[ai.tools]]` defines `name`, `kind`, `url`, `methods`, `secret_env`, `allow_private`, `schema`, `timeout_seconds`, `max_response_bytes`, `require_approval`, `cost_microunits`. Zero tool limits inherit globals; positive values narrow them. See [AI bounds and schema vocabulary](ai.md). AI defaults: 20 turns, 262144 context bytes, 45 seconds and 1048576 response bytes. These are also absolute safety ceilings.

### AI approvals and budgets

`[ai_policy]` configures `grant_public_key_env`, `issuer`, `audience`, `approval_ttl_seconds`, `require_provider_approval`, `provider_reserve_microunits`, `global_daily_budget_microunits`, `per_bot_daily_budget_microunits`, `per_user_daily_budget_microunits`, `currency`. `[ai]` and endpoint profiles configure input/output prices per million tokens. Tools select approval and fixed cost independently. See [all defaults/ranges and accounting limits](ai-policy.md) and [example](../configs/ai-policy.toml). A signed grant is required only where configured; every effect still receives a durable reservation and egress notice when the module is enabled.

### Named AI endpoints and bots

The stage-1 named participant configuration is resolved from global `[ai]`, then
`[ai.endpoints.NAME]`, then `[ai.bots.NAME]`. Set `default_bot` when named
endpoints are used without a global model. The endpoint selects `provider`
(`openai` or `anthropic`), URL, model, `key_env`, `auth`, `allow_private`,
optional streaming, capabilities and bounded overrides. A bot selects one
endpoint and may narrow tools and limits. `max_output_tokens` is bounded to
`1..65536`; `capabilities` currently accepts only `basic_text` and `tool`.
Multimodal input/output and other capability names are not part of this wire
contract.

```toml
[ai]
default_bot = "assistant"
max_output_tokens = 2048

[ai.endpoints.openai]
provider = "openai"
url = "https://api.openai.com/v1"
model = "operator-selected-model"
key_env = "QGRAMM_OPENAI_KEY"
auth = "bearer"
capabilities = ["basic_text", "tool"]

[ai.bots.assistant]
endpoint = "openai"
tools = []
```

For a local, private OpenAI-compatible service, `auth = "none"` is allowed
only with `allow_private = true` and no `key_env`. This deliberately trusts the
configured private endpoint; plaintext is sent to it. Secret values remain in
the process environment and are never written to TOML, profiles or logs.
Named agents are permanent user/device identities. They can be attached to
BASIC direct or group chats and are addressed explicitly after a normal message;
attaching a named agent to MLS E2EE is rejected in stage 1. See the [named AI
network contract](ai-network.md).

Secret references are names only; the process resolves them at runtime:

```toml
[security]
master_key_env = "QGRAMM_MASTER_KEY"
```

```text
TOML reference name -> process environment value -> key material
```

`validate` and `explain` report the reference name and never the value.

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

`qgramm-build build` derives `qg_*` tags from the final effective feature set. Rebuild only when that set changes; ordinary configuration, capacity, policy, URL and secret-reference changes need a restart. A feature disabled in TOML is absent from the selected build. `plan` shows the selected tags and uncalibrated sizing estimate; `validate` and `explain` are offline configuration diagnostics.

Disabled modules have no routes, tables or workers in a new deployment. Tables from a previously fuller deployment are not destructively deleted when features change.

## Sizing

`expected_concurrent_users` means concurrent connected people, initially assuming one socket per person. Automatic limits allow 20% connection headroom and bound queues/workers by local resources. Active traffic, multiple devices, group fanout, files, AI latency and retention alter capacity substantially.

`qgramm-build plan` prints chosen settings and assumptions; `compose` explicitly writes a local deployment file with estimated CPU/RAM limits. These estimates are heuristic until calibration and do not reserve hardware. Runtime uses CPU/cgroup limits; operators can set explicit limits after measurements. The first benchmark target is 10,000 connections, separate from sustained message rate.

## Backup/restore

Stop the container. Run `qgramm -config ... -backup /data/backup.db` using the same build/config/environment; the command must run against a stopped instance and writes a consistent SQLite backup. Back up the file directory and all relevant keys separately with the service stopped. Restore database, files and matching keys together before starting **one** instance. Do not restore old AI MLS state and resume sending without a fresh cryptographic identity/epoch: counter rollback can invalidate security.

Schema versions are tracked by `schema_versions`. Version 2 adds retention indexes atomically to existing databases. Cleanup deletes at most 256 expired rows per statement and yields between batches, within a 20-second cycle budget; large backlogs can continue on later minute ticks. Old messenger data is not imported.
