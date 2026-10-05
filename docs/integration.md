# Backend integration

One deployment serves one application. Your backend authenticates people, binds device public keys to their identities, and manages membership. QGramm deliberately does not register people or implement your UI.

## Provisioning

Use `Authorization: Bearer <management secret>` only from your trusted backend:

1. `PUT /management/v1/users/{user}` with `{"disabled":false}`.
2. `PUT /management/v1/users/{user}/devices/{device}` with base64 X25519 `public_key` and, for MLS, Ed25519 `signing_key`.
3. `POST /management/v1/chats/direct` with `{"id":"stable-chat-id","mode":"basic","members":["alice","bob"]}`. For groups use `/chats/groups` when compiled.
4. `PUT /management/v1/chats/{chat}/members/{user}` with `role`, `can_send`, `active`, `allow_history`. Device keys are immutable; rotation creates a new device ID and revokes the old one.

Identifiers accept ASCII letters, digits, `_`, `-`, `.` and at most 128 characters. Provide stable chat IDs: retries of a create with an existing ID return 409 rather than creating a second conversation.

Sign client JWTs with your Ed25519 private key, never inside a browser. Claims: `iss`/`aud` matching TOML, `sub` user ID, `device_id`, `iat`, `exp`. Lifetime must be at most 15 minutes. QGramm stores only the verification public key, checks disabled users/revoked devices, and rechecks open sockets. Keep the management secret out of clients.

## Basic message encryption

Fetch authenticated `/v1/capabilities`. Authenticate/pin its server key through your backend before trusting it; a compromised key-distribution authority can impersonate endpoints.

HPKE uses RFC9180 mode Base, KEM 32 (X25519), KDF 1 (HKDF-SHA256), AEAD 1 (AES-128-GCM), info UTF-8 `qgramm/basic/v1`. The JSON envelope has `key_id`, `enc`, `ciphertext`, using standard padded base64. `key_id` is the full 32-byte SHA256 digest of the receiver public key, lower-case hex.

AAD is compact JSON produced in this exact key order, without spaces:

```json
{"version":1,"chat":"chat-id","user":"sender-user","device":"sender-device","operation":"client-operation-id"}
```

The normative field names and byte encoding are in `internal/cryptoenc/engine.go` and its RFC vector tests. Submit `POST /v1/chats/{chat}/messages` with `operation_id` and `envelope`, plus optional attachment/reply/forward references. Ciphertext protects the payload; routing references remain visible and server-authorized. Applications needing end-to-end integrity of such references must include and verify them in their encrypted application payload.

For a fetched basic message, AAD uses the **recipient** user/device and the server message ID as `operation`. QGramm decrypts inside the container, stores AES-256-GCM ciphertext, and rewraps to each requesting device key. Basic mode cannot protect against the container operator.

## Delivery and retries

Send and batch accept the optional HTTP header `Prefer: return=minimal`. A single
send still returns 201 after durable commit, with
`{"status":"accepted","receipt":{"message_id":"...","chat_id":"...","operation_id":"...","seq":1}}`.
Successful batch items use `receipt` instead of `message`; failed items keep
their existing status/error. The response includes `Preference-Applied: return=minimal`.
Without this header the full message response remains unchanged. The compact
receipt avoids reading and re-encrypting message content for the sender;
recipient delivery and encryption are unchanged. Retry the exact encrypted
request: the receipt identifies the original acceptance, even after edits or
deletion, while current access is checked again. It is not a delivery/read receipt.

Replay orders events by chat sequence, but message events project the currently
accessible message state rather than an immutable historical payload. Journal
reads, batched message projection and optional projection hooks are separate
reads; the page is not a single database snapshot. A later delete can therefore
make an earlier creation event contain a tombstone. Clients apply revisions and
deletion precedence; a membership change during projection can require resync.

- A 201 send result means SQLite committed both message and event. It does not mean a recipient received/read it.
- Preserve the complete serialized request and operation ID for retries. Changing ciphertext or metadata while reusing an operation ID returns 409, even if plaintext is identical.
- `POST .../messages/batch` accepts `{messages:[...]}` and returns HTTP207 with per-item results. A batch is not an all-or-nothing transaction; retry failed items only. Prepared elements share bounded FULL commits (up to16 messages/8MiB and queue capacity), with an individual savepoint per element. Preparation/savepoint failures remain per-item; an unsuccessful shared commit rejects its tentative successes. Duplicate operation IDs force the earlier group to finish before retry validation. ACK and notifications follow commit. A single message above the group byte limit runs alone within the configured message limit.
- History: `GET .../messages?after=0&limit=100`, ordered by creation sequence. Events: `GET .../events?after=0`, ordered by chat sequence.
- Events refer to the **current message projection**, so replay after edits/deletion converges to present state rather than recreating deleted payloads.
- `POST .../receipts` accepts `{delivered: N, read: M}` with `0 <= M <= N <= chat sequence`. These are explicit per-device claims, not proof a human viewed the message. Duplicate receipts do not create new events.
- Delivery is at-least-once; deduplicate by `(chat_id,seq)` and message ID/revision. Deduplication is guaranteed only within configured retention.
- HTTP410 or `sync.error` after retention requires syncing history and current chat state. Do not treat it as an empty event stream.

## WebSocket

Obtain `POST /v1/ws-tickets` using the client JWT. Connect `/v1/ws?ticket=...` within 30 seconds; the ticket can be consumed once. Never put your long-lived token in a URL. Connections expire after 15 minutes and must reconnect with a fresh ticket.

```json
{"type":"subscribe","chat_id":"chat-id","after":42}
```

Use `unsubscribe` to remove a subscription. Up to 128 chats may be subscribed per connection. Send receipts via HTTP; after reconnect resume from your durable acknowledged cursor. Slow sockets are closed; persisted messages remain replayable. The server checks allowed browser origins configured in TOML.

## Optional operations

Edits use PATCH on the message with `operation_id`, `expected_revision`, and `message` containing the newly encrypted replacement. Only text payload changes; references/attachments cannot be replaced through edit. Deletes use DELETE with JSON `operation_id`; global mode removes active payload storage, author_only hides it from the author. Replies must target accessible messages in the same chat. Forwards require access to the source and fresh encryption for the destination.

Reactions POST to `.../messages/{message}/reactions` with `operation_id`, numeric `type`, `remove`; GET returns types/user IDs. The application renders the type dictionary and supplies authorization/UI semantics.

Clients implement MLS, media capture, WebRTC and human-visible identity verification. The server does not ship a client SDK. See the E2EE, files/calls and AI documents for their separate contracts.
