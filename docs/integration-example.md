# Runnable basic integration

From the repository root, with the Go version required by `go.mod`:

```sh
go run ./examples/basic
```

The example starts the real minimal QGramm core on an ephemeral loopback HTTP port, provisions Alice and Bob with separate X25519 devices, creates a basic direct chat, signs Ed25519 JWTs, subscribes Bob over WebSocket, sends an HPKE envelope as Alice, repeats the exact request and checks the same message ID, decrypts Bob's message event and observes a delivered receipt event. Success prints one `OK` line. It uses a temporary SQLite database and randomly generated keys, prints no credentials, and closes sockets/server/database and removes its temporary directory. HTTP and socket operations have deadlines; errors exit nonzero.

The HTTP exception is explicit and confined to this local smoke. Production uses HTTPS/WSS, independently authenticated server key distribution, and separately deployed service/backend/clients. This verifies the repository's own Go implementation; it does not prove interoperability with another HPKE implementation, browser behavior, production TLS, or load capacity.

## Move the pattern into your application

`examples/basic/main.go` marks backend and client steps. Keep `mint`, the Ed25519 signing private key, and management calls in your trusted backend. Authenticate the person and authorize/bind the device's public key before provisioning it or minting a token. Register immutable device IDs and revoke old IDs when rotating keys. Clients own their device private keys, short-lived JWTs, HPKE operations, socket connection and processed event cursor. `localServer` is only a smoke harness; replace it with your deployed service URL and an independently supplied server public-key pin.

The example imports `internal/core` and `internal/cryptoenc` because it lives inside this repository's Go module. These are internal implementation packages, not an externally importable client SDK. An external application uses the documented HTTP/WS wire contract and an RFC9180-compatible HPKE implementation; the helper calls below show the required bindings. See [the integration contract](integration.md) for suites, encoding and policy details.

Backend token minting (check `err`; never ship `private` to a client):

```go
now := time.Now()
token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
    "iss": "qgramm", "aud": "qgramm", "sub": "bob", "device_id": "bob-phone",
    "iat": now.Unix(), "exp": now.Add(10*time.Minute).Unix(),
}).SignedString(private)
```

Bob exchanges that JWT with `POST /v1/ws-tickets` (`Authorization: Bearer ...`, response HTTP201), then consumes its `ticket` once within 30 seconds:

```go
conn, response, err := dialer.Dial(wsBase+"/v1/ws?ticket="+url.QueryEscape(ticket), nil)
// Check err and close response.Body on failure; do not log ticket-bearing URLs.
err = conn.WriteJSON(map[string]any{"type":"subscribe", "chat_id":"hello", "after":cursor})
```

Alice checks `/v1/capabilities`'s `server_key` against the independently supplied pin, decodes its padded base64 value and encrypts:

```go
envelope, err := cryptoenc.SealEnvelope(serverPublicKey, []byte("Hello, Bob!"),
    cryptoenc.Binding("hello", "alice", "alice-phone", "hello-1"))
body, err := json.Marshal(core.MessageInput{OperationID:"hello-1", Envelope:&envelope})
// POST body to /v1/chats/hello/messages with Alice's JWT. HTTP201 returns
// {"status":"accepted","message":{...}}: committed, not recipient delivery.
```

Bob's `message.created` event contains the current recipient-specific message projection in `data`. Decrypt `data.envelope` using the recipient binding, whose operation is the **server message ID**:

```go
plain, err := bobEngine.OpenEnvelope(*event.Data.Envelope,
    cryptoenc.Binding("hello", "bob", "bob-phone", event.MessageID))
```

Persist the processed cursor before `POST /v1/chats/hello/receipts` with `{"delivered":event.Seq,"read":0}`; this is a device delivery claim, not proof of human reading. Basic mode decrypts inside QGramm and rewraps per device; it is not protection against the container operator.

For production, retain the complete serialized send request and operation ID before the first attempt. Retry those **exact bytes**, without resealing: fresh HPKE randomness changes ciphertext and reusing an operation ID with changed content returns 409. The smoke deliberately fails on HTTP/transport errors instead of implementing a production retry policy. Deduplicate at-least-once events by `(chat_id,seq)` and message ID/revision, reconnect with a fresh ticket and a durable cursor, and resync history/current state after HTTP410 or `sync.error`. Implement JWT refresh, browser origin configuration and socket ping/pong handling for long-lived clients.
