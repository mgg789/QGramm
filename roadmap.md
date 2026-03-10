# Qgramm Roadmap

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
