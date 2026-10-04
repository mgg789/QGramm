# Независимые MLS runtime gates

Из корня QGramm: `sh tools/mls-interop/run.sh`. Нужны Docker Engine/Compose,
доступ к pinned upstream GitHub source и Go/Rust package registries. Скрипт
создаёт отдельный compose project, затем удаляет только его containers/network.
System Go/Rust не устанавливаются. Application runtime не получает Rust/gRPC.

Проверяются реальные production `cryptoenc.MLSParticipant` и AI HTTP handlers
против отдельного OpenMLS процесса с suite1, Ed25519/X25519/AES128GCM:

- Welcome admission, public Add commit и следующий epoch.
- Независимые application сообщения в обоих направлениях, exact AAD.
- Durable encrypted participant snapshot/reload, sender generation counters,
  отказ replay, appended-byte wire, wrong binding и stale epoch.
- Management admission AI, human HTTP send, durable queue/job/history,
  OpenMLS decrypt AI answer; повтор после Core restart на той же SQLite DB.
- Replay ledger из настоящего persisted AI state после restart.

AI provider здесь — явно local HTTPS mock. Проверяется application/provider
contract, TLS и штатный egress validator, но не живой OpenAI или Anthropic API.
Test-generated CA trusted только runner process через `SSL_CERT_FILE`.
Изолированная Docker сеть использует `11.99.0.0/24`: runner `.2`, OpenMLS `.3`.
Ни host routes, ни production egress guard не изменяются. Этот subnet должен
быть свободен; одновременно запускается один такой fixture project.

Все test credentials/keys создаются заново, raw wire/private snapshots/JWTs
не выводятся. gRPC возвращает `MLSMessage(key_package)`; test helper извлекает
канонический inner KeyPackage официальным MLS parser перед QGramm admission.
Runtime contract не меняется.

Pinned sources:

- [mls-go v1.3.0](https://github.com/thomas-vilte/mls-go/tree/e7ef547b6bab2a63a83aaed6fcce949b71a11139),
  commit `e7ef547b6bab2a63a83aaed6fcce949b71a11139`.
- [OpenMLS](https://github.com/openmls/openmls/tree/4f407c45857d177e7b5933d32795307b4f14b196),
  commit `4f407c45857d177e7b5933d32795307b4f14b196`, unmodified protocol/wire policy.
- Go/Rust/Debian base image content digests зафиксированы в Dockerfiles.
- `Cargo.lock` фиксирует Rust transitive dependencies; build использует
  `cargo build --release --locked`. Это lock, сгенерированный при проверке
  указанного upstream revision; implementation source остаётся unmodified.
- Nested `go.mod`/`go.sum` содержат test-only protobuf/gRPC dependencies;
  `-mod=readonly` запрещает изменение модулей при запуске.

Upstream broader default mixed public/private handshake commit matrix известен
как FAIL против pure OpenMLS wire policy. Relay профиль поддерживает public
commits; tooling не patch OpenMLS и не включает incompatible runtime fallback.
Результаты library matrix и ограничения authority описаны в `docs/e2ee.md`.
Passing these fixtures не доказывает полный RFC coverage или external roster
authority correctness; live provider acceptance проверяется отдельно.

## Third-party licenses

OpenMLS: MIT, Copyright (c) 2020 OpenMLS Authors; [pinned license](https://github.com/openmls/openmls/blob/4f407c45857d177e7b5933d32795307b4f14b196/LICENSE).
mls-go: MIT, Copyright (c) 2026 Thomas Vilte; [pinned license](https://github.com/thomas-vilte/mls-go/blob/e7ef547b6bab2a63a83aaed6fcce949b71a11139/LICENSE).
Docker builds clone these repositories including their upstream license files;
no upstream implementation/generated protobuf source is copied into QGramm.
The OpenMLS runtime test image includes its upstream LICENSE.
Go dependencies remain module-cache packages with their upstream licenses;
container package distributions carry their own package copyright notices.
