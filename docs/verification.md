# Verification status — 2026-10-04

This is a pre-release core. Implementation, mocks, independent peers and live providers are separate evidence.

## Passed

| Check | Command / evidence |
|---|---|
| Minimal and full unit/integration/race | `go test -race ./...`; `go test -race -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_mcp,qg_http_tools ./...` |
| Vet | `go vet ./...` and the same full tag set |
| Build/runtime matrix | Current command: `sh scripts/build-matrix.sh` (13 features, 28 profiles and 7 invalid configurations); previous 30-profile result included the now-removed Redis experiment. Registry/routes/tables/files/dependency graphs checked in that recorded run. |
| Protocol/security integration | JWT issuer/audience, device revocation, ACL, recipient HPKE, durable dedup/restart, replay/cursors, receipts and one-use WS tickets |
| Fault paths | SQLite `SQLITE_FULL` rollback/recovery, exact batch retry after lost response, reconnect, expired replay, bounded capacity, corrupt/missing file chunks, quotas; AI late-reply invalidation after deletion |
| Durable grouped writer | Opportunistic groups up to16 queued jobs/8MiB with SQLite FULL, per-job savepoints and no wait timer. Tests cover no ACK/fan-out before commit, failed commit, real SQLITE_FULL rollback/retry, hook failure, cancellation, dedup and sequence order; minimal/full race suites pass. |
| Fuzzing | Five-second runs: `FuzzStrictJSON`, `FuzzEncryptedFrames`, `FuzzMLSKeyPackage`, two workers; no crashes. Short smoke fuzz, not exhaustive fuzzing |
| HPKE | CFRG RFC9180 suite32/1/1 vector, 257 generations; retired keys, wrong AAD and key removal tests |
| MLS independent interop | Pinned mls-go RFC-vector package tests; OpenMLS suite1 Welcome/application/public commit matrix. [Raw evidence](../internal/cryptoenc/testdata/interop-evidence.json) |
| Production MLS/AI restart | `sh tools/mls-interop/run.sh`, `-race`: actual adapter snapshot/replay/counter/epoch and Core AI HTTP/SQLite restart against OpenMLS; provider is HTTPS mock |
| Calls direct/TURN | `python3 tools/webrtc/run.py`, exit0: real Opus/VP8 frames between Pion peers, direct and forced coturn relay/relay. [Scope](webrtc-verification.md) |
| Native browser direct/TURN | Chromium152 Linuxarm64: decoded PCM and rendered video, selected relay/relay TURN pairs, HPKE signaling, substituted fingerprint rejected with zero media; runner exit0. [Details](browser-webrtc-verification.md) |
| OpenAI-compatible API | Verified against live DeepSeek `deepseek-flash`: Chat Completions JSON API, encrypted input, job dedup, real response and recipient decryption. The core uses Go HTTP transport. [Evidence](benchmarks/live-provider.json) |
| Live provider + independent MLS | DeepSeek `deepseek-flash` from Docker, independent OpenMLS input/response AAD, actual Core SQLite restart and replay rejection; two succeeded jobs and provider audit records. [Evidence](benchmarks/live-mls-provider.json) |
| Archived Redis experiment (removed) | Actual pinned Redis7.2.14: private Unix socket, TCP/AOF/snapshots disabled, healthy Pub/Sub, SIGKILL → local fallback, bounded queue, reap/cleanup; real-process race tests (no skips), minimal and Redis container smoke. [Historical lifecycle and fixture commands](redis.md) |
| Runnable integration | `go run -race ./examples/basic`: Ed25519, management provisioning, ticket WebSocket, pinned-key HPKE send/decrypt, identical retry and delivery receipt |
| Anthropic adapter | Messages/tool-use/tool-result contracts covered by integration tests with a test provider |
| Load | 10,000 WS, sustained100/s and burst1000/s on Docker Linux4CPU/8GiB limit; accepted=delivered=history. Published p95/p99 rerun, hardware/provenance and reproduction tooling; separate group and file microbenchmark. [Results](benchmark.md) |
| Alternative baselines | Real NATS JetStream default/always fsync and Centrifugo, sequential 10,000 WebSocket runs, p95/p99 and sampled CPU/RAM. [Scope and results](comparison-benchmark.md) |
| Performance changes | Two original/full/compact-receipt runs and two NATS strict-fsync/Centrifugo runs; phase-labelled CPU/RAM, p95/p99, durable history checks, separate private CPU/heap/SQL-wait profiles. Full-response burst rejections and variation are retained. [Dynamics and limits](performance-dynamics.md) |
| Further three changes + Redis | 12 uninstrumented runs: baseline/native/embedded Redis × standard/all-subscribed × two repeats; same generator and quota, full responses and SQLite FULL commits. Equal accepted/event/history counts, not per-ID or persistent device ACK verification. Redis includes both processes; one overlapping baseline excluded and replaced. [Method and evidence](performance-iteration.md) |
| Docker | Minimal arm64 build, UID10001, health/restart, retained volume, exclusive0600 offline backup; third-party license bundle included |
| Architectures | Static Linux amd64/arm64 binaries cross-built; arm64 container run. amd64 native runtime not checked |
| Contracts/licenses | Generator covers39 routes; OpenAPI3.1/WS JSONSchema validation; `go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check` |
| Readiness | Independent inspector reviewed implementation/fixes before commits; no blocking findings |

Development host: macOS arm64 Go1.26.4; Docker Go1.26.8. Image/source/workload contours and sampled resources accompany benchmark JSON. Test fixtures create ephemeral synthetic keys; real credentials are never committed or printed.

Provider acceptance uses live DeepSeek for the OpenAI-compatible API and
integration fixtures for the Anthropic Messages contract. Separate vendor-hosted
live calls are not release gates in the selected acceptance policy.

## Deferred acceptance and unsupported profiles

- Browser PCM and TURN pass in Linux Chromium. macOS Chrome ICE, Safari/Firefox, public NAT and combined client MLS fingerprint authentication remain separate client/platform contours.
- Operator tool endpoints were not supplied; HTTP/MCP contracts and permission/egress boundaries have local integration coverage. Live provider tool invocation against external operator services remains a deployment check.
- Mixed public/private MLS handshake matrix failed; only documented public member commits are supported. External signed roster authority and client cryptographic checks are mandatory.
- No independently qualified Linux local-SSD reference host, long soak, exact process peaks, TLS performance or concurrent large-file load. Sizing remains workload-dependent.
- No independent cryptographic audit, production deployment, horizontal cluster or guarantee of deleting downloaded/provider/backed-up copies.

These gates prevent claiming the entire requested production release has passed acceptance. Functioning modules and reproducible checks expose their current limitations.
