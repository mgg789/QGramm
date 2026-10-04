# Запускаемый пример basic-интеграции

Из корня репозитория, с версией Go из `go.mod`:

```sh
go run ./examples/basic
```

Пример запускает настоящий минимальный QGramm core на временном HTTP-порту loopback, создаёт Alice и Bob с отдельными X25519-устройствами и basic-чат, подписывает Ed25519 JWT, подключает Bob к WebSocket, отправляет HPKE-конверт от Alice, повторяет тот же запрос и проверяет совпадение ID сообщения, расшифровывает событие на стороне Bob и получает событие подтверждения доставки. При успехе выводится одна строка `OK`. SQLite и случайные ключи временные; учётные данные не выводятся. Сокеты, сервер и база закрываются, временный каталог удаляется. HTTP/WS-операции ограничены по времени, ошибка завершает процесс с ненулевым кодом.

Исключение для HTTP явно включено только в локальном smoke. Для production нужны HTTPS/WSS, независимо подтверждённый ключ сервера и отдельно развёрнутые сервис, бэкенд и клиенты. Проверяется собственная Go-реализация репозитория; совместимость с другой HPKE-реализацией, браузер, production TLS и нагрузка этим запуском не проверяются.

## Перенос в приложение

В `examples/basic/main.go` отмечены шаги бэкенда и клиентов. `mint`, закрытый ключ Ed25519 и management-вызовы остаются в доверенном бэкенде. До регистрации устройства и выдачи токена аутентифицируйте человека и подтвердите связь публичного ключа устройства с его учётной записью. Идентификаторы устройств неизменяемы: ротация создаёт новый ID с отзывом старого. Клиентам принадлежат закрытые ключи устройств, короткие JWT, HPKE, WebSocket и курсор обработанных событий. `localServer` — только smoke-обвязка; замените её URL развёрнутого сервиса и независимо переданным закреплённым публичным ключом сервера.

Пример может импортировать `internal/core` и `internal/cryptoenc`, потому что находится внутри Go-модуля репозитория. Это внутренние пакеты реализации, а не доступный внешнему приложению SDK. Внешнее приложение использует HTTP/WS-контракт и HPKE, совместимый с RFC9180; вызовы ниже показывают необходимые привязки. Набор алгоритмов, кодирование и политики описаны в [контракте интеграции](integration.ru.md).

Подписание токена на бэкенде (проверяйте `err`; `private` не передаётся клиенту):

```go
now := time.Now()
token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
    "iss": "qgramm", "aud": "qgramm", "sub": "bob", "device_id": "bob-phone",
    "iat": now.Unix(), "exp": now.Add(10*time.Minute).Unix(),
}).SignedString(private)
```

Bob передаёт JWT в `POST /v1/ws-tickets` (`Authorization: Bearer ...`, ответ HTTP201), затем использует полученный `ticket` один раз в течение 30 секунд:

```go
conn, response, err := dialer.Dial(wsBase+"/v1/ws?ticket="+url.QueryEscape(ticket), nil)
// Проверить err, при ошибке закрыть response.Body; URL с ticket не логировать.
err = conn.WriteJSON(map[string]any{"type":"subscribe", "chat_id":"hello", "after":cursor})
```

Alice сверяет `server_key` из `/v1/capabilities` с независимо полученным ключом, декодирует base64 с padding и шифрует:

```go
envelope, err := cryptoenc.SealEnvelope(serverPublicKey, []byte("Hello, Bob!"),
    cryptoenc.Binding("hello", "alice", "alice-phone", "hello-1"))
body, err := json.Marshal(core.MessageInput{OperationID:"hello-1", Envelope:&envelope})
// POST body в /v1/chats/hello/messages с JWT Alice. HTTP201 возвращает
// {"status":"accepted","message":{...}}: commit, а не доставку получателю.
```

Событие `message.created` содержит в `data` текущую проекцию сообщения для получателя. Bob расшифровывает `data.envelope` с привязкой к получателю и **серверному ID сообщения** в поле operation:

```go
plain, err := bobEngine.OpenEnvelope(*event.Data.Envelope,
    cryptoenc.Binding("hello", "bob", "bob-phone", event.MessageID))
```

Сохраните обработанный курсор перед `POST /v1/chats/hello/receipts` с `{"delivered":event.Seq,"read":0}`. Это заявление устройства о доставке, а не доказательство прочтения человеком. В basic-режиме QGramm расшифровывает сообщение внутри контейнера и перешифровывает для устройства; от оператора контейнера такой режим не защищает.

В production сохраняйте весь сериализованный запрос и operation ID до первой попытки. Повторяйте **те же байты**, без нового шифрования: новая случайность HPKE меняет ciphertext, и тот же operation ID с изменённым запросом даёт 409. Smoke завершает работу при HTTP/транспортной ошибке; production-политику повторов он не реализует. Убирайте дубли событий по `(chat_id,seq)` и ID/revision сообщения, переподключайтесь с новым ticket и сохранённым курсором, при HTTP410 или `sync.error` синхронизируйте историю и текущее состояние. Для долгоживущего клиента нужны обновление JWT, настройки browser origins и обработка WS ping/pong.
