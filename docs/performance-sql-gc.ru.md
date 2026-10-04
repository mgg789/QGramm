# Commit, SQL-чтения и аллокации

[English](performance-sql-gc.md).

## Что изменено

Redis удален в `4b4308e`: в текущей сборке нет процесса брокера, feature/config/image/helper и его клиентских зависимостей. Предыдущие результаты Redis остаются архивными.

Коммит `b131b63` меняет горячий путь:

- Уже ожидающие сообщения объединяются в SQLite WAL **FULL** commit: максимум 16 jobs и 8 MiB сохраненного payload/metadata. Дополнительного таймера ожидания нет; крупное сообщение идет отдельно. Каждый job проверяет актуальные ACL/epoch/idempotency и выполняет SQL-only hooks в своем savepoint. Ошибка/отмена откатывает этот job; неисправимая ошибка транзакции или commit отклоняет все предварительные успехи. Ответы и уведомления выдаются после устойчивого commit. Точный retry восстанавливает результат при потерянном ответе. Конкурентные сообщения могут фиксироваться атомарно вместе; HTTP batch сохраняет последовательную отправку и результаты каждого элемента.
- Preflight чата/ACL — один JOIN; projection читает доступ и публичный ключ получателя одним свежим SQL-снимком. Проверки внутри транзакции остаются. Кэша ACL/ключей между запросами и обхода повторной авторизации нет. Число read-helper вызовов полного send с HTTP device authentication:6→4; непустой Events4→3, пустой Events2→1. Модули могут дополнительно читать SQL в hooks.
- Для одного сообщения не создаются ID-map пакетного replay. Metadata — структура; hash считается без копирования всего envelope JSON в объединенный буфер. Очередь освобождает ссылки на plaintext/envelope; attachments копируются, сохраняя владение и nil/empty wire semantics.
- Настройки GC и HPKE не менялись. Работа направлена на аллокации. Диагностические SQL-counters, гистограммы очереди/транзакций и GC доступны только в `qg_bench_profile`; в production нет гистограмм и debug endpoint.

Метрики различают транзакции commit, обработанные jobs и новые сохраненные сообщения. `commit_count` больше не равен числу сообщений. Queue wait считается для job, writer service — для группы вместе с commit. Время commit включает SQLite/driver и не выделяет fsync отдельно.

## Методика и границы

До: точная минимальная сборка `a78ebcf`, Redis выключен при компиляции. После: реализация `b131b63`; JSON фиксирует реальные immutable snapshot/image/binary hashes и overlays. Server Go 1.26.8 одинаков; байты generator/runner одинаковы.

По два повтора standard/subscribed для каждого варианта:8 основных прогонов, последовательно с cooldown минимум 60с.10 000 WebSocket,10с idle,100 сообщений/с30с +1 000/с5с. Standard — один подписанный получатель; subscribed —5 000 личных чатов и10 000 команд подписки (в активном чате подписаны оба пользователя). Basic HPKE,17 байт синтетического plaintext, полный HTTP-ответ. TLS, большие файлы и активность по множеству чатов не измеряются.

Стенд: Apple M5 Pro, 18 logical CPU, macOS 26.6.2, Docker Desktop Linuxarm64 VM kernel 6.12.76-linuxkit. Квота 4 CPU/8 GiB; эффективный отображаемый RAM около 7.748 GiB. Общий хост, SQLite в Docker volume; выделенный Linux local SSD не квалифицирован. Квота не резервирует ядра. В обоих измеренных бинарниках Redis отсутствует.

Delivery — приход `message.created` выбранному получателю. Генератор сравнивает количества accepted/events/history, не набор каждого received ID, расшифровку или устойчивый delivered/read ACK. Эти контракты покрываются отдельными protocol/fault tests. Percentiles nearest-rank рассчитываются для успешных запросов; отказы показаны отдельно. Общий percentile включает burst и зависит от смеси успешных запросов. CPU — среднее фазовых samples (100%=одно ядро); RAM — фазовый sampled max, не точный RSS-пик. Диапазоны двух повторов не являются доверительным интервалом.

Отдельные инструментированные subscribed прогоны на 2 000 пользователях сравнивают SQL-read calls, аллокации, GC/commit/queue counters. Одинаковые profiling overlays и forced-GC checkpoints влияют на timing и исключены из основного сравнения. Дельты аллокаций/GC включают трафик и projection истории, а не изолированную одну операцию. Гистограммы дают counts/верхние границы, не точные p95/p99. Сырые pprof остаются приватными: профиль может сохранять чувствительную runtime-память.

## Результаты основных прогонов

Диапазоны двух повторов. Latency — доставка; p95/p99 steady относятся к постоянной фазе. CPU/RAM также steady. Во всех8 прогонах **63 908 accepted = events = history**,0 отказов и неожиданных ошибок.

| Профиль / версия | p95 steady, мс | p99 steady, мс | p99 весь прогон, мс | CPU, % ядра | RAM, MiB |
|---|---:|---:|---:|---:|---:|
| standard / до | 5.04–5.16 | 7.38–8.01 | 6.99–10.80 | 13.47–13.84 | 271.20–281.60 |
| standard / после | 5.09–5.30 | 7.04–7.52 | 6.52–6.98 | 12.60–14.28 | 272.70–281.60 |
| subscribed / до | 4.77–4.84 | 6.66–7.45 | 6.56–20.88 | 15.50–16.12 | 330.10–347.90 |
| subscribed / после | 4.94–4.95 | 6.88–6.96 | 6.25–31.99 | 14.96–15.41 | 329.90–334.30 |

Средние **per-run** значения (не pooled percentiles):

- Standard: steady p99 −5,4%, p95 +2,0%; общий p99 −24,1%. CPU −1,6%; RAM +0,3%.
- Subscribed: steady p99 −1,9%, p95 +2,8%; общий p99 **+39,3%**. CPU −4,0%; RAM −2,0%. Второй subscribed repeat ухудшил общий p99 **20,879 → 31,994 мс**, первый улучшил **6,563 → 6,246 мс**.

Это умеренное снижение некоторых затрат и смешанный результат latency, а не общее ускорение. Двух повторов на общем хосте недостаточно, чтобы доказать причину каждого хвоста или статистическую значимость небольших CPU/RAM-различий. Причину subscribed burst tail эта серия пока не изолирует; нельзя автоматически списывать ее на GC или считать устраненной.

[Summary JSON](benchmarks/sqlgc-summary.json)

- standard: [before 1](benchmarks/sqlgc-before-standard-1.json), [before 2](benchmarks/sqlgc-before-standard-2.json), [after 1](benchmarks/sqlgc-after-standard-1.json), [after 2](benchmarks/sqlgc-after-standard-2.json)
- subscribed: [before 1](benchmarks/sqlgc-before-subscribed-1.json), [before 2](benchmarks/sqlgc-before-subscribed-2.json), [after 1](benchmarks/sqlgc-after-subscribed-1.json), [after 2](benchmarks/sqlgc-after-subscribed-2.json)

## Отдельная диагностика: commit и GC

Один subscribed прогон до/после на **2 000 пользователях**; четыре одинаковых forced-GC checkpoints (idle/steady/burst/final). 7 998/7 989 accepted = events = history, без ошибок. Интервалы final-minus-idle: **45,170 / 43,495с**; они включают историю, фоновые задачи и profiler, поэтому нормализация на accepted не является изолированной стоимостью send.

| Счетчик | До | После |
|---|---:|---:|
| Выделенные байты / accepted¹ | 101 801 | 98 725 (−3,0%) |
| Аллокации / accepted¹ | 1 622 | 1 531 (−5,6%) |
| SQL read-helper calls / accepted¹ | 13,385 | 9,701 (−27,5%) |
| Commit-транзакции / accepted | 7 998 / 7 998 | 7 824 / 7 989 |
| Суммарное время commit | 5,418с | 4,754с (−12,2%) |
| Суммарное время writer service | 6,218с | 5,528с (−11,1%) |
| Сумма ожиданий jobs | 8,800с | 0,551с (−93,7%) |
| Сумма GC-пауз | 28,922мс | 25,998мс (−10,1%) |
| GC cycles, включая forced | 48 | 48 |

¹ За весь измеренный интервал с трафиком, историей и фоновой работой, разделенный на accepted. Read-helper counter не включает write/driver-internal SQL. Read-helper calls/s: 2 370 → 1 782 (−24,8%); allocation bytes/s практически не снизились: 18,03 → 18,13 MB/s из-за разной длительности интервала. Это не throughput benchmark.

Commit на сообщение в среднем: **0,677 → 0,595мс**; время отдельной commit-транзакции: **0,677 → 0,608мс**. Объединение фиксирует всего **1,021 сообщения на commit**; при этой нагрузке большинство jobs приходят без уже готовых соседей. Не добавлялся таймер, искусственно собирающий большой batch. Число commit уменьшается примерно на2%, а не пропорционально пределу16. Это не счетчик fsync.

Гистограммы показывают интервальные bucket/cumulative counts: ожидание writer более8мс встречалось277 раз до и0 после. Это профилированный прогон с coarse buckets, не точный p95/p99 и не объяснение ухудшившегося primary tail. GC cycles не сократились; вывод «GC устранен» неверен. Сырые pprof остаются приватными.

[Diagnostic summary](benchmarks/sqlgc-diagnostic-summary.json) · [Before run](benchmarks/sqlgc-before-diagnostic.json) / [counters](benchmarks/sqlgc-before-diagnostic-counters.json) · [After run](benchmarks/sqlgc-after-diagnostic.json) / [counters](benchmarks/sqlgc-after-diagnostic-counters.json).

## Следующие измерения

1. Для subscribed burst tail нужны временные ряды commit/body/projection/queue/GC без forced GC, сопоставленные с конкретными медленными запросами. Нынешние агрегаты не доказывают причину31,994мс.
2. Allocation gain только3% при read-helper gain27,5%: следующий профиль стоит сосредоточить на оставшихся HPKE/JSON/driver allocations, сохраняя свежие криптографические контексты и проверки доступа. Просто увеличивать GOGC пока не обосновано.
3. Нагрузку по многим активным чатам и sustained write saturation измерять отдельно; opportunistic group commit при100/с обычно нечего объединять. Longer soak/Linux SSD нужны для калибровки, а не для переименования этих Docker-результатов в подтвержденный production sizing.

## Проверки

Прошли `go test -race ./...`, race полного 13-feature профиля, profile-tag tests, `go vet ./...`, `go run -race ./examples/basic`. После удаления Redis прошла матрица 28 профилей + 7 ошибочных конфигураций. Независимые review/inspection пройдены; найденное владение input-slice исправлено до финальных images. Group-тесты используют настоящую SQLite: нет ранних ACK/fan-out, контролируемый сбой commit, реальный SQLITE_FULL/recovery, отмена до/во время job/commit, savepoint hook failure, dedup/порядок и лимиты count/bytes. Projection-тесты проверяют актуальные member/device/user ACL, ротацию ключа, tombstone/hooks и совместимость digest/metadata.

## Воспроизведение

```sh
go build -o work/qgramm-bench ./cmd/qgramm-bench
docker build --build-arg CONFIG=configs/container.toml -t qgramm:sqlgc .
python3 tools/benchmark.py --image qgramm:sqlgc --out work/standard.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s
python3 tools/benchmark.py --image qgramm:sqlgc --out work/subscribed.json \
  --generator-binary work/qgramm-bench --users 10000 --duration 30s \
  --rate 100 --burst 5s --burst-rate 1000 --idle 10s --idle-subscriptions
```

Сравниваемые images собирайте из отдельных checkout выбранных ревизий, фиксируйте source/image/binary hashes и один generator. Два повтора запускайте последовательно с cooldown≥60с; не смешивайте profiler с latency-прогонами. Исторические JSON содержат точные fingerprints зафиксированного runner; текущая команда воспроизводит нагрузку, не обещает побитовую идентичность будущего build/toolchain.

Для повторения диагностических checkpoints приватный wrapper изменял только условие sampler на `args.profile_dir and phase in ("idle", "steady", "burst") and phase != last_phase`; финальный SIGUSR1 выполнялся после load. Проверено ровно4 runtime snapshot. SHA wrapper указан в JSON. Текущий runner без этого выбора может дополнительно снимать setup/history, поэтому такие профили не эквивалентны этой серии.

[Следующая серия replay/WAL](performance-tail.ru.md).
