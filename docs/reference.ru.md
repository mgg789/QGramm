# Справочник интегратора QGramm

Ядро предоставляет HTTP-команды и WebSocket-события. Ваше приложение отвечает за учетные записи, интерфейс, ключи устройств, MLS и WebRTC. Контракты полей: [OpenAPI](openapi.json), [WebSocket JSON Schema](websocket.schema.json), [интеграция](integration.ru.md). Все сообщения пользователю и AI принимаются зашифрованными; AI-провайдер получает plaintext.

## Полный TOML reference

Неизвестные поля и недопустимые сочетания отклоняются. `.env` содержит значения секретов; TOML — только имена переменных. Сервис самостоятельно не читает `.env`; значения передает процесс запуска либо Compose. Примеры: `qgramm.toml`, `configs/minimal.toml`, `configs/full.toml`, `configs/container.toml`.

| Секция | Поля |
|---|---|
| `server` | `listen`: адрес; `tls_cert`, `tls_key`: пути TLS; `allow_insecure_loopback`: только разработка на loopback; `trusted_proxy`: доверять HTTPS-заголовку из изолированного ingress; `origins`: разрешенные browser origins |
| `storage` | `path`: SQLite; `files`: каталог зашифрованных частей файлов |
| `security` | `issuer`, `audience`: JWT authority; `token_public_key_env`: base64 Ed25519 32-byte verification key; `management_secret_env`: отдельный bearer минимум 32 символа; `master_key_env`: AES-256 32-byte key; `hpke_key_env`: X25519 32-byte private key; `previous_master_key_envs`, `previous_hpke_key_envs`: до четырех старых ключей каждого вида |
| `features` | Независимые boolean: `groups`, `files`, `e2ee`, `calls`, `delete`, `edit`, `reply`, `forward`, `reactions`, `openai`, `anthropic`, `mcp`, `http_tools` |
| `capacity` | `expected_concurrent_users`: одновременно подключенные люди; `max_connections`, `queue_depth`, `workers`: явные пределы, 0 — расчет |
| `policy` | `history`: `since_join` либо `all` с явным grant внешнего backend; `delete_mode`: `global` либо `author_only`; `reaction_types`: словарь числовых типов с 0; `event_retention_hours`, `dedup_retention_hours`, `upload_ttl_hours`: сроки; `max_message_bytes`, `max_batch`, `max_file_bytes`, `max_chunk_bytes`, `max_storage_bytes`: размеры и квоты |
| `ai` | `openai_url`, `anthropic_url`: provider base URL; `openai_key_env`, `anthropic_key_env`: имена секретов; `model`: модель; `max_steps`: предел вызовов инструментов; `max_context_turns`, `max_context_bytes`: контекст; `timeout_seconds`, `max_response_bytes`: сетевые пределы; `tools`: массив разрешенных коннекторов |
| `calls` | `turn_urls`: внешний TURN; `turn_secret_env`: имя shared secret; `credential_ttl_seconds`: срок credentials |

Каждый `[[ai.tools]]` задает `name`, `kind` (`http`/`mcp`), `url`, `methods`, `secret_env`, `allow_private`, `schema`, `timeout_seconds`, `max_response_bytes`. Последние два поля при 0 наследуют AI-пределы, положительные значения только сужают их. Defaults AI: 20 turns, 262144 context bytes, 45 секунд, 1048576 response bytes; увеличивать сверх этих safety ceilings нельзя. Точный допустимый поднабор JSON Schema описан в [AI reference](ai.md): неизвестные ограничения не игнорируются. Инструменты требуют AI-provider, соответствующий connector — включенной build-фичи; звонки требуют TURN-настроек. Значения и ограничения проверяет `internal/config/config.go`.

## Сборка и развертывание

```sh
go run ./cmd/qgramm-build build -config qgramm.toml -out bin/qgramm
go run ./cmd/qgramm-build plan -config qgramm.toml
go run ./cmd/qgramm-build compose -config configs/container.toml -out compose.yaml
docker compose --env-file .env up --build -d
```

Отключенные модули исключены Go build tags: нет их маршрутов, таблиц и workers в новой БД. Старые таблицы более полной установки не удаляются автоматически. Состав TOML сверяется с манифестом бинарника; смена функций требует сборки, обычных настроек — перезапуска. Docker работает UID 10001, с persistent `/data`, healthcheck и graceful shutdown. SQLite WAL/FULL рассчитан на один экземпляр; горизонтальный кластер не реализован.

Расчет ресурсов учитывает CPU/cgroup/RAM, ограничивает очереди и параллельность; Compose задает CPU/RAM limits, но не резервирует хост. Одно число пользователей не описывает поток сообщений, размеры файлов или fan-out. Текущие рекомендации консервативны и не являются гарантией производительности. [Измерения и ограничения](benchmark.md).

## Доставка и операции

Принятие означает commit сообщения, события и dedup в одной транзакции. Повторять нужно прежний ciphertext и `operation_id`; измененный запрос с прежним ID дает 409. Batch возвращает 207 с отдельным результатом каждого элемента, не атомарный результат всего списка. События упорядочены внутри чата; устройства имеют свои cursors/receipts. `accepted`, `delivered`, `read` различаются. При reconnect повтор возможен: deduplicate `(chat_id,seq)`. При 410/`sync.error` синхронизируйте состояние и историю. Переполнение дает 503 с `Retry-After`; durable события не теряются при потере wake-up.

Owner/admin/member и `can_send` задаются внешним management API. Отзыв устройства/участника закрывает соединения и блокирует новые команды. Редактирование требует expected revision; удаленное сообщение не редактируется. Глобальное удаление очищает активный payload, связанные доступы к файлам и реакции, публикует tombstone. `author_only` скрывает проекцию только автору. Ответ проверяет исходный чат; E2EE forwarding требует нового клиентского шифрования.

## Файлы и звонки

Загрузка: создать upload с quota reservation → отправить индексированные части с SHA256 → запросить статус для resume → идемпотентно complete → сослаться на готовые upload IDs в сообщении. Basic file key — независимый AES-256, передается HPKE; chunks — nonce+ciphertext+tag с документированным AAD, дополнительно шифруются при хранении. E2EE chunks opaque, ключ передается клиентами в MLS. Незавершенный набор не публикуется; TTL удаляет брошенные uploads. Нет транскодирования.

Calls — только 1:1 аудио/видео. Offer/answer/ICE идут через авторизованный API с idempotency, epoch barrier, timeout и credentials внешнего TURN. Core не обрабатывает медиа. Basic сигналинг HPKE, WebRTC media DTLS-SRTP; E2EE сигналинг и fingerprints защищаются MLS и проверяются клиентом. Несовпадение fingerprint требует завершения звонка. [Точные wire-поля](files-calls.md) и [проверка транспорта](webrtc-verification.md).

## Безопасность, ключи, AI

Basic доверяет контейнеру: у него private HPKE/master keys. TLS обязателен; `trusted_proxy` требует закрытого прямого HTTP-порта. Публичные ключи связывает доверенный внешний backend. Видны metadata, размеры, время и участники. Basic не обещает forward secrecy при компрометации статических ключей. Human E2EE использует MLS suite1, ключи участников серверу не выдаются; клиент проверяет identity/signature/epoch/roster. [MLS lifecycle](e2ee.ru.md).

Для ротации замените primary ключи, внесите имена старых секретов в `previous_*_key_envs`, перезапустите. Новые записи используют primary, старые читаются только при сохранении нужных ключей. Удаление retired master делает его данные нечитаемыми; автоматического перешифрования нет. Backup требует всех применимых ключей. Это не криптографический аудит и не автоматическая forward secrecy.

AI — явный отдельный участник личного чата. Он владеет только своими MLS-ключами; OpenAI/Anthropic получают plaintext. Durable jobs не создают повторный ответ при replay. Неопределенный внешний исход помечается `uncertain` и не повторяется бесконечно с оплатой. Global delete сбрасывает весь сохраненный AI context этого чата и отменяет queued/running jobs; поздний ответ не восстанавливает контекст. Уже отправленные провайдеру/инструменту запросы отозвать нельзя. Инструменты разрешает backend; URL/методы/размеры/timeout/шаги ограничены. Redirects запрещены, DNS dial закреплен, private addresses требуют явного разрешения. Модель не расширяет права. State шифруется; snapshots/backups могут сохранять прежние ключи. [AI contract](ai.md).

## Backup/restore и приемка

Остановите сервис, выполните `qgramm -config ... -backup /data/backup.db` в одноразовом контейнере с теми же config/env/volume. Скопируйте files и нужные ключи отдельно. Restore — согласованный database+files+keys, затем один экземпляр. Не восстанавливайте старый MLS AI snapshot для продолжения отправки без новой identity/epoch: rollback счетчиков опасен.

Скачанные получателями копии невозможно гарантированно удалить. WAL, SSD и backups не обеспечивают физическое стирание; retention и encrypted volumes — ответственность оператора. Независимый криптоаудит не подтвержден. Реальные provider API, браузеры, TURN и benchmark следует отличать от unit/mocks; текущий статус — [verification](verification.md).
