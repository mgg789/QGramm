# Security and limitations

## Trust boundaries

TLS/WSS is mandatory except explicitly enabled loopback development. `trusted_proxy=true` trusts forwarded TLS status; isolate the container port behind your own authenticated HTTPS ingress. Do not expose an HTTP listener to untrusted callers who can forge proxy headers. Origin allowlists are defense in depth, not authentication.

Basic HPKE conceals message payloads from a TLS-terminating proxy. The container holds receiver/storage keys and can decrypt. HPKE static receiver-key compromise does not provide forward secrecy for retained envelopes. IDs, membership, sizes, timings, operations and references are metadata visible to the server/proxy. Bind server/device keys through your trusted backend; key-directory substitution breaks confidentiality.

Human-to-human MLS uses client-owned keys; the relay has no group secrets. MLS cryptographic and identity validation is also required on clients. A server can deny, delay or reorder delivery. A compromised external identity authority can introduce substituted identities. Do not advertise protection against an authority you also trust to bind participant keys.

AI is an intentional recipient inside the container. Its conversation plaintext reaches the selected provider and permitted tools. Keys, prompts, provider secrets and tool credentials must never appear in logs. The host/container administrator remains trusted for AI conversations.

## Persistence and keys

SQLite stores message bodies and event data encrypted under an independent AES-256-GCM master key. Routing metadata is not encrypted. File chunks are encrypted in separate records. Directory/database permissions are restrictive, but operators must protect volumes, backups and environment secrets.

MLS snapshots include signing/ratchet state, are encrypted, and must be saved atomically before emitting ciphertext/ACK. Restoring an old snapshot or running two instances over the same state can reuse counters. Single-instance deployment is required. Historical backups can retain secrets whose deletion would otherwise contribute to forward secrecy.

Master-key replacement is **not** an online rotation mechanism: replacing it without reencrypting existing records loses readability. HPKE-key replacement changes capabilities/key ID and rejects old in-flight envelopes; obtain the new authenticated key and retry under a new operation after confirming previous acceptance. Seamless keyring-based rotation and automated master re-encryption are not provided in this iteration.

## Authorization and abuse

Management bearer access is separate from client Ed25519 JWTs. JWTs validate issuer/audience/expiry/issued-at, device ownership and revocation. Device public keys are immutable. Members have explicit send permissions; a membership removal closes sockets and blocks future history/download requests.

Bounded HTTP concurrency, connection count, parser sizes, batch/file/chunk limits, upload quotas and queues reduce overload. They do not replace application/IP abuse controls at your ingress. Presence tracking, spam scoring and registration policy are outside the core.

Tool endpoints and methods are explicitly allowlisted. Redirect/DNS checks and pinned dialing protect egress; private endpoints require operator opt-in. Model output cannot authorize a new tool. Tool output remains untrusted application data; granting a destructive tool still grants its real side effects.

## Deletion

Global delete removes the active message payload and emits a tombstone. Author-only delete hides the projection only from that author. Neither mode erases recipients' downloaded copies. SQLite WAL, previous backups, SSD behavior and retained file copies can prevent forensic erasure; encrypted-volume/backup retention belongs to the operator. Do not promise remote deletion or secure physical destruction.

## Release evidence

The pure-Go MLS dependency has no independently confirmed cryptographic audit. Standard-vector, state-persistence and interoperability tests must be distinguished from an audit. Check `verification.md` before relying on production-readiness claims. No horizontal cluster or cross-instance websocket bus is provided.
