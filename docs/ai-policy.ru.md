# AI policy: разрешения, расходы и выход данных

Второй этап добавляет опциональный модуль `features.ai_policy` / `qg_ai_policy` для именованных BASIC-ботов и старого прямого MLS AI-пути. [Пример TOML](../configs/ai-policy.toml), [AI-сеть](ai-network.ru.md), [OpenAPI](openapi.json). Нужен хотя бы один провайдер. Исключение модуля требует пересборки; на новой БД отсутствуют его маршруты, таблицы и cleanup. Старые таблицы автоматически не удаляются.

## TOML

| Поле `[ai_policy]` | По умолчанию / смысл |
|---|---|
| `grant_public_key_env` | `QGRAMM_AI_GRANT_PUBLIC_KEY`: base64 Ed25519 public key, 32 байта; private signing key хранит внешний backend |
| `issuer`, `audience` | `qgramm-backend`, `qgramm-ai-control`; отдельно от device JWT |
| `approval_ttl_seconds` | 300; диапазон 1–900 секунд, максимальная жизнь grant |
| `require_provider_approval` | false; true требует разрешения перед каждым вызовом модели, включая вызов после результатов tools |
| `provider_reserve_microunits` | 1000000; диапазон 0–1000000000000, резерв до вызова провайдера |
| `global_daily_budget_microunits` | 0 означает отсутствие лимита; иначе до 1000000000000000 |
| `per_bot_daily_budget_microunits` | Такой же диапазон; счет AI user identity (`user_id` участника, не имя TOML bot profile) |
| `per_user_daily_budget_microunits` | Такой же диапазон; счет автора исходного сообщения |
| `currency` | `USD`; три заглавные буквы, метка учета |

Microunit — миллионная доля единицы валюты. Ненулевой бюджет требует положительного резерва провайдера. В `[ai]` и `[ai.endpoints.NAME]` задаются `input_price_microunits_per_million_tokens` и `output_price_microunits_per_million_tokens`: по умолчанию 0, максимум 1000000000000. Endpoint переопределяет глобальные цены. Каждый `[[ai.tools]]`, HTTP или MCP, получает `require_approval` (false) и `cost_microunits` (0, такой же максимум). Успешный tool списывает эту фиксированную стоимость. Несовместимые approval/cost настройки без модуля отклоняются.

## Разрешение внешнего backend

Текст чата, ответ модели, аргументы tool и streaming-дельты не дают прав. Если требуется approval, сервер сохраняет зашифрованное продолжение и переводит задание в `awaiting_approval`, не отправляя внешний запрос. Worker освобождается; эта пара bot/chat сохраняет последовательность исполнения. Событие `ai.approval.required` содержит `job`, `request_id`, `action="approval_required"`, без текста и аргументов.

Backend читает `GET /management/v1/ai/requests/{request}`. Ответ содержит identity, epoch, session version, action/name, request/destination hashes, origin адресата, required, reserve, статус и время. Prompt, аргументы и сохраненное продолжение не выдаются. Backend принимает решение и подписывает Ed25519 JWT с точным header `{"alg":"EdDSA","typ":"JWT"}`. Полный [пример структуры claims](ai-policy.md) содержит:

- `v=1`, уникальный одноразовый `jti`, `request_id`, `job_id`, `chat_id`;
- `source_user`, `source_device`, `ai_user`, `ai_device`, `epoch`;
- `action` (`provider` или `tool`), `name`, `request_hash`, `destination_hash`;
- `iss`, `aud`, текущий `iat`, `exp` с жизнью не больше approval TTL.

Поля binding копируются из pending request; wildcard grant не предусмотрен. Request hash связан с запросом, адресатом, ценами/резервом, epoch и версией сессии. Destination hash относится к полному адресу, событие содержит только origin. Нормализация аргументов tool отклоняет повторные JSON-ключи; это не RFC 8785. Неизвестные/повторные claims, неверная подпись, scope, срок или audience отклоняются.

Все маршруты ниже требуют отдельного management bearer:

| Маршрут | Запрос / ответ |
|---|---|
| `POST /management/v1/ai/approvals` | `{"token":"signed-JWT"}` → `grant`, `status="approved"`; точный повтор идемпотентен |
| `DELETE /management/v1/ai/grants/{grant}` | Отзыв grant ID либо nonce, в том числе до выдачи → `grant`, `revoked` |
| `GET /management/v1/ai/requests/{request}` | Метаданные bound request; состояния awaiting/approved/dispatched/completed/uncertain |
| `GET /management/v1/ai/usage` | `items`, опционально `next_cursor`; `limit` 1–100, descending `cursor`, фильтр `user` (source human user ID) либо `bot` (AI participant user ID) |

Одобренное задание возвращается в `queued`. Перед продолжением повторно проверяются source visibility, membership, device status, tools, epoch, session revision, подпись, expiry и revoke. Потребление grant, резерв бюджета, egress notice и dispatched intent фиксируются одной транзакцией до сети. Nonce одноразовый. Продолжение не повторяет прошлые provider/tool вызовы и не расшифровывает повторно уже потребленное MLS-сообщение. Отмена и удаление исходника блокируют поздний ответ; уже отправленный эффект отозвать нельзя. Ограниченный cleanup обрабатывает до 256 истекших awaiting/approved requests за проход, очищает продолжение и переводит ожидающее задание в failed; истекший grant отклоняется сразу при проверке.

## Счетчики и границы

Все вызовы проходят gateway, даже без approval. До отправки атомарно проверяются и резервируются дневные UTC-счета: global, AI user и автор сообщения. При отказе бюджета запрос не отправляется. Успешный вызов с известным usage заменяет резерв расчетной стоимостью по ценам оператора; input/output округляются вверх отдельно. Cache input Anthropic входит в input tokens и показывается отдельно. Streaming OpenAI запрашивает финальный usage chunk; cumulative usage Anthropic не суммируется повторно. [OpenAI wire contract](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events), [Anthropic wire contract](https://platform.claude.com/docs/en/build-with-claude/streaming).

Отсутствующий usage означает неизвестное значение, а не ноль. При нем и неопределенном сетевом исходе резерв сохраняется. Dispatched эффекты после рестарта автоматически не повторяются. Успешный ответ может иметь неизвестную стоимость. Фактическая расчетная стоимость может оказаться выше резерва: следующий вызов блокируется при превышении бюджета. Задавайте консервативный резерв под модель и output limit. Это оценки, не сверенные счета провайдера и не жесткая гарантия ограничения внешних расходов. Специальные cache-тарифы и billing rules не угадываются.

Usage возвращает scope, request/job IDs, UTC day, invocation count, reserved/cost microunits, input/output/cache tokens, `usage_known`, currency и статус. Один вызов отражается в нескольких scope: не суммируйте их как разные обращения. Для отчета выбирайте один scope, например фильтр bot. Grants, продолжение, суммы ledger и aggregate accounts зашифрованы при хранении; IDs, время и routing metadata видимы. Backup требует соответствующих master keys.

Перед каждым effect сохраняется `ai.egress.notice`: `job`, `request_id`, `action`, `name`, `destination_origin` и confidentiality. Обычный provider/tool использует `confidentiality="external_plaintext"`; retrieval интегрированного vault — `confidentiality="integrated_storage"` и всё равно требует отдельный storage grant из [AI этапа 3](ai-stage3.ru.md). Это порядок commit до отправки; доставка события клиенту может произойти позже. Интегратор отображает свое предупреждение. URL path/query, prompt, ответ и аргументы не включены. Разрешение управляет доступом, а адресат получает plaintext. Приватный IP сам по себе не означает криптографически защищенный endpoint.

Третий этап — LLM/tool encryption sidecars и scoped file/RAG/GraphRAG — этим модулем не реализован. Контейнер, inference и retrieval hosts остаются доверенными границами обработки plaintext. Локальные тесты второго этапа не являются независимым аудитом или новой приемкой реального API провайдера.
