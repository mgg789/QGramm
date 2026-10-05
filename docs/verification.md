# Verification status — 2026-10-05

This is a pre-release core. Implementation, mocks, independent peers and live providers are separate evidence.

## Passed

| Check | Command / evidence |
|---|---|
| Minimal and full unit/integration/race | `go test -race ./...`; `go test -race -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_mcp,qg_http_tools ./...` |
| Vet | `go vet ./...` and the same full tag set |
| Build/runtime matrix | `sh scripts/build-matrix.sh`: 35 build/runtime profiles and 9 invalid configurations passed with preset configuration (`148de04`), including five presets and two explicit-override profiles; registry/routes/tables/files/dependency graphs checked. Previous 30-profile evidence includes the removed Redis experiment. |
| Configuration diagnostics | `init`, `validate`, `explain`; preset/explicit-false/zero precedence, strict unknown keys, AI model selection, declared versus derived sources, exclusive 0600 output, secret-value exclusion. Minimal/full race and the disposable integration example passed. [EN guide](configuration.md), [RU reference](reference.ru.md). |
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
| Commit/GC/SQL campaign | 8 primary before/after runs, 63,908 offered = accepted = events = history, no errors/backpressure; two instrumented 2k diagnostics excluded from latency tables. Redis absent in both binaries. Lower read/alloc/commit work, mixed tail results preserved. [Evidence](performance-sql-gc.md) |
| Replay/WAL follow-up | One-query history, two-query nonempty replay, single-event range path, fresh ACL/key checks and per-batch HPKE parsing; optional PASSIVE worker retains FULL/automatic fallback. Natural-GC diagnostics and initial SQL regression preserved; final same-load comparison uses two repeats per profile. [Evidence](performance-tail.md) |
| Three isolated optimizations | 18 primary runs, 143,775 offered = accepted = events = history, zero errors/backpressure; selected ready-batch commit + minimal ACK, idle reuse reverted and single-message regressions preserved. Same-load controls against dev2ce7077; main qualification deferred. [Evidence](performance-three.md) |
| Expanded scenario evidence | Shared active-chat/group/reconnect matrix with exact per-ID plaintext/order/history checks, 10k idle attempts, 300-second loads and four parallel-file runs. Separate NATS FILE/MEMORY consumer control; original Centrifugo reply failure, paginated-history OOM and QGramm setup failure retained. Strict admission SLO is separate from accepted-message integrity. [Method and all attempts](scenario-benchmark.md) |
| Retention/fan-out follow-up | Nine Q-only primary attempts plus three diagnostics; indexed bounded cleanup retained, merged replay query reverted after tail regression. Final soak: zero server rejections,149859 accepted/141 generator skips; all primary accepted records/live deliveries verified. Group CPU/latency improvement is not established. [All attempts and tradeoffs](performance-retention-fanout.md) |
| Full batch read follow-up | 6 primary runs,47,890 offered=accepted=events=history, zero errors/backpressure. Grouped full projection and preliminary snapshots tested separately; no convincing gain, neither retained, conditional combination not run. Archived source patch and provenance. [Evidence](performance-batch-reads.md) |
| Docker | Minimal arm64 build, UID10001, health/restart, retained volume, exclusive0600 offline backup; third-party license bundle included |
| Architectures | Static Linux amd64/arm64 binaries cross-built; arm64 container run. amd64 native runtime not checked |
| Contracts/licenses | Generator covers39 routes; OpenAPI3.1/WS JSONSchema validation; `go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check` |
| Readiness | Independent inspector reviewed implementation/fixes before commits; no blocking findings |

Development host: macOS arm64 Go1.26.4; QGramm Docker Go1.26.8. Competitor build/toolchain provenance is recorded per campaign. Image/source/workload contours and sampled resources accompany benchmark JSON. Test fixtures create ephemeral synthetic keys; real credentials are never committed or printed.

Provider acceptance uses live DeepSeek for the OpenAI-compatible API and
integration fixtures for the Anthropic Messages contract. Separate vendor-hosted
live calls are not release gates in the selected acceptance policy.

## Stage-1 named AI evidence

The stage-1 named-agent checks currently provide local evidence only: SQLite
transaction and restart paths, concurrent task claims, multiple-agent targeting,
BASIC group ACL and source deletion/revocation guards, encrypted progress/cancel
state where `qg_ai_streaming` is enabled, and provider calls through local
mocked HTTP fixtures. This does not establish interoperability with a hosted
OpenAI or Anthropic service, and no new live-provider result is claimed here.

`sh scripts/build-matrix.sh` passed 38 valid build/runtime profiles and 10
invalid profiles, including the named network configuration and independent
streaming exclusion. The matrix checks routes, tables, selected files and
production dependency graphs. The existing 35-profile/9-invalid evidence above
is a separate pre-stage-1 result.

`go test -race ./...`, the complete 14-tag `go test -race ./...`, full-profile
`go vet ./...`, and `go run -race ./examples/basic` passed. The named two-bot
integration uses an actual local HTTP/SSE server without authentication;
it checks encrypted input/output, explicit group targeting, no invocation loop,
streaming and exact retry without a second provider call. MLS progress tests
check receiver decryption of chunks and final output using the durable sender
ratchet. These are local runtime/contract checks, not a new load measurement.

## Deferred acceptance and unsupported profiles

- Browser PCM and TURN pass in Linux Chromium. macOS Chrome ICE, Safari/Firefox, public NAT and combined client MLS fingerprint authentication remain separate client/platform contours.
- Operator tool endpoints were not supplied; HTTP/MCP contracts and permission/egress boundaries have local integration coverage. Live provider tool invocation against external operator services remains a deployment check.
- Mixed public/private MLS handshake matrix failed; only documented public member commits are supported. External signed roster authority and client cryptographic checks are mandatory.
- No independently qualified Linux local-SSD reference host, hours-long soak, exact process peaks, TLS performance or combined messaging/large-file load. Five-minute Docker runs and concurrent 16/64 MiB attachment flows are published with admission/setup/history failures in the scenario report. Sizing remains workload-dependent.
- No independent cryptographic audit, production deployment, horizontal cluster or guarantee of deleting downloaded/provider/backed-up copies.

These gates prevent claiming the entire requested production release has passed acceptance. Functioning modules and reproducible checks expose their current limitations.

## Stage-2 policy evidence

The optional [AI policy module](ai-policy.md) introduces signed grants, sealed resumable continuations, encrypted usage/budget accounts and pre-egress events. The generated contract now covers 51 exact source routes and both policy event shapes. Documentation checks verify local links, UTF-8 and JSON syntax; these are contract-maintenance checks, not proof of runtime or external-provider acceptance. Stage-2 runtime evidence must be read with its exact tested profile and commands. Prior live DeepSeek evidence remains a separate result.

Stage-2 checks on 2026-10-05 passed:

```sh
go test -race ./... -timeout=120s
go test -race -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_ai_streaming,qg_ai_policy,qg_mcp,qg_http_tools ./... -timeout=180s
go vet -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_ai_streaming,qg_ai_policy,qg_mcp,qg_http_tools ./...
sh scripts/build-matrix.sh
go test -tags qg_ai_policy,qg_openai,qg_http_tools ./internal/modules -run '^$' -fuzz '^FuzzAIPolicyCanonicalArguments$' -fuzztime=5s -parallel=2
go run -race ./examples/basic
python3 scripts/generate-contract.py
python3 scripts/check-docs.py
go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check
git diff --check
```

The matrix passed 41 valid build/runtime selections and 12 invalid configurations, including policy/stream/provider exclusion. The fuzz run completed 66,560 executions. Local real HTTP handlers verified a provider → approved tool → provider sequence, committed notice before each request, encrypted checkpoint, recreation of the policy runtime before resume, one tool invocation and one final reply. Estimated cost settled to 34 microunits in that fixture. Other regressions cover provider gating, legacy MLS pause/resume and final decrypt, TTL queue release, revocation, concurrent budget admission, rollback across all three scopes, unknown usage, duplicate settlement and restart with an uncertain dispatched effect. Independent inspection reported READY after corrections.

These checks use local test providers/HTTP servers and persisted SQLite fixtures; they are not new live API calls, an OS-process kill test, a performance benchmark or independent cryptographic audit. Stage-3 evidence follows separately.

## Stage-3 storage and external endpoint evidence

On 2026-10-05, the following checks passed:

```sh
go test -race ./...
go test -race -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_ai_streaming,qg_ai_policy,qg_ai_storage,qg_ai_endpoint,qg_mcp,qg_http_tools ./...
go vet -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_ai_streaming,qg_ai_policy,qg_ai_storage,qg_ai_endpoint,qg_mcp,qg_http_tools ./...
go test -race -tags qg_ai_endpoint,qg_e2ee ./internal/microsafer ./cmd/qgramm-micro-safer
sh scripts/build-matrix.sh
go test -tags qg_ai_storage ./internal/aivault -run '^$' -fuzz '^FuzzStorageGrant$' -fuzztime=5s -parallel=2
go test -tags qg_ai_endpoint,qg_e2ee ./internal/microsafer -run '^$' -fuzz '^FuzzRPCJSON$' -fuzztime=5s -parallel=2
go run -race ./examples/basic
python3 scripts/generate-contract.py
python3 scripts/check-docs.py
go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check
git diff --check
```

The matrix checks 47 Core profiles, 16 independently selected micro-safer
adapter combinations and 14 invalid configurations. It verifies selected
source files and production dependencies, with neither micro-safer in Core nor
Core in micro-safer. Both base and full `qgramm-build -target micro-safer`
builds passed. The generated contract covers 64 exact source HTTP routes;
documentation checks passed 396 local links, UTF-8 and JSON syntax. Short fuzz
smokes completed 188,942 storage-grant and 102,637 RPC JSON executions without
crashes; these are not exhaustive fuzzing.

Integrated-vault tests use actual SQLite and HTTP management/provider fixtures:
shared/user/bot scopes, cryptographically authenticated routing headers,
encrypted content/files/vectors/edges, tampering, cross-resource denial,
bounded search/BFS, grant expiry/revocation and approval in either order.
Retrieval cannot release context to another destination or scope/owner label,
and successful dialogue context is cleared after vault use. The provider
sequence is local test HTTP, not a new live vendor call.

`TestAIEndpointCoreMicroSaferTransport` uses the actual Core HTTP handler and
an actual MLS pair. It checks encrypted request/response delivery, valid Core
operation IDs, exact retry, reconstructed endpoint state without a duplicate
effect, pending-rekey and revoked access. Separate endpoint tests cover
encrypted streaming/outbox ordering, uncertain effects, independent adapter
exclusion, redirects/credential separation, bounded requests and HTTP
deadlines. Storage denial followed by valid retrieval is also exercised through
the encrypted MLS inbox, with a decryptable final response. The administrative
storage MCP and external MCP adapter are different trust boundaries.

The sidecar remains a beta pinned two-device profile: offline rejoin after
epoch/membership changes, operator-managed replacement after retained-record
caps, text OpenAI-compatible SSE and limited MCP operations. No live Ollama,
vLLM, MLX or llama.cpp runtime acceptance, automatic embeddings/ANN, native
agent framework, new load run, OS-process kill test or independent audit is
claimed. [EN contract](ai-stage3.md), [RU contract](ai-stage3.ru.md).
