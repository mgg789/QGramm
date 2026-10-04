# Криптографический адаптер

Базовый envelope — HPKE RFC 9180, Base mode, X25519/HKDF-SHA256/AES-128-GCM
(32/1/1), через Cloudflare CIRCL v1.6.3. `info` строго `qgramm/basic/v1`.
`enc`, `ciphertext`, публичный ключ — стандартный padded Base64; `key_id` —
полный SHA-256 публичного ключа в lowercase hex. Каждый envelope создаёт новый
HPKE context, одно сообщение с sequence=0. Отправителя Base mode не удостоверяет:
аутентификация и проверка доступа обязательны на уровне HTTP/WebSocket.

`Binding(chat,user,device,operation)` задаёт точный порядок JSON-полей:
`version,chat,user,device,operation`, version=1, UTF-8, JSON escaping Go
(`\u003c`, `\u003e`, `\u0026` для HTML-символов). Привязка должна формироваться
сервером из удостоверенных account/device/chat данных, а не приниматься от клиента.

Данные на диске используют AES-256-GCM с криптографически случайным nonce от
`cipher.NewGCMWithRandomNonce`. Формат: `0x01 || nonce(12) || ciphertext || tag(16)`.
AAD: `0x01 || caller_aad`. Master key ровно 32 байта. На один master key разрешено
менее 2^32 вызовов Seal за весь срок службы; ротация и счётчик — ответственность
приложения. Разные записи/части файла должны иметь разные AAD с идентификатором
записи/файла, индексом части и общим количеством частей/длиной. Engine не выполняет
потоковое чтение и не проверяет полноту файла — это контракт файлового хранилища.

## MLS (`qg_e2ee`)

`mls.go` использует `thomas-vilte/mls-go v1.3.0`, библиотечные Group,
KeyPackage, Welcome, Commit, PrivateMessage и MarshalState. Suite 1:
X25519/HKDF-SHA256/AES-128-GCM/Ed25519. В минимальном build MLS не импортируется.
MLSParticipant представляет одно устройство в одной группе; методы Invite/Join
используют полные MLSMessage wire wrappers. ProcessCommit принимает public commit
существующего участника и проверяет membership tag и библиотечную подпись commit.
Private commits и external joins пока не поддерживаются адаптером.

`Snapshot(engine,aad)` сохраняет библиотечное Group state вместе с приватным
signing key и pending KeyPackage keys; контейнер шифруется at rest. Встроенный
file Store библиотеки сохраняет signing keys только в памяти, поэтому здесь
используется отдельный контейнер с Ed25519 private key и библиотечным MarshalState.

Приложение обязано сериализовать всю последовательность mutation + snapshot +
durable commit. Перед отправкой шифротекста или подтверждением обработки входящего
сообщения требуется атомарно сохранить новое состояние и outbound job. Нельзя
восстанавливать старые snapshots, делать две активные копии участника или
повторно отправлять сообщение через откатившийся SecretTree: это риск nonce reuse.
Adapter принимает incoming application только current epoch и хранит replay hashes
полного wire в encrypted snapshot. Это компенсирует обнаруженный replay после
upstream MarshalState/restore. Ledger ограничен 65536 принятыми сообщениями на
epoch; затем Decrypt fail-closed и требуется rekey. Ledger очищается только после
успешного epoch advance. Его тоже нельзя откатывать или терять при рестарте.
Persist failure должен прерывать операцию, не выпускать ciphertext и выводить
участника из эксплуатации до восстановления последнего подтверждённого состояния.
Сnapshot сам не является защитой от отката БД. Credential identity — утверждение;
приложение должно удостоверить соответствие account/device/signing key при admission.

## Проверки

`go test ./internal/cryptoenc` проверяет at-rest authentication, random nonces,
тампер всех байтов, обрезание, wrong key/AAD, envelope binding, low-order key reject,
и полный опубликованный CFRG RFC 9180 vector suite 32/1/1 base mode.
Источник fixture: https://github.com/cfrg/draft-irtf-cfrg-hpke/blob/master/test-vectors.json

`go test -tags qg_e2ee ./internal/cryptoenc` дополнительно проверяет MLS Welcome,
сохранение pending keys, sender/receiver counters после restart, replay reject,
wrong AAD, добавление третьего участника и epoch advance, tampered commit,
а также совместимость wire adapter с public Client API той же библиотеки.

Independent OpenMLS suite1 library runtime и upstream RFC 9420 vectors проверены
отдельно; точные команды, результаты и оставшиеся release gates в `docs/e2ee.md`
и `testdata/interop-evidence.json`. Они не означают сертификацию/аудит библиотеки.
Production adapter snapshot/replay и actual AI HTTP lifecycle с
SQLite restart также прошли отдельные independent OpenMLS gates; provider в них
явно local HTTPS mock, а не live API. Reproducer: tools/mls-interop/run.sh.

`NewWithPrevious(master,hpkePrivate,previousMasters,previousHPKE)` принимает
не более четырёх retired 32-byte ключей каждого типа. Encryption и advertised
public key всегда primary; storage Open пробует bounded retired AES-GCM keys,
HPKE выбирает точный key_id. После удаления retired key старые records/envelopes
больше не открываются. At-rest framing не меняется; AAD обязателен и при rotation.
