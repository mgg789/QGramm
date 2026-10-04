# AI participants and external tools

AI adapters are optional compiled modules: `qg_openai`, `qg_anthropic`,
`qg_mcp`, and `qg_http_tools`. Tools require at least one compiled provider.
Credentials are runtime environment references in TOML; no provider credentials
are baked into binaries or deployment files.

Global message deletion resets that AI chat's entire stored context, including
possible paraphrases, and marks its queued/running jobs failed in the deletion
transaction. A late provider result cannot restore captured context or create a
reply for those jobs. MLS participant state is retained independently. Requests
already sent to providers/tools cannot be recalled; author-only hiding does not
reset shared context. Other existing messages and downloaded/provider copies
are subject to their own retention.

AI resource limits are configurable with `[ai]` fields `max_context_turns`
(default 20, range 1..20), `max_context_bytes` (262144, range 1..262144),
`timeout_seconds` (45, range 1..45), and `max_response_bytes` (1048576,
range 1..1048576). Context is bounded by serialized JSON bytes, keeps the latest
user turn and trims older complete turn prefixes. An oversized latest turn is
rejected before provider access. Intermediate tool conversations also fail
closed at these context bounds. Encoded outbound requests retain the fixed
1 MiB safety ceiling. Each provider/tool network call has a configured deadline;
JSON and MCP SSE responses use bounded readers.

Each configured tool may set `timeout_seconds` and `max_response_bytes`;
0 inherits the global value. Positive tool values can only narrow the effective
global limit, with absolute ceilings 45 seconds/1048576 bytes. Tool result text
also retains the existing 65536-byte ceiling. Revocation/schema/egress controls
still apply before every tool effect.

Ограничения `[ai]`: `max_context_turns` (20, 1..20), `max_context_bytes`
(262144, 1..262144), `timeout_seconds` (45, 1..45), `max_response_bytes`
(1048576, 1..1048576). Context измеряется serialized JSON bytes; сохраняется
последний user turn, oversized одиночный input отклоняется до provider call.
Tool может задать `timeout_seconds`/`max_response_bytes`: 0 наследует global,
положительное значение только сужает effective limit. Сохраняются ceiling
1 MiB outbound request и 65536 bytes tool text, штатные ACL/schema/egress guards.

Management explicitly creates an AI user, device and direct chat:

```http
POST /management/v1/ai/participants
Authorization: Bearer <management-secret>
Content-Type: application/json

{"user_id":"existing-human","device_id":"registered-device","provider":"openai","mode":"basic","tools":[]}
```

No tool permission is inferred from a message or provider output. Replace a
chat's allowlist with `PUT /management/v1/ai/chats/{chat}/tools`, body
`{"tools":["configured-name"]}`. Names must already exist in TOML and their
adapters must be compiled. Each tool call checks the current allowlist immediately
before sending. Revocation cannot recall a request already sent to an external
service. HTTP tools require exactly one configured HTTP method. Arguments are
JSON objects sent as the request body to the fixed configured URL; models cannot
choose an endpoint or arbitrary request headers.

Supported local schema vocabulary: `type`, `properties`, `required`,
`additionalProperties` (boolean), `items`, `enum`, `description`, and `title`.
Supported types are object, array, string, number, integer, boolean and null;
depth is limited to eight. Unknown properties are denied unless explicitly
allowed. Unsupported JSON Schema constraints fail closed. This is a limited
schema validator, not a full JSON Schema implementation.

The OpenAI adapter uses JSON Chat Completions and function tools. The Anthropic
adapter uses Messages with text, tool_use and tool_result blocks. Provider URLs
require HTTPS and public resolved addresses. Requests use runtime keys and
bounded responses; errors never persist response bodies or credential values.

Remote MCP uses Streamable HTTP initialize, initialized notification and
tools/call. It supports protocol versions 2025-03-26 and 2025-06-18, session IDs,
JSON replies and SSE result events. Every tool invocation creates a fresh
session. It exposes only the explicitly configured tool name, without remote
tool discovery, stdio subprocesses, roots, sampling, resumable streams or
server-initiated execution. MCP session termination and resume are not supported;
remote services should expire idle sessions. HTTP and MCP tools use public HTTPS
by default. `allow_private=true` explicitly permits private targets and HTTP for
that configured tool. DNS is resolved once per request; only validated addresses
are dialed. Redirects and proxy environment variables are disabled, preventing
credential migration to another origin.

Basic AI messages use the existing server HPKE ingress and encrypted at-rest
payloads. In E2EE mode, the management request additionally supplies a base64
`key_package` for the named human device. The KeyPackage must authenticate its
device credential and registered Ed25519 signing key using MLS suite 1. The AI
is an actual MLS group participant, creates the two-member group, and returns a
Welcome plus epoch. The human joins the Welcome. The relay persists the public
GroupContext and authenticated device roster; the AI alone stores its own
encrypted participant snapshot. Message AAD binds chat, original sender, device
and operation ID. Human ratchet state is committed before any provider/tool
request; AI send state and its outbound message commit together before delivery.
MLS membership transitions wait for queued/running AI jobs to drain and apply
confirmed commits to the AI snapshot transactionally. Rollback of database/MLS
snapshots remains unsafe and is not a supported recovery procedure.
The participant snapshot includes a persisted current-epoch replay ledger;
after 65,536 received MLS messages it fails closed until an epoch transition.

Admitting an AI to an E2EE chat authorizes this server participant to decrypt
messages for that chat and send plaintext to the configured provider and
explicitly permitted tools. Human-only E2EE chats remain opaque to the relay.
AI context is encrypted at rest, bounded to twenty retained turns and a 256 KiB
history target. Input must be UTF-8 text; files are not automatically fetched.
The model loop has configurable max_steps (1..64), at most eight tool calls per
step, 64 KiB tool arguments/results, 1 MiB external responses and a three-minute
job deadline. A worker pool uses `capacity.workers`, capped at eight, and claims
jobs transactionally. A chat has at most one running job, preserving context and
MLS state ownership; independent chats progress concurrently. This is not a measured AI
capacity guarantee.

Incoming human messages and queued jobs commit in one SQLite transaction. There
are at most 64 queued/running jobs per AI chat. Job states are `queued`, `running`,
`succeeded`, `failed`, and `uncertain`. A restart changes `running` to `uncertain`
and never blindly retries a provider or tool call. Network errors can have unknown
external outcomes; the adapter conservatively records uncertainty. It guarantees
one local output per job, not exactly-once provider requests or remote tool
effects. Failed persistence after an external request leaves the job in flight;
restart recovery marks it uncertain. No automatic retry/reset route is provided.
Inspect metadata with `GET /management/v1/ai/chats/{chat}/jobs`. Audit records
contain job IDs, action, configured tool name, outcome and timestamp, without
prompt/response bodies. Conversations remain encrypted in the context table.

Verification uses local mocked provider responses, a real local HTTP MCP server,
egress boundary tests, local persistence/dedup and MLS participant roundtrips.
External OpenAI, Anthropic, public MCP endpoints, third-party MLS interoperability
and production load have not been tested by these checks.
