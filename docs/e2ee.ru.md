# MLS E2EE relay: контракт и границы проверки

Модуль включается только build tag `qg_e2ee`. Единственный поддерживаемый suite —
RFC 9420 suite 1: X25519 / HKDF-SHA256 / AES-128-GCM / Ed25519, библиотека
`github.com/thomas-vilte/mls-go v1.3.0`. HPKE envelope basic режима — отдельный
RFC 9180 suite 32/1/1, не замена MLS.

Human MLS secrets, private signing keys и epoch secrets не передаются relay.
Wire messages, KeyPackages, Welcome и commits хранятся зашифрованными AES-256-GCM
at rest. Public GroupContext, device roster, epoch и account/device ACL — metadata.

## Удостоверение устройств и KeyPackages

Внешний backend сначала регистрирует устройство через management API: immutable
X25519 public key и Ed25519 `signing_key` (standard padded Base64). Для MLS
используется именно этот Ed25519 signing key. Credential identity — raw UTF-8
`device_id`; display-name и `user_id` вместо него не принимаются.

`POST /v1/mls/keypackages`: `{"key_package":"<base64 naked KeyPackage>"}`.
Relay проверяет suite, version, BasicCredential, lifetime, capability/leaf
signature и KeyPackage signature библиотекой; credential device ID и signing
public key должны совпасть с токеном и зарегистрированным устройством. Один
KeyPackage имеет SHA-256 ID и не загружается повторно. Не более 100 ещё не
потреблённых KeyPackages на устройство.

`POST /v1/chats/{chat}/mls/keypackages/{device}/claim` атомарно помечает один
KeyPackage потреблённым для этого chat и возвращает его wire. Запрос требует
active membership/can_send, целевое устройство — active account/device этого chat.
Повторная claim того же KeyPackage невозможна; после создания Welcome запись
помечается `used=1` в той же транзакции и не используется повторно даже в этом chat.

## Привязка сообщения

Поле `mls` обычного send API содержит standard Base64 полного RFC MLSMessage с
PrivateMessage application content. Relay не принимает public application,
arbitrary plaintext, Welcome/commit в message endpoint и некорректный wire.
Сверяются wire `group_id`/epoch, зарегистрированный chat mapping, current roster
device и `authenticated_data == cryptoenc.Binding(chat,user,device,operation_id)`.
Pending transition запрещает отправку. В транзакции отправки Core повторно
проверяет epoch/barrier/account/device ACL, модуль — присутствие device в roster.

У relay нет sender-data key: он не расшифровывает encrypted sender leaf и не
проверяет private-message signature/AEAD. Получатель обязан проверить библиотекой
MLS AEAD/signature, восстановить действительного отправителя и сравнить binding
с account/device и внешними metadata. Прохождение relay означает разрешённую
доставку wire, а не доказательство его успешной MLS-аутентификации.

## Initialization и переход epoch

Обычный management create E2EE chat устанавливает `pending_rekey=true`.
Удостоверенный participant создаёт настоящую MLS group и возвращает public state.
Внешний management backend проверяет MLS state и разрешённую device roster.
`POST /management/v1/chats/{chat}/mls/confirm` инициализирует mapping при
`previous_epoch=-1` и пустом `transition_id`, используя signed roster proof ниже.
Внутренний InitMLSRelayTx используется для AI creation только после реального
library CreateGroup/Invite и проверки account/device ACL в management transaction.

`POST /v1/chats/{chat}/mls/commits` принимает:

```json
{"commit":"<base64 public member commit>","welcomes":[{"device_id":"recipient","key_package_id":"claimed SHA256 ID","welcome":"<base64 MLSMessage Welcome>"}]}
```

Relay проверяет current group/epoch, authenticated request account/device,
current roster leaf index и RFC `FramedContentTBS` signature зарегистрированным
Ed25519 public key с текущим GroupContext. External/nonmember/private commits
не поддерживаются этим endpoint. Commit и targeted Welcome сохраняются durably
at rest; ставится pending barrier без advance epoch. Второй candidate до
confirmation отклоняется. AI jobs могут блокировать переход через transaction hook.

`GET /v1/chats/{chat}/mls` возвращает current public context/epoch/roster/barrier.
`GET /v1/chats/{chat}/mls/inbox` возвращает до 100 targeted Welcome только текущему
authenticated device и до 100 public commits только device current roster.
Inbox не destructive: клиент должен deduplicate ID, persist обработанное
participant state и свои cursors. Для следующих страниц используются
`?after_welcome=<id>&after_commit=<id>`; ответ содержит `next_welcome` и
`next_commit`. Cursors scoped по chat/device; неизвестные отклоняются. Записи
control не очищаются автоматически — storage retention задаётся отдельно.

Management confirm нового epoch требует `transition_id`, `group_id`,
`group_context`, `previous_epoch`, `epoch`, `signer_device`, `signature`, `roster`.
Epoch должен advance ровно на один; old mapping/epoch/candidate совпадают, signer —
тот же действительный участник, что подписал commit, и остаётся в новом roster.
Каждое устройство новой roster активно и разрешено account ACL; roster покрывает
каждый active account и не содержит duplicate device/leaf indexes.

Signer подписывает Ed25519 точные bytes `MLSRosterProof(...)`: canonical JSON
в порядке `version,chat_id,group_id,previous_epoch,epoch,commit_hash,context_hash,roster`.
`version=1`; group_id Base64, hash — SHA-256 lowercase hex; roster сортируется по
device_id, entry fields — `device_id,leaf_index`; JSON escaping соответствует Go.
`commit_hash` — SHA-256 полного candidate wire (пустая строка при initialization),
`context_hash` — SHA-256 нового serialized GroupContext. Authority proof и
management authentication обязательны оба. Новая roster/context, AI snapshot,
epoch/barrier и event подтверждаются одной DB transaction.

## Обязательство внешней authority

Relay **не** может проверить MLS membership_tag или confirmation_tag без secrets,
TreeKEM path decryption/parent hashes и совпадение нового authenticated ratchet tree
с предоставленным roster/context. Public signature не покрывает confirmation_tag
и membership_tag. Новый GroupContext проверяется структурно и связывается с proof,
но relay не доказывает его криптографическое соответствие commit. Это не server
full MLS commit validation.

Прежде чем подписывать proof, внешний participant обязан выполнить полный
библиотечный ProcessCommit: membership MAC, signature, epoch, TreeKEM/path,
confirmation MAC, authenticated new tree и device credential/signing keys.
Management authority обязана проверить результат этого участника и roster
policy. Нельзя подтверждать произвольную переданную клиентом roster лишь потому,
что API request аутентифицирован. Подпись malicious participant не заменяет эту
внешнюю проверку. Confirmation endpoint — explicit trusted authority boundary.

После member removal/device revoke account ACL блокирует send/read, E2EE barrier
останавливает новые сообщения до подтверждённого rekey. Уже известные бывшему
участнику old-epoch secrets и plaintext невозможно отозвать relay.

## Проверки и release gate

Проверки пакета: `go test -race -tags qg_e2ee ./internal/cryptoenc ./internal/modules`
и `go vet -tags qg_e2ee ./internal/cryptoenc ./internal/modules`.
Тесты охватывают suite1 настоящие Welcome/app/commits, restored sender/receiver
counters, replay/wrong binding, Client API wire interoperability, device key
admission, consumed-once claims, signed roster, epoch barrier, stale confirmation,
tampered commit signature и targeted Welcome isolation.

Upstream pinned v1.3.0 RFC interoperability vectors были запущены:
`go test ./ciphersuite ./group ./framing ./treesync ./schedule ./secrettree ./interop -run 'Interop|Vector|Passive' -count=1` — PASS.
Это coverage библиотеки по published vectors, а не независимый runtime клиент.

Независимый OpenMLS runtime interop проверяется отдельно. До записанного PASS для
suite1 Welcome, bidirectional application, Add/Remove commits, epoch changes,
restart/replay и admitted AI lifecycle E2EE production publication blocked.
Флаг config, build tag или пользовательский boolean не заменяют release gate.
Фактические independent runtime результаты ниже; нельзя считать сборку Rust
или self-interop прошедшим independent gate.

### Фактический independent runtime результат

OpenMLS pinned `4f407c45857d177e7b5933d32795307b4f14b196`, unmodified protocol и
wire policy, против mls-go v1.3.0 `e7ef547b6bab2a63a83aaed6fcce949b71a11139`.
Docker test project `qgramm-mls-interop`, Go 1.26 image, Rust только в test container.
Upstream Dockerfile OpenMLS pinned на указанный revision; устаревший supplied
wire-format patch не применялся. Ни Rust, ни interop dependency не добавлены
в application runtime.

После build/up запускался upstream test-runner:

```sh
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -fail-fast -config /configs/welcome_join.json
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -fail-fast -config /configs/application.json
docker compose -p qgramm-mls-interop -f docker/docker-compose.yml run --rm test-runner -client mls-go:50051 -client openmls:50051 -suite 1 -public -fail-fast -config /configs/commit.json
```

Все три команды exit 0: Welcome 32 assignments / 16 cross-implementation,
application 24 / 12 cross-implementation, public commits 436 / 400 cross-implementation,
errors=0. Application проверяет оба направления, порядок/перестановку сообщений
и переходы epoch; public commit scenarios включают Add, Remove, Update, empty,
group_context_extensions и PSK. Конкретные metadata и counters сохранены в
`internal/cryptoenc/testdata/interop-evidence.json`.

Без `-public` commit matrix exit 1: восемь scenarios остановились из-за OpenMLS
pure wire-format policy при смешанных public/private handshake proposals. Это
реальный FAIL broader matrix; адаптер relay принимает только public commits.
Независимый production adapter и AI lifecycle проверены отдельным runner ниже.
Broader mixed-policy matrix остаётся FAIL; profile public commits не означает
поддержку mixed/private handshakes. Live provider API и проверка operational
external authority не входят в synthetic provider fixture.

Public/private здесь — формат MLS-сообщений, а не доступность чата. Прикладные
сообщения передаются как зашифрованные PrivateMessage. Proposals и commits
управляют составом группы и сменой эпохи: PublicMessage удостоверяет отправителя,
но оставляет управляющее содержимое видимым; PrivateMessage дополнительно
шифрует его. RFC 9420 §6 допускает публичные handshakes, когда сервис доставки
должен их проверять. Публичный commit не раскрывает секретные ключи группы.
QGramm принимает public member commits для проверки подписей и согласования
ACL/эпох. Клиенты должны выбрать этот профиль; зашифрованные управляющие
сообщения требуют дополнительной реализации и проверки совместимости.

### Production adapter и AI с независимым OpenMLS

2026-10-04: `docker compose -p qgramm-adapter-gate -f tools/mls-interop/compose.yml run --rm runner`
exit 0, Go `-race -mod=readonly -tags 'qg_e2ee qg_openai'`; оба теста PASS:
`TestProductionAdapterOpenMLSRestartReplayAndEpoch` и
`TestProductionAIHTTPWithIndependentOpenMLSAndDBRestart`.

Первый использует production MLSParticipant: encrypted disk snapshot/restore,
OpenMLS human wire decrypt, wrong binding/replay/appended-byte rejection,
два persisted sender counters, OpenMLS decrypt replies, public Add commit и
новый epoch, stale-epoch rejection. Второй проходит настоящие management AI
admission, client send, durable jobs и history, два human→AI→human exchanges;
между ними Core закрывается и заново открывается на той же SQLite DB. Human —
отдельный unmodified pinned OpenMLS process. Persisted `ai_chats.state` хранит
replay ledger, повтор первого human ciphertext после restart отклонён.

Provider внутри independent MLS стенда — local HTTPS mock с отдельным test CA
в isolated Docker network. Это проверка provider contract/TLS и штатного egress
validator. Отдельно OpenAI-совместимый API проверен живым DeepSeek; см.
[статус приемки](verification.md). Production networking guard не изменён.
Raw private keys, snapshots/JWTs не логируются. Reproducible test-only tooling:
`sh tools/mls-interop/run.sh`, pinned source/image digests, nested go.mod/go.sum;
Rust/gRPC не добавлены в deployment/runtime. См. tools/mls-interop/README.md.

Источники: [RFC 9420](https://www.rfc-editor.org/rfc/rfc9420),
[MLS WG interoperability](https://github.com/mlswg/mls-implementations),
[mls-go v1.3.0](https://github.com/thomas-vilte/mls-go/tree/v1.3.0),
[OpenMLS](https://github.com/openmls/openmls).
