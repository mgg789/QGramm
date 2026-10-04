# Реальные сравнительные baseline-измерения

[English](comparison-benchmark.en.md)

Сравнение выполняется последовательно на одном Apple M5 Pro / Docker Desktop Linux arm64 с квотами контейнеров `--cpus 4 --memory 8g`. Генераторы работают на macOS вне квоты сервера. Docker фактически показывает доступный предел 7.748 GiB. Это один прогон каждой конфигурации, не статистическое исследование и не рейтинг протоколов.

## Сценарий и контракты

10 000 открытых соединений, один отправитель, один получатель, остальные соединения простаивают. Обычная нагрузка: 100 сообщений/с 30 секунд; всплеск: 1000/с 5 секунд. QGramm отправляет зашифрованный текст `benchmark payload` (17 байт до шифрования); baseline отправляет уникальную строку из 17 ASCII-цифр. Все измерения без TLS. NATS и Centrifugo используют WebSocket; QGramm принимает публикации HTTP, доставляет WebSocket. Нагрузка учитывает успешно принятые и уникально доставленные сообщения отдельно. У baseline 64 одновременных запроса максимум; Centrifugo отправляет команды по одному соединению последовательно и включает ожидание в очереди клиента в ACK-латентность.

| Система | Работа, включённая в публикацию | Что означает ACK |
|---|---|---|
| QGramm minimal | JWT, Ed25519, ACL устройства/чата, HPKE envelope, SQLite, история, durable delivery | HTTP 201 после транзакции SQLite WAL с `synchronous=FULL` |
| NATS JetStream 2.11.3 | Проверка общего токена, file stream replicas=1, durable consumer с explicit ACK | JetStream PubAck; при стандартном `sync_interval=2m` это не fsync каждого сообщения |
| Centrifugo 6.2.3 | JWT соединения, authenticated publish/subscribe, memory history 10 000 публикаций/60 секунд | Публикация принята в память; durable chat/БД отсутствует |

HPKE/Ed25519/ACL QGramm и транзакции чата отсутствуют в baseline. Поэтому сравнение отвечает на вопрос о наблюдаемых расходах конкретных сценариев, но не позволяет объявлять универсального победителя или обещать такой же расход ресурсов после добавления эквивалентной прикладной логики.

## Воспроизведение

Из корня репозитория:

```sh
cd tools/comparison
go mod download
go vet ./...
cd ../..
python3 tools/comparison/run.py --service nats --out docs/benchmarks/comparison-nats-10000.json
python3 tools/comparison/run.py --service centrifugo --out docs/benchmarks/comparison-centrifugo-10000.json
```

Runner создаёт временные конфиги и случайные секреты, публикует только loopback-порты, удаляет только свои UUID-контейнеры/тома в `finally`. Секреты не включаются в результаты. Генератор имеет общий timeout 300 с; для более долгих сценариев его можно явно увеличить `--timeout`. Source-build скрипты используют новый `mktemp` context и очищают только его, сохраняя существующие файлы пользователя. Корневые зависимости Go не изменяются; `tools/comparison/go.mod`/`go.sum` фиксируют SDK. Образы имеют конкретные версии; JSON фиксирует фактически использованный image ID и digest.

На этом хосте загрузка обоих образов Docker Hub завершалась `EOF`; загрузка GitHub release binary также завершилась TLS-ошибкой. Для NATS использован официальный модуль `github.com/nats-io/nats-server/v2 v2.11.3`, собранный Go 1.26.4 для Linux arm64 и помещённый в уже имеющийся Alpine 3.22. Centrifugo аналогично собран из `github.com/centrifugal/centrifugo/v6 v6.2.3`; `go version -m` обоих Linux-бинарников подтвердил версии модулей и Go 1.26.4. Это реальные серверы. Альтернативный воспроизводимый путь:

```sh
sh tools/comparison/build-nats.sh
python3 tools/comparison/run.py --service nats --image qgramm-comparison-nats:2.11.3 --out docs/benchmarks/comparison-nats-10000.json
sh tools/comparison/build-centrifugo.sh
python3 tools/comparison/run.py --service centrifugo --image qgramm-comparison-centrifugo:6.2.3 --out docs/benchmarks/comparison-centrifugo-10000.json
```

Для проверки более строгого fsync-контракта runner поддерживает `--nats-sync always`. Это отдельная конфигурация, её результаты нельзя смешивать со стандартным JetStream.

## Результаты

| Система, обычная фаза | ACK p95 / p99, ms | Delivery p95 / p99, ms | Принято/доставлено, обе фазы | Выборочный максимум CPU | Выборочный максимум RAM |
|---|---:|---:|---:|---:|---:|
| QGramm minimal | 4.364 / 6.657 | 4.676 / 6.898 | 7988 / 7988 | 61.05% | 666.3 MiB |
| NATS JetStream default sync | 2.759 / 5.161 | 2.788 / 5.186 | 8000 / 8000 | 23.66% | 489.5 MiB |
| NATS JetStream `sync_interval=always` | 3.945 / 6.641 | 4.018 / 6.621 | 8000 / 8000 | 23.50% | 469.3 MiB |
| Centrifugo memory history | 2.781 / 4.719 | 2.800 / 4.952 | 8000 / 8000 | 28.48% | 450.1 MiB |

Centrifugo burst: ACK p95/p99 2.555/4.013 ms, delivery 2.544/3.970 ms; установка 10 000 JWT-соединений 9.649 с. Принято и уникально доставлено 8000 сообщений, 0 дубликатов/publish/receive/disconnect errors, generator exit 0. Raw evidence: `docs/benchmarks/comparison-centrifugo-10000.json`.

NATS `always` burst: ACK p95/p99 2.633/4.920 ms, delivery 2.998/5.221 ms. Это отдельный прогон с меньшим окном durability: сервер настроен вызывать `Sync()` при каждой записи в файл до normal-path ответа; crash/power-loss resilience не тестировалась. Raw evidence: `docs/benchmarks/comparison-nats-fsync-10000.json`.

NATS burst: ACK p95/p99 2.025/4.104 ms, delivery 2.026/4.074 ms; обычная фаза 3000, всплеск 5000 сообщений. Все 8000 уникально доставлены, дубликаты/ошибки отсутствуют. Установление 10 000 соединений заняло 10.575 с. Стандартный JetStream не даёт fsync-before-ACK, поэтому меньшая ACK-латентность сама по себе не означает такую же гарантию записи, как у QGramm. Raw evidence: `docs/benchmarks/comparison-nats-10000.json`; QGramm: `docs/benchmarks/minimal-10000-p99.json`.

Одна попытка повторного NATS `sync_interval=always` завершилась `unexpected EOF` при установлении соединений, до начала нагрузки; latency/resource цифры этой попытки не включены. Причина не установлена. Между последующими прогонами выдержана пауза не менее 60 с для снижения влияния connection churn; настройки сети/sysctl не менялись. Подробности: `docs/benchmarks/comparison-setup-failures.json`.

## Метод измерений

ACK-латентность: от начала запроса в генераторе до ответа сервера. Delivery-латентность: от того же начала до callback/read у реального клиента получателя; общий monotonic clock процесса, без рассинхронизации часов. Перцентили nearest rank `sorted[ceil(p*n)-1]`, только успешные запросы/наблюдения. Каждый payload имеет уникальный ID, считается первая доставка и отдельно дубликаты; ожидание доставки заканчивается не позднее 30 секунд после фаз.

`docker stats --no-stream` + пауза 1 с даёт примерно одну выборку за 2 с: от запуска/установления соединений до окончания нагрузки. Максимумы выборочные; короткие пики и CPU генератора не входят. CPU 100% означает одну занятую логическую CPU, не весь лимит из четырёх CPU. Actual offered/s вычисляется как число отправленных запросов / elapsed_seconds каждой фазы; если предел 64 in-flight замедлит генератор, растянутая фаза будет видна в JSON. После первых прогонов генератор дополнен fail-fast integrity gate, учётом разрывов idle-соединений и таймаутом ответа Centrifugo, затем полные 10 000-connection baseline прогоны повторены. JSON фиксирует SHA текущего генератора; offered/accepted/delivered, дубликаты и publish/transport errors проверены. Runner сохраняет JSON и возвращает ошибку при потере, дубликатах или transport/publish errors.

Замер диска/сети, crash-recovery, проверка power-loss durability, горизонтальное масштабирование, множество активных чатов и шифрование клиентом внутри server quota не выполнялись.

Планировщики различаются: QGramm использует ticker и записал 7989 запросов, baseline планирует 8000 отдельных дедлайнов. Надо сравнивать actual accepted/rate, а не выдавать номинальные 1000/с за гарантированную устойчивую пропускную способность.

## Источники контрактов

- [NATS 2.11.3 filestore: defaultSyncInterval](https://github.com/nats-io/nats-server/blob/v2.11.3/server/filestore.go#L316): стандартный interval 2 минуты. [Синхронная запись и SyncAlways](https://github.com/nats-io/nats-server/blob/v2.11.3/server/filestore.go#L6777) вызывают `Sync()`; [AsyncFlush по умолчанию false](https://github.com/nats-io/nats-server/blob/v2.11.3/server/filestore.go#L429). [Публикация сохраняется до normal-path ответа](https://github.com/nats-io/nats-server/blob/v2.11.3/server/stream.go#L5210). Эти строки проверены также в локальном официальном Go-модуле v2.11.3. Это описание пути записи; power-loss/crash и ошибки fsync отдельно не проверены.
- [NATS configuration reference](https://docs.nats.io/reference/config/): `sync_interval` и серверные параметры.
- [NATS server 2.11.3](https://github.com/nats-io/nats-server/releases/tag/v2.11.3), [nats.go 1.39.1](https://github.com/nats-io/nats.go/releases/tag/v1.39.1).
- [Centrifugo 6.2.3](https://github.com/centrifugal/centrifugo/releases/tag/v6.2.3), [JWT и конфигурация](https://centrifugal.dev/docs/server/configuration), [publish permissions/history](https://centrifugal.dev/docs/server/channels).
