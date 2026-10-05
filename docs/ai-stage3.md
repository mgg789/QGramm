# AI stage 3: scoped encrypted storage and MLS sidecars

Stage 3 has two separate contours. The integrated vault is a bounded encrypted
resource store inside the trusted QGramm container. The optional `micro-safer`
worker is an external MLS participant that owns its private state. Neither
contour is a server-side embedding service or an ANN index.

## Integrated vault

Enable `ai_policy` and `ai_storage` together with a provider feature. The
example is [configs/ai-storage.toml](../configs/ai-storage.toml). A storage
tool is declared in `[[ai.tools]]` with `kind = "storage"`, a fixed
`resource`, `storage_action` (`read`, `search` or `graph`) and
`require_approval = true`. Storage tools cannot carry an HTTP URL, secret,
methods or `allow_private` settings. The tool's resource and action are part
of the policy request.

The management API uses the management bearer and is an administrative API:

```text
PUT    /management/v1/ai/storage/resources/{resource}
DELETE /management/v1/ai/storage/resources/{resource}
PUT    /management/v1/ai/storage/resources/{resource}/documents/{document}
GET    /management/v1/ai/storage/resources/{resource}/documents/{document}
DELETE /management/v1/ai/storage/resources/{resource}/documents/{document}
PUT    /management/v1/ai/storage/resources/{resource}/edges/{edge}
DELETE /management/v1/ai/storage/resources/{resource}/edges/{edge}
GET    /management/v1/ai/storage/requests/{request}
POST   /management/v1/ai/storage/grants
DELETE /management/v1/ai/storage/grants/{nonce}
POST   /management/v1/ai/storage/mcp
```

The MCP endpoint is also administrative and accepts the supported JSON-RPC
initialization, tool listing, and retrieval calls. A model must never receive
this management credential or call this endpoint. Model retrieval runs through
the storage tool path, where the generic AI policy approval and a second typed
storage grant are both checked.

There are two independent grant contracts. The generic AI policy grant uses
`ai_policy.audience`; the storage resource grant uses the distinct
`ai_storage.audience` and is checked after the policy effect is bound. They are
not interchangeable tokens.

The storage grant is an Ed25519 JWT with the exact fields `nonce`, `request_id`,
`request_hash`, `chat_id`, `source_user`, `ai_user`, `resource`, `action`,
`destination`, `iss`, `aud`, `iat`, and `exp`. Unknown or duplicate claims,
wrong issuer/audience, an excessive TTL, or an action outside `read`, `search`,
and `graph` is rejected. The grant is bound to the policy request's exact
effect hash, provider destination, resource and action. Core consumes and
revokes grants; the storage package only verifies and binds them.

The resource header (`scope`, `owner`, and routing visibility) is checked by
resource before payload decryption. `shared` resources may be explicitly
shared. `user` resources belong to the source user and `bot` resources belong
to the AI participant; private retrieval is limited to a two-member direct
chat. A retrieval request contains one resource only, so one query cannot mix
private and shared resources or labels.

Resource metadata, document text, vectors, filenames, file bytes, document
metadata, and edge fields are encrypted at rest with record-specific AAD.
Search uses supplied vectors and optional lexical text matching. It performs a
bounded scan and bounded top-K result selection; graph traversal is a bounded
BFS over encrypted edges. There is no embedding model, cloud embedding call,
ANN index, or plaintext content/vector index. Search responses omit file bytes
and vectors. The package limits item size, text/metadata size, vector
dimensions, resource/document/edge counts, result count and graph depth.

Retrieved private context is tainted to the approved provider destination and
resource label. It cannot be released to a different external tool or mixed
with a resource carrying another scope/owner label. Separately granted
resources with the same label may be combined. After a successful vault use, the successful
dialog context is cleared so a later tool cannot reuse the private history.

## External `micro-safer` participant

Build the optional external participant with `ai_endpoint` and `e2ee`. The
endpoint registry exposes:

```text
PUT /management/v1/ai/endpoints/{endpoint}
GET /v1/chats/{chat}/ai/endpoints
```

An endpoint is an active registered device with `kind` `llm`, `tools`, or
`storage`. The registry does not own endpoint MLS private state. The external
worker owns one encrypted singleton state, pins exactly two participants for a
chat, and freezes on membership, group-context, or epoch changes until an
offline rekey/rejoin. Encrypted outbox entries are durable; an uncertain
external effect is reported and is never automatically retried.

The worker receives ciphertext over its HTTPS relay and decrypts only at its
own endpoint boundary. The relay server does not receive plaintext. Local LLM
runtimes have not been independently verified here; only the HTTP contracts
and local mocked transport are covered by the current implementation evidence.

### The standalone worker contract

`qgramm-micro-safer` reads a standalone TOML deployment file. It does not read
or reuse the QGramm server's core configuration. The file names the endpoint
`user` and `device`, the HTTPS `server_url`, secret environment-variable names,
the encrypted local `db_path`, the grant `issuer` and `audience`, pinned
`peers` and `chats`, fixed `handlers` and `models`, and optional local storage
limits. URLs and credentials are deployment data: a request body cannot choose
an HTTP URL, bearer key, model, or MCP endpoint. Configured model and handler
URLs reject credentials, query strings, and fragments.

The endpoint and model/tool plaintext boundary is explicit. Relay traffic is
MLS ciphertext over HTTPS; a configured local loopback model or tool receives
plaintext at the worker host. An external model or HTTP/MCP tool requires both
`[endpoint].allow_external_plaintext = true` and the handler/model's
`allow_plaintext = true`. This is an opt-in to the trusted endpoint host's
outbound plaintext request, not an embedding or agent-runtime protocol.

The worker's CLI is deliberately small:

```text
qgramm-micro-safer init   -config qgramm.toml [-chat name]
qgramm-micro-safer join   -config qgramm.toml [-chat name] -welcome file-or-base64
qgramm-micro-safer run    -config qgramm.toml [-chat name]
qgramm-micro-safer ingest -config qgramm.toml -input content-bundle.json
```

`init` creates the local participant and prints public bootstrap material;
`join` consumes a Welcome and verifies the pinned two-member group; `run`
opens the encrypted singleton state, attaches configured handlers, and polls
the relay; `ingest` imports a trusted local plaintext content bundle into the
encrypted storage sidecar. Protect the import file separately; encryption of
the database does not delete that file. `run` exits on the host process's interrupt or
`SIGTERM`. The wire contract has no peer-issued cancellation command; an HTTP
context may still cancel a local in-flight call.

The build and deployment configurations are separate. `qgramm-build build
-target core` builds the server, while `qgramm-build build -target
micro-safer` builds `bin/qgramm-micro-safer` from `./cmd/qgramm-micro-safer`
with the feature manifest from the core build configuration injected as build
tags and a feature string. The resulting worker then reads its own TOML at
runtime. A core TOML file is not a worker deployment file, and this page does
not imply that an upstream local model runtime has been installed or accepted.

### Relay, RPC, and handler wire contracts

The worker's relay transport uses `Authorization: Bearer <token_env>` and
bounded requests. It checks `/v1/chats/{chat}/mls`, polls
`/v1/chats/{chat}/events` with a durable cursor, and sends encrypted responses
to the configured chat. Joining requires offline group ID and peer signing-key
pins; additional initial `group_context` and nonzero `epoch` pins are optional.
Runtime always checks the relay group ID/context/epoch against the retained
participant state and requires a roster of exactly two devices. Chat names may
reference a differently named peer via `[chats.<chat>].peer`.
A mismatch freezes processing until an offline Welcome/rekey restores
the pinned state; no automatic membership change is performed.

The encrypted RPC request is version 1 and has `request_id`, `client_id`,
`chat`, `source_peer`, `action`, `name`, `body`, and an optional outer `grant`.
Its body is strict canonical JSON with duplicate and trailing values rejected.
Responses are durable typed frames with `request_id`, `kind`, `seq`, `final`,
and `body`; kinds include `notice`, `delta`, `result`, `error`, and
`uncertain`. `source_peer` identifies the source device (`user/device` is also
accepted by the storage sidecar), while the endpoint's configured `user` and
`device` identify the participant executing the request. These identities are
not interchangeable.

Configured `http` handlers accept only GET, POST, or PUT and return bounded
JSON. `mcp`/`tool` handlers use a configured URL and operation limited to
`initialize` or `tools/call`. A `model`/`llm` handler invokes a
configured OpenAI-compatible text chat completion endpoint. Its request has
text `system`, `user`, or `assistant` messages and a fixed configured model;
the caller cannot select a different model or destination. This contract does
not claim multimodal input, native agent-framework semantics, or arbitrary
OpenAI extensions.

The model stream is raw SSE: only `data:` lines are consumed, each frame is
bounded, and `[DONE]` is required. Frames are persisted as typed `delta` and
final `result` responses before relay acknowledgement; an external plaintext
model first emits a safe `notice` naming the configured endpoint. There is no
remote peer cancellation or automatic retry of an uncertain external effect.

### Two grant contracts at the worker boundary

The outer RPC grant is an MLS-bound typed request grant. Its exact claims are
`request_id`, `chat`, `source_peer`, `target_endpoint`, `action`, `name`,
`body_hash`, `destination_hash`, `epoch`, `group_id`, `group_context`,
`nonce`, `iss`, `aud`, `iat`, and `exp`. Verification binds the exact RPC body,
handler name/action, endpoint, destination hash, group state, issuer/audience,
and a maximum 900-second lifetime.

Native vault retrieval uses a separate Ed25519 JWT contract with
`request_id`, `request_hash`, `chat_id`, `source_user`, `ai_user`, `resource`,
`action`, `destination`, `nonce`, `iss`, `aud`, `iat`, and `exp`. The sidecar
verifies its configured storage issuer/audience, exact request hash and
destination, resource scope/owner, and the pinned source identity, then
consumes the nonce once in its local encrypted database. The outer RPC grant
and native vault grant have distinct audiences and are never substituted for
one another.

### Local persistence and limits

The worker stores its MLS singleton, cursor, intents, inbox, and outbox in an
encrypted local SQLite database (schema version 2). `max_pending` defaults to
4096 and is bounded by configuration; transactional inserts reject new
pending intents, inbox items, or unsent outbox items once that count is full.
Retained intents, inbox rows, RPC grants and storage grants are each capped at
`max_pending * 4`; retained outbox rows at `max_pending * 8`, including terminal
rows. Reaching a retained cap stops admission and requires an operator-managed
offline replacement identity/group and state migration. Do not delete replay
rows while reusing the same MLS identity. This is a beta operational limit.
`request_timeout_ms` defaults to 120000 (maximum 1800000) and bounds relay,
model and tool HTTP requests, including streaming. The integrator supplies and
rotates short-lived relay tokens; the worker reads the named environment value.
Endpoint frame and body sizes, concurrency, storage item/text sizes, vector
dimensions, resource/document/edge counts, result counts, and graph depth are
also bounded by the standalone configuration. The storage defaults are 1024
resources, 10000 documents, 20000 edges, 8 MiB items, 1 MiB text, 100 results,
and graph depth 16 when storage is enabled.

Durable rows are retained for cursor progress, replay deduplication, outbox
replay, and explicit uncertain outcomes. The worker does not run an automatic
garbage collector that could delete replay or tombstone evidence while an
effect is unresolved. No claim is made here that these limits replace
deployment-specific capacity planning.

## Contract-only grant example

The following is an HTTP contract example for an integration backend. It does
not import QGramm's internal Go packages and does not replace authorization in
the caller:

```http
GET /management/v1/ai/storage/requests/req-42
Authorization: Bearer <management-secret>

POST /management/v1/ai/storage/grants
Authorization: Bearer <management-secret>
Content-Type: application/json

{"token":"<Ed25519 JWT with the exact storage claims>"}
```

The token must carry the request ID and hash returned by the request endpoint,
the configured resource/action, and the exact effective provider destination.
The model then supplies only the approved retrieval arguments to its `storage`
tool, for example `{"query":"invoice","limit":8}`. The caller must treat
the result as provider-destination-tainted and discard successful private
history after the retrieval workflow.

A standalone backend can keep the internal signing implementation out of its
client integration and exchange only the HTTP contract:

```go
type StorageGrant struct {
    Nonce string `json:"nonce"`; RequestID string `json:"request_id"`; RequestHash string `json:"request_hash"`; ChatID string `json:"chat_id"`
    SourceUser string `json:"source_user"`; AIUser string `json:"ai_user"`; Resource string `json:"resource"`; Action string `json:"action"`; Destination string `json:"destination"`
    Issuer string `json:"iss"`; Audience string `json:"aud"`; IssuedAt int64 `json:"iat"`; ExpiresAt int64 `json:"exp"`
}

// token is produced by the backend's Ed25519 signer with the exact JWT tags
// and the distinct ai_storage audience; it is never a generic wildcard grant.
req, _ := http.NewRequest("POST", baseURL+"/management/v1/ai/storage/grants", bytes.NewReader([]byte(`{"token":"`+token+`"}`)))
req.Header.Set("Authorization", "Bearer "+managementSecret)
req.Header.Set("Content-Type", "application/json")
resp, err := http.DefaultClient.Do(req)
```

The snippet is a wire-contract example: the caller supplies its own signer,
JWT duplicate-claim rejection, key storage and HTTP error handling.

See the generated [OpenAPI contract](openapi.json), the [configuration guide](configuration.md),
and the [Russian stage-3 contract](ai-stage3.ru.md).
