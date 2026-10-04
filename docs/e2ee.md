# MLS E2EE relay: contract and verification boundaries

[Русская версия](e2ee.ru.md).

Build tag `qg_e2ee` enables the module. Only RFC 9420 suite 1 is supported:
X25519 / HKDF-SHA256 / AES-128-GCM / Ed25519, implemented by
`github.com/thomas-vilte/mls-go v1.3.0`. Basic HPKE uses the separate RFC 9180
suite 32/1/1 and does not replace MLS.

The relay receives no human MLS private signing keys or epoch secrets. Wire
messages, KeyPackages, Welcome and commits use AES-256-GCM encryption at rest.
Public GroupContext, roster, epoch and account/device ACL are metadata.

## Device credentials and KeyPackages

An external backend first registers immutable X25519 public and Ed25519
`signing_key` values through management API, using standard padded Base64.
MLS must use that registered Ed25519 key. BasicCredential identity is the raw
UTF-8 `device_id`; display names and `user_id` are not substitutes.

`POST /v1/mls/keypackages` accepts `{"key_package":"<base64 naked KeyPackage>"}`.
The library validates suite/version, BasicCredential, lifetime, capabilities,
leaf signature and KeyPackage signature. Credential device ID and signing key
must match the authenticated registered device. SHA-256 IDs prevent duplicate
uploads; at most 100 unconsumed packages per device are accepted. Clients must
extract the inner KeyPackage object from any MLSMessage(key_package) wrapper.

`POST /v1/chats/{chat}/mls/keypackages/{device}/claim` atomically consumes one
package for this chat and returns its wire. The requester requires active
membership/can_send; the target account/device must be active in this chat.
A package cannot be claimed twice. Welcome creation marks it `used=1` in the
same transaction; it cannot be reused even in that chat. Welcome Secrets must
reference the claimed package using RFC MakeKeyPackageRef.

## Message binding

The send API's `mls` field is standard Base64 of a complete RFC MLSMessage with
PrivateMessage application content. Public application, plaintext, Welcome,
commit and malformed/noncanonical wire are rejected in this endpoint. The relay
checks group_id/epoch against the chat mapping, the current device roster, and
exact `authenticated_data == cryptoenc.Binding(chat,user,device,operation_id)`.
Pending transitions block sends. The transaction rechecks epoch/barrier and
account/device ACL; the module rechecks device roster membership.

Binding is canonical UTF-8 JSON with Go escaping and field order
`version,chat,user,device,operation`; version is 1. Recipients must use identical
bytes. Without sender-data keys the relay cannot decrypt the sender leaf or
validate the PrivateMessage signature/AEAD. Recipients must perform library MLS
authentication, recover the actual sender, and compare binding with the
account/device and external metadata. Relay acceptance authorizes wire delivery;
it does not prove successful MLS authentication.

## Initialization and epoch transitions

Management creation of an E2EE chat sets `pending_rekey=true`. An authenticated
participant creates a real MLS group and supplies public state. The external
management backend validates MLS state and the permitted roster.
`POST /management/v1/chats/{chat}/mls/confirm` initializes the mapping with
`previous_epoch=-1`, empty `transition_id`, and the signed proof below.
Internal `InitMLSRelayTx` initializes AI chats only after actual library
CreateGroup/Invite and account/device checks in the management transaction.

`POST /v1/chats/{chat}/mls/commits` accepts:

```json
{"commit":"<base64 public member commit>","welcomes":[{"device_id":"recipient","key_package_id":"claimed SHA256 ID","welcome":"<base64 MLSMessage Welcome>"}]}
```

The relay checks current group/epoch, authenticated account/device, current
roster leaf index, and RFC FramedContentTBS signature using the registered
Ed25519 public key and current GroupContext. External/nonmember/private commits
are unsupported. Commit and targeted Welcome are durably encrypted at rest;
a pending barrier is set without advancing epoch. A second candidate before
confirmation is rejected. AI job hooks can block transitions transactionally.

`GET /v1/chats/{chat}/mls` returns public context/epoch/roster/barrier.
`GET /v1/chats/{chat}/mls/inbox` returns up to 100 Welcome messages targeted to the
authenticated device and up to 100 public commits to current-roster devices.
Reads are nondestructive: clients must deduplicate IDs and persist processed
participant state and cursors. Pagination accepts
`?after_welcome=<id>&after_commit=<id>` and returns `next_welcome`/`next_commit`.
Cursors are scoped to chat/device; unknown cursors are rejected. Control records
are not automatically deleted; retention is separate storage policy.

Management confirmation requires `transition_id`, `group_id`, `group_context`,
`previous_epoch`, `epoch`, `signer_device`, `signature`, `roster`. Epoch advances
by exactly one and must match old mapping/epoch/candidate. The signer must be
the authenticated participant who signed the commit and must remain in the new
roster. Every roster device must be active and allowed by account ACL. The roster
covers every active account without duplicate devices or leaf indices.

The signer Ed25519-signs exact `MLSRosterProof(...)` bytes: canonical JSON order
`version,chat_id,group_id,previous_epoch,epoch,commit_hash,context_hash,roster`.
Version is 1; group_id is Base64; hashes are lowercase SHA-256 hex. Roster entries
are sorted by device_id with fields `device_id,leaf_index`; escaping follows Go.
commit_hash covers complete candidate wire (empty at initialization); context_hash
covers the new serialized GroupContext. Both authority proof and management
authentication are required. Roster/context, AI snapshot, epoch/barrier and event
are confirmed in one DB transaction.

## External authority obligation

Without group secrets the relay **cannot** validate membership_tag,
confirmation_tag, TreeKEM path decryption/parent hashes, or correspondence between
the new authenticated ratchet tree and submitted roster/context. Public signature
covers neither confirmation_tag nor membership_tag. GroupContext is structurally
checked and bound to proof, but its cryptographic correspondence to the commit
is not proved by the relay. This is not full server MLS commit validation.

Before signing, the external participant must perform complete library
ProcessCommit validation: membership MAC, signature, epoch, TreeKEM/path,
confirmation MAC, authenticated new tree and device credentials/signing keys.
Management authority must verify that result and roster policy. It must not
confirm an arbitrary submitted roster solely because the HTTP request is
authenticated. A malicious participant signature does not replace external
validation. Confirmation is an explicit trusted authority boundary.

Removal/revocation blocks send/read through account/device ACL and blocks new
messages behind the rekey barrier until confirmation. Old secrets/plaintext
already known to former participants cannot be revoked by the relay.

## Checks and release gates

Package commands:
`go test -race -tags qg_e2ee ./internal/cryptoenc ./internal/modules` and
`go vet -tags qg_e2ee ./internal/cryptoenc ./internal/modules`.
They cover real suite1 Welcome/application/commits, persisted sender/receiver
counters, replay/binding, device admission, once-only claims, signed roster,
epoch barrier, stale confirmation, tampered signatures and targeted Welcome.
Encrypted snapshot version 2 persists a per-epoch replay ledger capped at 65,536
entries; reaching the cap fails closed and requires rekey. Version 1 snapshots
are refused because they lack durable replay protection.

Upstream published vectors passed:

```sh
go test ./ciphersuite ./group ./framing ./treesync ./schedule ./secrettree ./interop -run 'Interop|Vector|Passive' -count=1
```

Vector coverage is distinct from independent runtime verification. A build/config
flag or user-controlled boolean cannot satisfy a release gate.

### Independent library matrix

2026-10-04: unmodified OpenMLS revision
`4f407c45857d177e7b5933d32795307b4f14b196` against mls-go v1.3.0 revision
`e7ef547b6bab2a63a83aaed6fcce949b71a11139`, Docker project qgramm-mls-interop,
Go 1.26; Rust exists only in test containers. The obsolete supplied OpenMLS
wire-format patch was not applied. In the pinned upstream clone:

```sh
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -fail-fast -config /configs/welcome_join.json
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -fail-fast -config /configs/application.json
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -public -fail-fast -config /configs/commit.json
```

All exited 0: Welcome 32 assignments/16 cross-implementation, application 24/12,
public commits 436/400, errors=0. Application covers both directions, ordering
and epoch changes. Public commits cover Add, Remove, Update, empty,
group_context_extensions, PSK and combined cases. Sanitized revisions/counters
are in `internal/cryptoenc/testdata/interop-evidence.json`.

Default commit matrix without `-public` exited 1: eight scenarios failed due to
OpenMLS pure wire-format policy rejecting mixed public/private proposals. This
is a real broader matrix FAIL. The relay profile supports public commits;
no OpenMLS patch or incompatible runtime fallback is enabled.

### Independent production adapter and AI lifecycle

2026-10-04: `docker compose -p qgramm-adapter-gate -f tools/mls-interop/compose.yml run --rm runner`
exited 0 with Go `-race -mod=readonly -tags 'qg_e2ee qg_openai'`. Both passed:
`TestProductionAdapterOpenMLSRestartReplayAndEpoch` and
`TestProductionAIHTTPWithIndependentOpenMLSAndDBRestart`.

Production MLSParticipant passed encrypted disk snapshot/restore, independently
generated OpenMLS human decrypt, wrong-binding/replay/appended-byte rejection,
two persisted sender generations, OpenMLS reply decrypt, public Add/new epoch,
and old-epoch rejection. Actual AI handlers passed management admission, client
send, durable jobs/history and two human→AI→human exchanges, closing/reopening
Core on the same SQLite DB between them. The human is a separate unmodified
OpenMLS process. Restored `ai_chats.state` rejects the first human wire replay.

Provider is an explicit local HTTPS mock with temporary test CA in an isolated
Docker network. This checks provider contract/TLS and the normal egress
validator, **not live OpenAI/Anthropic acceptance**. Production network guards
are unchanged. Private keys, snapshots and JWTs are not logged. Reproduce with
`sh tools/mls-interop/run.sh`; source/base-image hashes and nested test dependency
sums are pinned. Rust/gRPC are excluded from deployment. See
[tooling instructions](../tools/mls-interop/README.md).

The public-commit interoperability profile has recorded PASS evidence.
Mixed/private handshakes, live provider acceptance and operational external
authority validation remain separate limitations, not implied by these results.

Sources: [RFC 9420](https://www.rfc-editor.org/rfc/rfc9420),
[MLS WG interoperability](https://github.com/mlswg/mls-implementations),
[mls-go v1.3.0](https://github.com/thomas-vilte/mls-go/tree/v1.3.0),
[OpenMLS](https://github.com/openmls/openmls).
