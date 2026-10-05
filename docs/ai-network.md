# AI network: configuration, participants and streaming

Named AI profiles extend the existing direct-chat AI API. A bot has a stable,
permanent user/device identity, encrypted private identity keys, separate encrypted context for each chat, and a configured
endpoint. The first stage supports named bots in BASIC direct/group chats;
the existing direct MLS AI path remains available. Adding named bots to MLS
groups requires explicit admission/rekey support and is rejected at this stage.

## Configuration

See [the complete example](../configs/ai-network.toml). Configuration resolves
global `[ai]` settings, then `[ai.endpoints.NAME]`, then `[ai.bots.NAME]`.
Explicit `false` overrides an inherited boolean. Explicit zero numeric limits
are rejected rather than silently inheriting. A named configuration without a
global model must select a valid `default_bot`.

Endpoints configure `provider` (`openai` or `anthropic`), `url`, `model`,
`key_env`, `auth`, `allow_private`, `streaming`, `capabilities`, and limits.
Bots reference an endpoint and may narrow/select limits, tools and a static
system prompt. Supported capabilities currently are `basic_text` and `tool`.
They describe configured adapter capabilities, not an independently probed
guarantee about a remote model. Multimodal input/output and structured output
need additional wire contracts; unsupported capability names fail validation.

`auth="bearer"` uses a runtime environment reference. Explicit `auth="none"`
requires `allow_private=true` and no key reference. Local OpenAI-compatible
servers can use this mode. It is an operator trust decision: plaintext reaches
that configured endpoint. Redirects/proxy environment variables remain disabled,
and each request resolves and dials validated addresses once. Credential values
never enter TOML, the build manifest, job records or logs.

`features.ai_streaming=true` selects `qg_ai_streaming`. A bot may set
`streaming=false` while the module is compiled. Removing the feature requires a
rebuild; then SSE provider implementations, progress routes, tables and cleanup
are absent. Provider-free binaries also exclude all AI participants/workers.

## Named participants

The management backend creates an agent from a TOML bot profile:

```http
POST /management/v1/ai/agents
Authorization: Bearer <management-secret>
Content-Type: application/json

{"bot_name":"assistant","user_id":"ai-assistant","tools":[]}
```

`user_id` and `tools` are optional; when `user_id` is omitted the server creates
the stable AI user identity, and a missing `tools` value starts from the bot's
configured tool set. Use the returned identity in ordinary chat management, then attach its AI
session with `POST /management/v1/ai/agents/{agent}/chats/{chat}` and a `tools`
allowlist. The configured bot tool set is an upper bound. The session context
is private to that bot/chat; one bot can serve several chats and several bots
can participate in a group. Membership, `can_send`, user disable and device
revocation are still enforced. A direct chat retains its two-member limit.
The endpoint and bot settings are resolved from current TOML when a task starts;
the encrypted creation profile is retained for identity/audit continuity.

After sending an ordinary encrypted user message, explicitly address agents:

```http
POST /v1/chats/{chat}/ai/tasks
Authorization: Bearer <device-token>
Content-Type: application/json

{"message_id":"accepted-message-id","agents":["ai-assistant","ai-reviewer"]}
```

The source belongs to the authenticated sender/device. `(message_id,agent_id)`
is the durable deduplication key. Each bot/chat executes one task at a time;
independent sessions can progress concurrently. AI output does not implicitly
invoke another AI. Task metadata is available at
`GET /v1/chats/{chat}/ai/tasks/{task}`; the list route returns the latest 100
tasks. Named tasks can be cancelled with `DELETE /v1/chats/{chat}/ai/tasks/{task}`
even when streaming is not compiled. Source and recipient ACLs are checked
before execution and before publishing the final normal message. Source deletion
invalidates captured context and prevents late replies. External requests already
sent cannot be recalled. Restart marks in-flight external outcomes `uncertain`;
it never blindly repeats a paid or side-effecting request.

## Durable streaming

OpenAI-compatible Chat Completions and Anthropic Messages SSE are processed
incrementally. Text and tool argument deltas are bounded; malformed/truncated
streams fail closed. Tool deltas are data, not permission to execute a tool.

Each saved chunk produces `ai.progress.available` with job ID, chunk cursor and
kind. This event carries no text/tool arguments. Fetch ciphertext through:

```http
GET /v1/chats/{chat}/ai/jobs/{job}/progress?after=0
Authorization: Bearer <device-token>
```

The response contains at most 200 chunks and current job status. Continue from
the last chunk cursor; reconnect uses the ordinary event journal. BASIC chunks
are encrypted at rest and HPKE-wrapped for the requesting device. MLS direct-chat
chunks are MLS ciphertext; the sender state is committed atomically with the
chunk and event. Final output uses the latest durable sender ratchet.

Decrypt each BASIC envelope or MLS message with the ordinary `Binding(chat,
sender,device,operation_id)`; operation IDs are `ai-progress-{job}-{chunk}`.
Plaintext is provider-neutral JSON: `text.delta` contains `text`,
`tool.started` contains `index/id/name`, and `tool.arguments.delta` contains
`index/arguments`. These are previews. Only a `succeeded` job and its final normal
message establish completion; previews may end in `uncertain`, `failed` or
`cancelled`.

The first text delta is committed immediately; later text is coalesced at 4 KiB,
a 25 ms arrival interval, a different event kind, or provider termination. There
is no timer worker per stream. Progress is capped at 4096 chunks / 2 MiB stored
per job. Retention follows `policy.event_retention_hours`, with bounded cleanup.
An expired cursor returns 410; a cursor ahead returns 400. Use final message
history to recover a completed result. Progress retrieval also checks source
visibility and current device/member access.

The source owner or chat administrator can call
`POST /v1/chats/{chat}/ai/jobs/{job}/cancel`. Cancellation first commits its state,
then aborts the local active request. Late chunks/results are rejected. It cannot
undo already-sent external effects or guarantee that a provider stops billing.

## Following stages

Stage 2 adds signed invocation grants, per-tool approvals, encrypted usage/budget
records and events before plaintext leaves the configured protected contour.
Stage 3 adds LLM/tool crypto sidecars and scoped file/RAG/GraphRAG storage with
explicit key ownership. These are planned contracts, not current capabilities.
An inference/retrieval process necessarily handles plaintext within its trusted
execution boundary. Routing ciphertext does not protect against compromise of
that host. Independent MLS interoperability/audit qualification remains separate
from local participant/streaming tests.
