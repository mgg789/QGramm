# QGramm Roadmap

## V1 Scope

- Invite-only auth flow with device binding, contact verification, captcha gate, and recovery key reveal.
- Personal chats with a clean iOS-first interface based on the provided Figma direction.
- Local security layer for identity and encrypted message bodies to keep architecture ready for server sync.
- Voice notes, circular video messages, file attachments with chunked upload simulation, reactions, replies, quotes, forwards, and reports.
- Calls history, call action sheet, trust levels, invite graph, admin moderation tools, and settings for encryption/cache/language.

## Progress Reports

### V1.0
- Date: 2026-03-10
- Status: In progress
- Done:
  - Parsed the task and extracted the V1 functional scope.
  - Read the Figma skill instructions and fetched Figma metadata, screenshots, and design context for the main chats and calls screens.
  - Verified the local environment, installed `xcodegen`, and prepared the source/resource folder structure.
- Next:
  - Generate the Xcode project.
  - Implement the SwiftUI shell, shared theme, models, local store, and the first buildable app flow.
  - Add the first semantic git commit once the scaffold builds.
- Architecture:
  - SwiftUI app with a single observable app store, feature folders, shared theme tokens, and local services for crypto, audio, storage, and media import.
  - UI-first V1 that stays compatible with later backend integration.
- Testing:
  - Environment validation only at this stage.
- Completion: 12%

### V1.1
- Date: 2026-03-10
- Status: Completed
- Done:
  - Поднят iOS-проект на `SwiftUI` через `xcodegen`, добавлены `project.yml`, структура feature-папок и `Assets.xcassets`.
  - Реализован invite-only вход: активация инвайта, локальная привязка устройства, captcha-gate, локальная отправка кода подтверждения и создание профиля с уникальным `@nickname`.
  - Добавлен recovery key flow: ключ создаётся при регистрации, показывается отдельным sheet сразу после входа и остаётся доступен в настройках.
  - Реализован shell V1: плавающий нижний dock, экран чатов с поиском и сегментами `Чаты / Группы / Каналы / Треды`, экран звонков, экран сети/инвайтов/админ-графа, экран настроек.
  - Реализован экран диалога с локальной моделью E2E-сообщений: reply, quote с редактируемым фрагментом, forward, report, реакции.
  - Добавлены реальные локальные медиа-функции: запись голосовых, импорт файлов, импорт видео-кружков, поэтапный chunk-upload progress без блокировки интерфейса.
  - Реализованы trust levels, недельные лимиты инвайтов, генерация кода/ссылки/QR, граф веток приглашений и админские действия `+trust / -trust / ban branch`.
  - Реализованы настройки профиля, шифрования, языка, кэша и локальной базы.
- Next:
  - При необходимости добавить реальный backend, транспорт E2E, VoIP и серверную часть invite-graph вместо локального стора.
  - Настроить remote для git и push ветки, так как в текущем репозитории remote не задан.
  - При отдельном запросе сделать device build вне песочницы и проверить на физическом iPhone.
- Architecture:
  - Один `AppStore` с `Codable`-состоянием и локальной JSON-персистентностью в `Application Support`.
  - Feature-first SwiftUI-структура: `Auth`, `Chats`, `Calls`, `Admin/Network`, `Settings`, плюс shared theme, models, services и components.
  - Криптослой на `CryptoKit`: симметричный ключ на диалог и локальное шифрование тела сообщений.
  - Медиа-слой на `AVFoundation`, `PhotosUI`, `fileImporter`, `CoreImage` для QR и локального preview pipeline.
- Testing:
  - `xcodegen generate`
  - `xcodebuild -project QGramm.xcodeproj -scheme QGramm -destination 'generic/platform=iOS Simulator' -derivedDataPath build/DerivedData CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO build`
  - Проверено, что `QGramm.app` создаётся в `build/DerivedData/Build/Products/Debug-iphonesimulator/`.
  - Обычный simulator build с codesign в этой среде падает не на коде, а на sandbox/CoreSimulator и extended-attribute ограничениях хоста.
- Completion: 100%

### V1.2
- Date: 2026-03-11
- Status: In progress
- Done:
  - Создан backend-модуль `backend/` на `Go + PostgreSQL` с production-структурой: `cmd`, `internal`, `migrations`, `configs`, `scripts`.
  - Добавлены серверные JSON-настройки лимитов/хранилища/root/runtime (`limits.json`, `storage.json`, `root.json`, `runtime.json`) + production sample.
  - Реализована полная SQL-схема (`migrations/001_init.sql`): users, invites, invite activations, invite graph, sessions, trust activity, conversations, messages, reactions, reports, uploads/chunks, attachments, calls, notifications, recovery bundle.
  - Реализованы бизнес-сервисы:
    - invite-only onboarding с привязкой устройства,
    - email verification code + captcha gate,
    - регистрация/логин/логаут/heartbeat,
    - trust engine L1-L5 по заданным правилам,
    - профили и уникальные никнеймы,
    - восстановление через recovery bundle,
    - чаты/сообщения/реакции/поиск по nickname,
    - жалобы и root-модерация,
    - chunked upload для файлов/голосовых/кружков/аватаров,
    - call history + websocket signaling,
    - QGramm broadcast и системные уведомления.
  - Поднят HTTP API c auth/root middleware и websocket endpoint.
  - Добавлен deploy-контур: `Dockerfile`, `docker-compose.yml` (backend + postgres + coturn), `scripts/deploy_remote.sh`.
  - Написана backend-документация: `backend/README.md`.
- Next:
  - Привязать iOS-фронт к новым API (убрать локальный стор как источник истины и включить сетевой слой).
  - Развернуть на сервере `178.140.207.217:2222` и прогнать e2e smoke-тесты на реальном окружении.
  - Дозакрыть SMTP/CAPTCHA production интеграции и TLS/reverse-proxy.
- Architecture:
  - Монолитный API-сервис на Go с PostgreSQL, websocket hub для realtime, файловым storage для chunk-upload, и конфигурируемым trust/invite/storage policy через JSON.
  - Сервер хранит только зашифрованный контент сообщений (ciphertext envelope), а recovery bundle хранится в encrypted-виде.
- Testing:
  - В текущей среде отсутствуют `go` и `docker`, поэтому компиляция/интеграционный запуск не выполнены локально.
  - Подготовлены полностью воспроизводимые файлы запуска и деплоя для проверки на целевом сервере.
- Completion: 75%

### V1.3
- Date: 2026-03-11
- Status: Completed
- Done:
  - Закрыт production-блокер chunk upload: добавлена обработка stale upload-сессий перед созданием новых, добавлен endpoint отмены загрузки `POST /v1/uploads/{uploadID}/cancel`, добавлена валидация размера assembled-файла.
  - Усилена безопасность отправки сообщений с вложениями:
    - attachment обязателен для `file/media/voice_note/circular_video`,
    - attachment запрещён для остальных типов,
    - attachment должен принадлежать отправителю,
    - `reply_to_message_id` теперь валидируется на принадлежность текущему диалогу.
  - Добавлены API для работы с файлами после доставки:
    - `GET /v1/attachments/{attachmentID}` (метаданные),
    - `GET /v1/attachments/{attachmentID}/download` (контролируемая выдача файла).
  - В `ListMessages` добавлен возврат реакций с user list (`reactions[]`) для полноценного рендера фронтом.
  - Добавлен production captcha mode `turnstile` (Cloudflare Turnstile) + обновлён `runtime.production.sample.json`.
  - Проверен и стабилизирован деплой на сервере:
    - пересборка/перезапуск стека docker compose,
    - исправлена деградация сети контейнера backend после частичного redeploy (полный recreate стека).
  - Проверен `ufw` на сервере и подтверждены открытые правила для backend/turn.
- Next:
  - Подключить iOS-фронт к backend API как к единому source of truth (сейчас UI-стор локальный).
  - Вынести backend за reverse-proxy с TLS (Nginx/Caddy) и включить реальный SMTP + Turnstile secrets.
  - Добавить интеграционные автотесты API в CI, чтобы smoke не выполнять вручную.
- Architecture:
  - Backend остаётся Go+Postgres монолитом с websocket-хабом и chunk storage, но теперь с полноценным циклом upload lifecycle (create/chunk/complete/cancel/download) и stricter attachment authorization.
- Testing:
  - На сервере выполнен полный e2e smoke: invite activation, register/login, автосоздание 2 диалогов, сообщения, реакции, жалобы, звонки, chunk upload, finalize attachment, download attachment, upload cancel.
  - Отдельно проверен негативный сценарий: root не может отправить attachment, принадлежащий другому пользователю (`forbidden`).
  - Health-check подтверждён: `GET /healthz -> {"status":"ok","service":"qgramm-backend"}`.
- Completion: 90%
