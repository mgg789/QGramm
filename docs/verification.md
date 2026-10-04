# Verification status

Current implementation is a pre-release. Record exact commands and environment here; do not equate source implementation with acceptance.

## Verified in development

- Go 1.26.4 on macOS/Apple Silicon: minimal tests and race tests for core/config/HPKE.
- HTTP integration: issuer rejection, device revocation, recipient re-encryption, operation deduplication, offline replay, persistent restart, expired cursors and single-use WebSocket tickets.
- RFC9180 CFRG HPKE vector: matching suite32/1/1 ciphertexts across 257 generations.
- File/call module race tests: resumable integrity/quota/ownership/TTL and authenticated HTTP call state, encrypted SDP/ICE, operation conflicts, epoch barriers and revoked devices.
- MLS adapter: suite1, Welcome, snapshot/restart counters, replay/tamper and same-library framing interoperability. This alone is not independent interop.

## Required before full release

- Full build matrix/race/vet and independent readiness review.
- Independent MLS implementation interoperability and official RFC9420 vector coverage.
- Docker runtime health/restart/backup; both target architectures build.
- Browser WebRTC direct/TURN calls with peer fingerprint verification.
- Live OpenAI/Anthropic API acceptance; mocks are separate evidence.
- Linux 4vCPU/8GiB local-SSD benchmark: 10,000 sockets; sustained100/s and burst1000/s; p95, memory/CPU, event loss and replay results.

No independent cryptographic audit, production deployment, cluster acceptance or calibrated resource guarantee is asserted.
