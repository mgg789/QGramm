# Files, batched messages and calls

Reproduce the HTTP/WebSocket contracts from the repository root:

```sh
python3 scripts/generate-contract.py
go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt
go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check
```

The generator maintains explicit schemas of current Go JSON types and asserts
exact coverage of every route declared in core and optional modules. It fails
when a route is added/removed without updating the map. Request/response body
changes still require reviewing the map; this check is not an AST or runtime
contract verifier. All feature routes are documented; a particular compiled
binary exposes only its selected modules. `/v1/capabilities` supplies enabled
features and runtime policy limits. HTTP input rejects unknown fields and trailing
JSON. WebSocket input uses `json.Unmarshal`, so extra fields are accepted.

Authentication uses external authority EdDSA JWTs: `sub`, `device_id`, `iss`,
`aud`, `iat`, and `exp`, with at most fifteen minutes between issue/expiry.
Account and device revocation are checked. Management uses a separate runtime
bearer secret. WS uses a one-use ticket from `POST /v1/ws-tickets` (30-second
lifetime); it does not accept a token as a WebSocket JSON command.

`POST /v1/chats/{chat}/messages/batch` returns 207 and one status per input.
Items commit independently in input order. Retry failed/unknown outcomes with
the same per-device `operation_id` and exactly the same request content;
reusing it for different content returns 409. Dedup retention is configured,
not an unlimited exactly-once guarantee. WS emits durable events with chat-local
sequence numbers. Subscribe from the last persisted `after` cursor, deduplicate
events by `(chat_id,seq)`, and acknowledge delivered/read via HTTP receipts.
There is no JSON WS ack frame. Expired event cursors produce HTTP 410 or
`sync.error`; recover current chat state and HTTP message history before replay.

File uploads start with `POST /v1/chats/{chat}/uploads`. Declare `operation_id`,
encrypted-wire total `size`, number of `chunks`, and SHA256 of concatenated wire
chunks. The configured file/chunk/storage quota and TTL apply. Metadata is bound
to chat, owning user/device and operation; uploader operation IDs support resume
when the declared metadata and basic file key match. The server reserves bytes
transactionally, including per-chunk overhead, before accepting data.

For basic files generate an independent random 32-byte AES key and send it in an
HPKE envelope to the server capability public key using
`cryptoenc.Binding(chat,user,device,upload_operation)`. Each chunk is
`12-byte random AES-GCM nonce || ciphertext || 16-byte tag`, under that file key,
with AAD `Binding(chat,owner,owner_device,upload_operation+"/"+decimal_index)`.
Never reuse a nonce under the file key. The server verifies authentication,
stores an additional at-rest authenticated encryption layer, and keeps the file
key encrypted at rest. Download returns the original encrypted wire chunk. The
key endpoint wraps the file key to the recipient device public key with binding
`Binding(chat,recipient_user,recipient_device,upload_id)` and supplies the original
chunk binding metadata. Basic encryption trusts the server with plaintext/key
access; it is not human-only E2EE.

For E2EE uploads omit the key envelope. The server treats chunk bytes as opaque
and never receives the client file key. Clients must encrypt file data and
distribute its key/manifest inside authenticated MLS application messages, bind
file identity/chunk index/expected lengths, verify completeness and final
digest, and prevent nonce reuse. The relay verifies uploaded byte digests and
bounded sizes; it cannot validate application-layer E2EE file plaintext or keys.
Checksums and upload metadata are visible to the server. Message `attachments`
contains upload IDs; the server accepts only completed uploads in the same chat.

Upload each chunk with `PUT /v1/uploads/{upload}/chunks/{index}` and the
`X-Chunk-SHA256` header containing lowercase SHA256 of its wire bytes. An
identical retry returns 200; a changed chunk returns 409. Query status for received
indices, sizes and hashes before retrying an interrupted upload. Completion
verifies contiguous chunk count, total size and concatenated digest, then marks
ready. The chunk uploader must be the original owner/device. Other members gain
read access only through a live attachment message visible at their joined
sequence. `history=all` does not itself bypass this boundary: granting earlier
history requires management `allow_history`. A global deletion removes live
attachment references; when the last live reference disappears the upload is
revoked and reserved quota is freed. Chunk cleanup occurs separately. Local
message hiding does not revoke everyone else's attachment access.

Calls require a direct chat with exactly two active participants. Create with
`POST /v1/chats/{chat}/calls`, mode `audio` or `video`; it returns call ID and a
`call.ringing` event. Call creation has no operation ID and is not retry-idempotent;
an existing active/ringing call produces a conflict. Signaling uses
`POST /v1/calls/{call}/signals` with type, `operation_id`, and encrypted payload
for offer/answer/ice. Only the callee may accept/reject; only the caller offers;
answer follows acceptance. End/reject closes the call. Signal retries reuse the
same operation and exact content and receive the prior sequence/state.

Basic signaling wraps JSON SDP/ICE fields in HPKE to the server, binds chat,
sender/device/operation, and declares `to_device` belonging to the peer. The
server validates bounded SDP/ICE and rewraps to that device. E2EE signaling uses
an opaque MLS field and current epoch, without `to_device` or HPKE envelope;
pending epoch transitions reject signaling. Applications must authenticate the
call/operation binding inside their MLS payload; the relay does not decrypt it.
Call and recipient metadata remains visible. TURN credentials come from
`GET /v1/calls/turn` with configured TTL and runtime shared secret; actual media
flows over WebRTC directly or TURN, not through the QGramm relay. SDP does not
provide media end-to-end encryption by itself; client WebRTC/media security must
be implemented and verified independently.

The license tool inventories all direct/transitive Go module graph entries and
copies full LICENSE/COPYING/NOTICE/COPYRIGHT files from the local module cache,
plus the Go runtime LICENSE. This is a superset for optional feature builds. It
fails if a module source/license is unavailable; download dependencies with the
normal project setup before generation. It does not claim legal review or infer
licenses from repository names.
