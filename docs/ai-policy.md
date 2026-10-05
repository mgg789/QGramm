# AI policy: approvals, usage and egress

Stage 2 adds the optional `features.ai_policy` / `qg_ai_policy` module to named BASIC agents and the existing direct MLS AI path. See [the TOML example](../configs/ai-policy.toml), [AI participants](ai-network.md) and [wire schemas](openapi.json). At least one AI provider must be compiled. Removing this feature requires rebuilding; a fresh database then has no policy routes, tables or cleanup. Existing tables are not automatically dropped.

## Configuration

| Field in `[ai_policy]` | Default / meaning |
|---|---|
| `grant_public_key_env` | `QGRAMM_AI_GRANT_PUBLIC_KEY`: base64 Ed25519 public key, 32 bytes; the backend retains the private signing key |
| `issuer`, `audience` | `qgramm-backend`, `qgramm-ai-control`; separate from device token issuer/audience |
| `approval_ttl_seconds` | 300; 1–900 seconds, maximum grant lifetime |
| `require_provider_approval` | false; true pauses before every provider invocation, including calls after tool results |
| `provider_reserve_microunits` | 1000000; 0–1000000000000, reservation before each provider call |
| `global_daily_budget_microunits` | 0 (unlimited); otherwise 1–1000000000000000 |
| `per_bot_daily_budget_microunits` | Same range; scope is the AI participant `user_id`, not its TOML bot profile name |
| `per_user_daily_budget_microunits` | Same range; scope is the source user (including an AI participant where the integration permits it) |
| `currency` | `USD`; exactly three uppercase letters, accounting label only |

One microunit is one millionth of the configured currency unit. Nonzero budgets require a positive provider reservation. `[ai]` and `[ai.endpoints.NAME]` accept `input_price_microunits_per_million_tokens` and `output_price_microunits_per_million_tokens` (default 0, maximum 1000000000000). Endpoint prices override global prices. Each `[[ai.tools]]`, whether HTTP or MCP, accepts `require_approval` (default false) and `cost_microunits` (default 0, same maximum). Tool success uses that fixed cost. Approval/cost settings cannot silently take effect without the policy module: incompatible configuration is rejected.

## Backend-issued permission

Ordinary chat text, model output, tool arguments and streamed tool deltas cannot grant permission. When a required approval is absent, the effect is not sent, encrypted continuation is saved and the job becomes `awaiting_approval`. The worker is released; this bot/chat remains serialized until the task resumes or terminates. The durable `ai.approval.required` event contains `job`, `request_id` and `action="approval_required"`, without prompt or arguments.

The management backend reads `GET /management/v1/ai/requests/{request}`. Its metadata contains the identities, epoch, session version, effect action/name, request and destination hashes, sanitized destination origin, required flag, reserve, status and timestamps. It does not expose the stored prompt, arguments or continuation. The backend makes its own approval decision and signs a JWT with Ed25519, header exactly `{"alg":"EdDSA","typ":"JWT"}`. The payload has exactly these fields:

```json
{
  "v": 1, "jti": "unique-single-use-nonce",
  "request_id": "request-id", "job_id": "job-id", "chat_id": "chat-id",
  "source_user": "human-user", "source_device": "human-device",
  "ai_user": "bot-user", "ai_device": "bot-device", "epoch": 0,
  "action": "provider", "name": "openai",
  "request_hash": "64-hex-digest-from-request-metadata",
  "destination_hash": "64-hex-digest-from-request-metadata",
  "iss": "qgramm-backend", "aud": "qgramm-ai-control",
  "iat": 1791158400, "exp": 1791158700
}
```

This is the typed claim shape, not a usable token. Copy the binding fields from the pending request, use current Unix seconds and a fresh nonce; never sign a generic wildcard grant. Actions are `provider` and `tool`. Request hashing binds the effective request, destination, cost/price configuration, epoch and session revision. The full destination is hashed; notices expose only its origin. Tool arguments are normalized with duplicate-key rejection before hashing and sending; this normalization is not RFC 8785. Unknown/duplicate claims, wrong signature, scope, audience or timestamps fail closed.

Management calls require the separate management bearer:

| Route | Body / result |
|---|---|
| `POST /management/v1/ai/approvals` | `{"token":"signed-JWT"}` → `grant`, `status="approved"`; identical submission is idempotent |
| `DELETE /management/v1/ai/grants/{grant}` | Revoke a grant ID or its nonce, including a nonce before issue → `grant`, `revoked` |
| `GET /management/v1/ai/requests/{request}` | Bound metadata; request lifecycle includes awaiting, approved, dispatched, completed and uncertain |
| `GET /management/v1/ai/usage` | `items`, optional `next_cursor`; `limit` 1–100, descending `cursor`, optional `user` (source user ID) or `bot` (AI participant user ID) filter (mutually exclusive) |

An approved job returns to `queued`. On resume the server rechecks current source visibility, membership, device status, tool allowlist, epoch, session revision, signature, expiry and revocation. Grant consumption, budget reservation, egress notice and dispatched intent commit together before network I/O. A nonce can authorize one effect. Bounded cleanup processes at most 256 expired awaiting/approved requests per pass, clears continuation and fails waiting jobs; expired grants are rejected immediately during verification. Cancellation and source deletion prevent late publication; already-sent effects cannot be recalled. The encrypted continuation resumes before the approved effect without repeating earlier provider/tool calls or reopening already-consumed MLS input.

## Accounting and confidentiality

All provider and tool invocations pass through the policy gateway, even when approval is disabled. Before sending, it atomically reserves against global, bot and source-user daily accounts (UTC). A budget rejection sends no request. Each successful known provider usage settles the reservation into an operator-configured estimated cost, rounding input and output components upwards independently. Anthropic cache input counts are included in input tokens and also exposed separately. OpenAI-compatible streaming requests ask for the terminal usage chunk; Anthropic streaming processes cumulative usage without summing repeated totals. Wire contracts: [OpenAI usage](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events), [Anthropic usage](https://platform.claude.com/docs/en/build-with-claude/streaming).

Missing usage is unknown, never zero. Unknown usage and uncertain network outcomes retain the reservation. Dispatched effects are not automatically retried after restart. A successful reply can coexist with unknown usage. Actual estimated cost can exceed the reservation, then blocks subsequent effects when the daily budget is exceeded. Set conservative reserves for the model and output limit. These counters are estimates, not reconciled provider invoices or a hard cap on external charges. Cache-specific tariffs and provider-specific billing rules are not inferred.

Usage items expose scope, request/job IDs, UTC day, invocation count, reserved/cost microunits, input/output/cache token counts, `usage_known`, currency and status. The same invocation is represented in several budget scopes: do not sum all scopes as distinct calls. Use one scope, such as a bot filter, for a report. Permission tokens, continuation, ledger amounts and aggregate counters are encrypted at rest; IDs, timestamps and routing metadata remain visible. Preserve matching master keys with backups.

Before every effect a durable `ai.egress.notice` contains `job`, `request_id`, `action`, `name`, `destination_origin` and a confidentiality value. Ordinary provider/tool calls use `confidentiality="external_plaintext"`; integrated vault retrieval uses `confidentiality="integrated_storage"` and still requires the separate storage grant described in [AI stage 3](ai-stage3.md). This establishes ordering before egress; a client may receive the event later. Integrators can render their own security notice. URLs with paths/query strings, prompts, responses and tool arguments are absent from this event. A signed permission is authorization, not encryption: the configured provider or tool receives plaintext. A private IP alone does not make an endpoint cryptographically protected.

Stage 3 LLM/tool encryption sidecars, protected remote endpoints and scoped file/RAG/GraphRAG storage are not implemented by this module. The container, inference host and retrieval host remain trusted plaintext boundaries. Local stage-2 tests do not establish an independent cryptographic audit or new live-provider acceptance.
