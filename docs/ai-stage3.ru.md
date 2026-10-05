# AI этап 3: scoped-хранилище и MLS sidecar

Этап 3 состоит из двух отдельных контуров. Интегрированный vault — это
ограниченное зашифрованное хранилище ресурсов внутри доверенного контейнера
QGramm. Дополнительный worker `micro-safer` — внешний MLS-участник, который
сам владеет своим приватным состоянием. Ни один контур не запускает embedding
model и не строит ANN-индекс на сервере.

## Интегрированный vault

Включаются вместе `ai_policy` и `ai_storage`, а также provider feature. Пример:
[configs/ai-storage.toml](../configs/ai-storage.toml). В `[[ai.tools]]`
используются `kind = "storage"`, фиксированный `resource`,
`storage_action` (`read`, `search` или `graph`) и `require_approval = true`.
Storage tool не может иметь HTTP URL, secret, methods или `allow_private`.
Resource и action входят в policy request.

Management API использует management bearer и обладает административными
правами:

```text
PUT    /management/v1/ai/storage/resources/{resource}
DELETE /management/v1/ai/storage/resources/{resource}
PUT    /management/v1/ai/storage/resources/{resource}/documents/{document}
GET    /management/v1/ai/storage/resources/{resource}/documents/{document}
DELETE /management/v1/ai/storage/resources/{resource}/documents/{document}
PUT    /management/v1/ai/storage/resources/{resource}/edges/{edge}
DELETE /management/v1/ai/storage/resources/{resource}/edges/{edge}
GET    /management/v1/ai/storage/requests/{request}
POST   /management/v1/ai/storage/grants
DELETE /management/v1/ai/storage/grants/{nonce}
POST   /management/v1/ai/storage/mcp
```

MCP endpoint также административный: он поддерживает согласованные JSON-RPC
initialize, tools/list и retrieval-вызовы. Модель не получает этот bearer и не
вызывает endpoint. Путь model retrieval проверяет generic AI policy approval и
второй typed storage grant.

Есть два независимых grant-контракта. Generic AI policy grant использует
`ai_policy.audience`; resource grant storage использует отдельный
`ai_storage.audience` и проверяется после binding policy effect. Эти token
нельзя взаимозаменять.

Storage grant — Ed25519 JWT с точными полями `nonce`, `request_id`,
`request_hash`, `chat_id`, `source_user`, `ai_user`, `resource`, `action`,
`destination`, `iss`, `aud`, `iat`, `exp`. Неизвестные или дублированные
claims, неверные issuer/audience, слишком большой TTL и action вне `read`,
`search`, `graph` отклоняются. Grant привязан к точному effect hash policy
request, destination провайдера, resource и action. Core отвечает за consume и
revoke; пакет хранилища только проверяет и связывает grant.

Перед расшифровкой payload по resource проверяются header-поля `scope`, `owner`
и routing visibility. `shared` можно явно расшарить. `user` принадлежит
source user, `bot` — AI participant; приватный retrieval допускается только в
личном чате из двух участников. Один retrieval request содержит один resource,
поэтому private/shared resources и labels не смешиваются.

Metadata resource, текст документов, vectors, filenames, file bytes, metadata
документов и edges зашифрованы с record-specific AAD. Search использует только
переданные vectors и необязательный lexical text matching. Сканирование и
top-K bounded; graph — bounded BFS по зашифрованным edges. Нет embedding model,
cloud embedding вызова, ANN и plaintext-индексов. Search не возвращает file
bytes и vectors. Ограничены размеры элементов, текста и metadata, dimensions,
число resources/documents/edges, results и глубина graph.

Полученный private context получает tainted label и destination утвержденного
провайдера. Его нельзя отдать другому внешнему tool или смешать с другим
resource с другим scope/owner label. Ресурсы с одинаковой меткой можно
объединять при наличии отдельного разрешения на каждый. После успешного использования vault успешный dialog context
очищается, чтобы следующий tool не использовал приватную историю.

## Внешний участник `micro-safer`

Внешний participant собирается с `ai_endpoint` и `e2ee`. Registry routes:

```text
PUT /management/v1/ai/endpoints/{endpoint}
GET /v1/chats/{chat}/ai/endpoints
```

Endpoint — активное зарегистрированное device с `kind` `llm`, `tools` или
`storage`. Registry не владеет приватным MLS state endpoint. Worker владеет
одним зашифрованным singleton state, pin-ит ровно двух участников чата и
останавливается при изменении membership, group context или epoch до offline
rekey/rejoin. Зашифрованный outbox сохраняется; uncertain external effect
возвращается вызывающему и автоматически не повторяется.

Через HTTPS relay передается ciphertext, plaintext появляется только на
endpoint host. Relay не получает plaintext. Реальные локальные LLM runtimes
отдельно не проверялись; текущая evidence покрывает HTTP-контракты и локальный
mocked transport.

### Контракт standalone worker

`qgramm-micro-safer` читает отдельный TOML deployment и не читает конфигурацию
core-сервера. В нём задаются `user` и `device` endpoint, HTTPS `server_url`,
имена secret environment variables, зашифрованный локальный `db_path`,
`issuer`/`audience` grant, pinned `peers` и `chats`, фиксированные `handlers` и
`models`, а также необязательные storage limits. URL и credentials являются
данными deployment: request body не может выбрать HTTP URL, bearer key, model
или MCP endpoint. В URLs model/handler запрещены credentials, query string и
fragment.

Граница plaintext на endpoint host задаётся явно. Relay передаёт MLS
ciphertext по HTTPS; настроенный loopback model или tool получает plaintext на
worker host. Для внешнего model или HTTP/MCP tool одновременно требуются
`[endpoint].allow_external_plaintext = true` и `allow_plaintext = true` у
handler/model. Это разрешение для исходящего plaintext на доверенном endpoint
host, а не embedding или agent-runtime протокол.

CLI worker намеренно небольшой:

```text
qgramm-micro-safer init   -config qgramm.toml [-chat name]
qgramm-micro-safer join   -config qgramm.toml [-chat name] -welcome file-or-base64
qgramm-micro-safer run    -config qgramm.toml [-chat name]
qgramm-micro-safer ingest -config qgramm.toml -input content-bundle.json
```

`init` создаёт локального участника и печатает public bootstrap material;
`join` принимает Welcome и проверяет pinned group из двух участников; `run`
открывает зашифрованный singleton state, подключает handlers и poll-ит relay;
`ingest` импортирует доверенный локальный файл с открытым содержимым в
зашифрованный storage sidecar. Защита и удаление исходного файла — отдельная
задача оператора. `run` завершается по interrupt или `SIGTERM`
хост-процесса. Wire contract не содержит peer-issued cancellation command;
HTTP context всё же может отменить локальный вызов.

Build и deployment конфигурации разделены. `qgramm-build build -target core`
собирает server, а `qgramm-build build -target micro-safer` собирает
`bin/qgramm-micro-safer` из `./cmd/qgramm-micro-safer`, передавая feature
manifest core build configuration в build tags и feature string. Затем worker
читает собственный TOML во время запуска. Core TOML не является deployment
файлом worker; этот документ не утверждает, что upstream local model runtime
установлен или принят.

### Relay, RPC и handler wire contracts

Relay transport использует `Authorization: Bearer <token_env>` и bounded
requests. Worker проверяет `/v1/chats/{chat}/mls`, poll-ит
`/v1/chats/{chat}/events` с durable cursor и отправляет encrypted responses в
настроенный chat. Для join обязательны offline pins group ID и signing key
собеседника; начальные `group_context` и ненулевой `epoch` можно закрепить
дополнительно. Во время работы group ID/context/epoch relay всегда сверяются
с сохраненным состоянием participant, а roster должен содержать ровно два
devices. Имя чата может ссылаться на другое имя peer через `[chats.<chat>].peer`. При mismatch
processing freeze-ится до offline Welcome/rekey; автоматической смены
membership нет.

Encrypted RPC request имеет version 1 и поля `request_id`, `client_id`, `chat`,
`source_peer`, `action`, `name`, `body` и необязательный внешний `grant`.
Body — strict canonical JSON: duplicate и trailing values отклоняются.
Responses — durable typed frames с `request_id`, `kind`, `seq`, `final`,
`body`; kinds: `notice`, `delta`, `result`, `error`, `uncertain`.
`source_peer` обозначает source device (storage sidecar также принимает
`user/device`), а `user` и `device` в конфигурации endpoint обозначают
исполняющего участника. Это разные identities.

`http` handlers принимают только GET, POST или PUT и возвращают bounded JSON.
`mcp`/`tool` handlers используют настроенный URL и operation только
`initialize` или `tools/call`. `model`/`llm` handler вызывает
настроенный OpenAI-compatible text chat completion endpoint. Request содержит
только text messages ролей `system`, `user`, `assistant` и fixed configured
model; caller не выбирает другой model или destination. Контракт не обещает
multimodal input, native agent-framework semantics или произвольные OpenAI
extensions.

Model stream — raw SSE: обрабатываются только строки `data:`, каждая frame
ограничена размером, и требуется `[DONE]`. Frames сохраняются как typed
`delta` и финальный `result` до relay acknowledgement; внешний plaintext model
сначала отдаёт безопасный `notice` с настроенным endpoint. Remote peer
cancellation и automatic retry uncertain external effect не поддерживаются.

### Два grant-контракта на границе worker

Outer RPC grant — typed request grant, связанный с MLS. Его точные claims:
`request_id`, `chat`, `source_peer`, `target_endpoint`, `action`, `name`,
`body_hash`, `destination_hash`, `epoch`, `group_id`, `group_context`,
`nonce`, `iss`, `aud`, `iat`, `exp`. Verify связывает точные RPC body,
handler name/action, endpoint, destination hash, group state, issuer/audience и
ограничивает lifetime 900 секундами.

Native vault retrieval использует отдельный Ed25519 JWT с полями
`request_id`, `request_hash`, `chat_id`, `source_user`, `ai_user`, `resource`,
`action`, `destination`, `nonce`, `iss`, `aud`, `iat`, `exp`. Sidecar проверяет
свой storage issuer/audience, exact request hash и destination, scope/owner
resource и pinned source identity, затем один раз consumes nonce в локальной
зашифрованной БД. Outer RPC grant и native vault grant имеют разные
audiences и не подменяют друг друга.

### Локальная persistence и limits

Worker хранит MLS singleton, cursor, intents, inbox и outbox в зашифрованной
локальной SQLite БД (schema version 2). `max_pending` по умолчанию 4096 и
ограничен конфигурацией; transactional inserts отклоняют новый pending intent,
inbox item или unsent outbox item после заполнения лимита. Также ограничены
frame/body, concurrency, storage item/text, vector dimensions, количество
resources/documents/edges, result count и graph depth. Storage defaults при
включении: 1024 resources, 10000 documents, 20000 edges, items 8 MiB, text 1
MiB, 100 results и graph depth 16.

Retained intents, inbox, RPC grants и storage grants ограничены каждый
`max_pending * 4`, outbox — `max_pending * 8`, включая завершенные записи.
При заполнении требуется управляемая оператором offline замена identity/group
и перенос состояния. Удалять replay-записи с повторным использованием той же
MLS identity нельзя. Это эксплуатационное ограничение beta-версии.
`request_timeout_ms` по умолчанию 120000 (максимум 1800000) ограничивает
HTTP-запросы relay/model/tools, включая streaming. Короткоживущий relay token
выпускает и обновляет интегратор; worker читает указанную переменную окружения.

Durable rows сохраняются для cursor progress, replay deduplication, outbox
replay и явных uncertain outcomes. Worker не запускает automatic garbage
collector, который мог бы удалить replay или tombstone evidence при
неразрешённом effect. Эти limits не заменяют capacity planning конкретного
deployment.

## Контрактный пример grant

Ниже HTTP-пример для backend-интегратора. Он не импортирует внутренние Go
пакеты QGramm и не заменяет authorization вызывающей стороны:

```http
GET /management/v1/ai/storage/requests/req-42
Authorization: Bearer <management-secret>

POST /management/v1/ai/storage/grants
Authorization: Bearer <management-secret>
Content-Type: application/json

{"token":"<Ed25519 JWT с точными storage claims>"}
```

Token должен содержать request ID/hash из request endpoint, настроенные
resource/action и точный effective destination провайдера. Затем model передает
storage tool только разрешенные arguments, например
`{"query":"invoice","limit":8}`. Caller обязан считать ответ tainted для
этого destination и очистить успешную private history после retrieval workflow.

Standalone backend может обмениваться только HTTP-контрактом, не импортируя
внутренние Go packages:

```go
type StorageGrant struct {
    Nonce string `json:"nonce"`; RequestID string `json:"request_id"`; RequestHash string `json:"request_hash"`; ChatID string `json:"chat_id"`
    SourceUser string `json:"source_user"`; AIUser string `json:"ai_user"`; Resource string `json:"resource"`; Action string `json:"action"`; Destination string `json:"destination"`
    Issuer string `json:"iss"`; Audience string `json:"aud"`; IssuedAt int64 `json:"iat"`; ExpiresAt int64 `json:"exp"`
}

// token выпускает собственный Ed25519 signer с точными JWT tags и отдельным
// ai_storage audience; wildcard grant недопустим.
req, _ := http.NewRequest("POST", baseURL+"/management/v1/ai/storage/grants", bytes.NewReader([]byte(`{"token":"`+token+`"}`)))
req.Header.Set("Authorization", "Bearer "+managementSecret)
req.Header.Set("Content-Type", "application/json")
resp, err := http.DefaultClient.Do(req)
```

Это пример wire-контракта: signer, reject duplicate claims, хранение ключа и
обработка HTTP errors остаются у интегратора.

См. [сгенерированный OpenAPI](openapi.json), [конфигурацию](configuration.md)
и [EN-контракт](ai-stage3.md).
