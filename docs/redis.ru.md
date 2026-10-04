# Эксперимент с Redis внутри контейнера

[English](redis.md).

```sh
docker build -f Dockerfile.redis -t qgramm:redis .
go run ./cmd/qgramm-build compose -config configs/redis.toml -out compose.yaml
```

`features.redis=true` включает `qg_redis`. Compose выбирает `Dockerfile.redis`.
Обычный образ и минимальная сборка не содержат Redis-server, его Go-клиент,
дополнительные маршруты, таблицы или workers этого модуля.

Redis 7.2.14 запускается дочерним процессом под UID 10001 внутри общей CPU/RAM
квоты контейнера. SHA-256 официального архива проверяется при сборке.
[Исходные лицензии](https://github.com/redis/redis/blob/7.2.14/COPYING)
Redis и встроенных библиотек включены в `/app/redis-licenses`.

TCP отключен: используется Unix socket с правами 0600 в приватном каталоге
0700. AOF и snapshots отключены. TOML `[redis]` задает `binary`,
`max_memory_mb` (default 64) и `queue_depth` (default 256). Новых секретов нет.
Redis `maxmemory` ограничивает данные, а не весь RSS и Pub/Sub buffers.

Redis передает только ID чата в Pub/Sub wake-уведомлениях. Сообщения, ключи,
ACL, идемпотентность и подтверждения сохраняются только в прежнем SQLite core.
После успешного SQLite FULL commit wake попадает в ограниченную очередь,
публикуется с timeout 10 мс и без ретраев; подписчик вызывает локальный fan-out.
Переполнение очереди и ошибка публикации вызывают локальный wake.
Независимый SQLite replay-проверяющий цикл работает каждую секунду.
Гарантия durable ACK и восстановление журнала сохраняются.

Отсутствующий Redis или неудачная начальная подписка блокируют запуск явно
включенного профиля. При падении процесса после запуска core продолжает
работать через локальные уведомления и SQLite replay. Readiness проверяет
работающее ядро; для восстановления Redis нужен перезапуск контейнера.
Автоматического рестарта дочернего процесса нет.

При остановке core закрывает клиент, посылает Redis interrupt, при необходимости
через секунду завершает процесс, дожидается его reap и удаляет приватный каталог.
Redis добавляет IPC и отдельный процесс, поэтому его наличие само по себе
не обещает ускорение. Результаты сравнения публикуются отдельно.

`GET /v1/capabilities` содержит `redis.status`: `ready` либо
`degraded_local_fallback`, и `durable=false`. Отсутствие поля означает отсутствие
модуля. Compose прибавляет к расчетному бюджету Redis maxmemory + 32 MiB buffers
+ 16 MiB процесса. Это эвристика, а не обещание реального RSS.

Проверки используют настоящий процесс, без молчаливого skip тестов:

```sh
sh scripts/test-redis.sh sh scripts/build-matrix.sh
```

Helper берет существующий executable либо собирает checksum-verified Redis из
официального архива в игнорируемом `work/`, затем удаляет временную сборку.
Нужны curl, make и C compiler. Установка сервиса на хост не выполняется.
Можно явно задать `QGRAMM_REDIS_TEST_BINARY`; эта переменная предназначена только
для тестов, в production путь задается через TOML `redis.binary`.
