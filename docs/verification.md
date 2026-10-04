# Verification status — 2026-10-04

This is a pre-release core. Implementation, mocks, independent peers and live providers are separate evidence.

## Passed

| Check | Command / evidence |
|---|---|
| Minimal and full unit/integration/race | `go test -race ./...`; `go test -race -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_mcp,qg_http_tools ./...` |
| Vet | `go vet ./...` and the same full tag set |
| Build/runtime matrix | `sh scripts/build-matrix.sh`: 28 profiles, 7 invalid configurations; registry/routes/tables/files/dependency graphs checked |
| Protocol/security integration | JWT issuer/audience, device revocation, ACL, recipient HPKE, durable dedup/restart, replay/cursors, receipts and one-use WS tickets |
| Fault paths | SQLite `SQLITE_FULL` rollback/recovery, exact batch retry after lost response, reconnect, expired replay, bounded capacity, corrupt/missing file chunks, quotas; AI late-reply invalidation after deletion |
| Fuzzing | Five-second runs: `FuzzStrictJSON`, `FuzzEncryptedFrames`, `FuzzMLSKeyPackage`, two workers; no crashes. Short smoke fuzz, not exhaustive fuzzing |
| HPKE | CFRG RFC9180 suite32/1/1 vector, 257 generations; retired keys, wrong AAD and key removal tests |
| MLS independent interop | Pinned mls-go RFC-vector package tests; OpenMLS suite1 Welcome/application/public commit matrix. [Raw evidence](../internal/cryptoenc/testdata/interop-evidence.json) |
| Production MLS/AI restart | `sh tools/mls-interop/run.sh`, `-race`: actual adapter snapshot/replay/counter/epoch and Core AI HTTP/SQLite restart against OpenMLS; provider is HTTPS mock |
| Calls direct/TURN | `python3 tools/webrtc/run.py`, exit0: real Opus/VP8 frames between Pion peers, direct and forced coturn relay/relay. [Scope](webrtc-verification.md) |
| Native browser partial | Chromium152 Linuxarm64: HPKE, decoded/rendered video, substituted fingerprint rejected with zero media. Audio incomplete; runner exit1. [Details](browser-webrtc-verification.md) |
| Live provider | DeepSeek `deepseek-flash`, actual OpenAI-compatible module: encrypted input, job dedup, real response and recipient decryption. [Evidence](benchmarks/live-provider.json) |
| Load | 10,000 WS, sustained100/s and burst1000/s on Docker Linux4CPU/8GiB limit; accepted=delivered=history. Separate group and file microbenchmark. [Results](benchmark.md) |
| Docker | Minimal arm64 build, UID10001, health/restart, retained volume, exclusive0600 offline backup; third-party license bundle included |
| Architectures | Static Linux amd64/arm64 binaries cross-built; arm64 container run. amd64 native runtime not checked |
| Contracts/licenses | Generator covers39 routes; OpenAPI3.1/WS JSONSchema validation; `go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt -check` |
| Readiness | Independent inspector reviewed implementation/fixes before commits; no blocking findings |

Development host: macOS arm64 Go1.26.4; Docker Go1.26.8. Image/source/workload contours and sampled resources accompany benchmark JSON. Test fixtures create ephemeral synthetic keys; real credentials are never committed or printed.

## Remaining gates and unsupported profiles

- Browser decoded audio is not confirmed; macOS Chrome ICE stayed checking. Browser TURN, Safari/Firefox, public NAT and client MLS fingerprint authentication remain unverified.
- OpenAI-hosted/Anthropic-hosted APIs were not called. DeepSeek validates OpenAI compatibility, not both vendors. Real tools and live provider+independent MLS were not exercised.
- Mixed public/private MLS handshake matrix failed; only documented public member commits are supported. External signed roster authority and client cryptographic checks are mandatory.
- No independently qualified Linux local-SSD reference host, long soak, exact process peaks, TLS performance or concurrent large-file load. Sizing remains workload-dependent.
- No independent cryptographic audit, production deployment, horizontal cluster or guarantee of deleting downloaded/provider/backed-up copies.

These gates prevent claiming the entire requested production release has passed acceptance. Functioning modules and reproducible checks expose their current limitations.
