# Три отдельных эксперимента: компактный ответ, batch commit и idle replay

[English](performance-three.md).

База — точный архив dev `2ce7077`, а не main. `0d42533` меняет только benchmark tooling; `ab09b58` повторно использует poll-буферы; `30eafab` добавляет групповой commit HTTP batch. Образы проверяют каждый вариант отдельно. FULL, автоматический checkpoint и прежний интервал fallback polling сохранены; дополнительный PASSIVE worker выключен.

## Что проверяем

1. Существующий `Prefer: return=minimal`: HTTP возвращает receipt устойчивого принятия и не читает/шифрует повторно сообщение для отправителя. Ответ по умолчанию остается полным, WebSocket по-прежнему передает зашифрованные события.
2. Настоящий HTTP batch: группы до 16 сообщений / 8 MiB/емкости очереди, отдельный savepoint на элемент, FULL commit до ACK/Wake. Ошибки элементов изолированы; ошибка commit отклоняет предварительные успехи. Повтор operation ID сначала фиксирует предыдущую группу. Full-ответы проецируются по одному, чтобы не смешивать оптимизацию commit с выдачей. Низкая емкость очереди уменьшает группу; oversized single остается в настроенном лимите сообщения.
3. Idle: список подписанных чатов, SQL/args и scratch-map до 500 heads используются повторно, пока набор чатов не изменится. После ухода подписчиков буферы освобождаются на следующем poll. ACL/ключи не кешируются, lost-Wake и отзыв доступа сохранены.

## Методика

14 отдельных основных прогонов, 2 повтора каждого случая, 60 с cooldown. Single control/minimal — standard и subscribed; idle — subscribed; batch — прежний/новый endpoint с одинаковыми готовыми пакетами по 10 сообщений, subscribed. Контроли окружают новые варианты; После отбора выполняются 4 комбинированных прогона: single, batch, batch, single, subscribed и minimal ACK. Idle исключен и отменен коммитом `6d6610c`; комбинация использует тот же batch-only образ без idle-патча. Генератор и runner заморожены, сборки/тесты не пересекаются с основными замерами.

10 000 WS,10с idle,100 сообщений/с 30с +1000/с 5с,17-байтный HPKE payload. Standard — один подписанный получатель; subscribed — 5000 парных чатов / 10000 подписок, один активный чат. Более насыщенная нагрузка не добавляется. Для batch это 10/100 HTTP запросов/с: скорость считается в **сообщениях**. Задержка начинается до HPKE подготовки готового пакета; ожидание накопления пакета в приложении не измеряется. Элементы делят HTTP-ACK timestamp, WS-события измеряются отдельно. Batch нельзя напрямую сравнивать с распределением одиночных сообщений.

Apple M5 Pro/macOS 26.6.2, Docker Desktop Linux arm64 VM,4 CPU / 8 GiB, SQLite volume, общий хост. Выделенный Linux/SSD и TLS не измеряются. CPU 100%=одно ядро, RAM — sampled max. Docker stats примерно каждые2с; idle CPU без первого sample уменьшает погрешность границы, но остается приблизительным и имеет мало точек. Среднее p95/p99 прогонов не является объединенным перцентилем,2 повтора не дают доверительного интервала. Равенство счетчиков не заменяет exact-ID/расшифровку/persistent-device-ACK.

## Локальная стоимость poll

Одинаковый приватный fixture, 5000 неизменных heads, 5 warmup/100 calls ×3, естественный GC, без HTTP/WS и фонового poll worker. Go 1.26.4/macOS отличается от Docker Go 1.26.8. Все heads проверены, лишних уведомлений нет.

| На poll | До | После |
|---|---:|---:|
| Время | 5,574мс | 5,406мс |
| Выделено | 1281829байт | 539987байт |
| Allocations | 35217,6 | 20156,5 |

Выделенные байты−57,9%, число allocations−42,8%. Время−3,0%, но диапазоны пересекаются: уверенный локальный результат — экономия выделений, а не скорость API.

## Проверки

После исправления embedding-контекста прошли minimal/full 13 features/profile `go test -race`, `go vet ./...`, матрица 28/7, пример Ed25519/WS/HPKE/receipt, проверка207 генератора и smoke. Исходный full-profile failure в AI fixtures с отмененным фоновым контекстом выявил регрессию: восстановлен прежний caller-owned context только для Core без writer, AI tests не менялись, затем прошли 5 race повторов и весь full profile. Ревью и инспекция выполнены до source-коммитов. Проверены commit failure/нет раннего ACK/Wake, partial rollback, повторы/конфликты ID, низкая емкость/переполнение, cancellation/shutdown, stable/churn/release poll, отзыв устройств и потерянные уведомления.

[Команды воспроизведения](performance-three.md#reproduction). `--response-mode minimal` включает компактный ответ; `--batch-size 10` — готовые пакеты. Основные замеры выполняются без профилирования.

## Результаты

18 основных прогонов; 143,775 предложено = принято = событий = история. Ошибок, backpressure, пропусков генератора и неожиданных отключений нет. Строки — среднее двух прогонов относительно full-ответа dev `2ce7077` при том же размере batch. Отрицательное изменение означает снижение.

| Variant | Steady delivery p95, ms | p99, ms | Δ p99 | Steady CPU, % | Δ CPU | Δ RAM | Whole-run Δ p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| ACK / standard | 5.137 | 12.834 | +76.3% | 12.45 | -3.3% | +0.7% | +8.4% |
| ACK / subscribed | 4.967 | 7.037 | -9.2% | 15.49 | -7.5% | +3.6% | -1.5% |
| Idle / subscribed | 4.941 | 6.860 | -11.5% | 16.41 | -2.0% | +5.0% | +38.8% |
| Batch / subscribed | 15.655 | 17.991 | -23.0% | 11.24 | -12.9% | +1.4% | -24.3% |
| Combined single | 4.855 | 7.932 | +2.4% | 15.30 | -8.6% | +3.2% | +2.2% |
| Combined batch | 15.079 | 17.017 | -27.2% | 11.19 | -13.3% | +3.4% | -27.7% |

## Каждый прогон

| Raw run | Accepted | Steady p95 / p99, ms | Whole p99, ms | Steady CPU, % | RAM max, MiB |
|---|---:|---:|---:|---:|---:|
| [next3-batch-before-subscribed-1.json](benchmarks/next3-batch-before-subscribed-1.json) | 7990 | 19.402 / 22.990 | 23.143 | 12.66 | 327.2 |
| [next3-batch-before-subscribed-2.json](benchmarks/next3-batch-before-subscribed-2.json) | 7980 | 19.694 / 23.747 | 21.167 | 13.14 | 339.0 |
| [next3-batch-subscribed-1.json](benchmarks/next3-batch-subscribed-1.json) | 7980 | 15.762 / 17.326 | 16.268 | 10.84 | 344.9 |
| [next3-batch-subscribed-2.json](benchmarks/next3-batch-subscribed-2.json) | 7980 | 15.547 / 18.656 | 17.296 | 11.64 | 330.4 |
| [next3-before-standard-1.json](benchmarks/next3-before-standard-1.json) | 7996 | 4.770 / 7.109 | 6.641 | 13.05 | 269.5 |
| [next3-before-standard-2.json](benchmarks/next3-before-standard-2.json) | 7993 | 4.880 / 7.446 | 6.671 | 12.71 | 279.7 |
| [next3-before-subscribed-1.json](benchmarks/next3-before-subscribed-1.json) | 7994 | 4.876 / 7.291 | 6.433 | 15.94 | 330.6 |
| [next3-before-subscribed-2.json](benchmarks/next3-before-subscribed-2.json) | 7987 | 5.030 / 8.203 | 7.458 | 17.55 | 322.4 |
| [next3-combined-batch-subscribed-1.json](benchmarks/next3-combined-batch-subscribed-1.json) | 7980 | 15.442 / 17.113 | 16.310 | 11.31 | 340.7 |
| [next3-combined-batch-subscribed-2.json](benchmarks/next3-combined-batch-subscribed-2.json) | 7980 | 14.715 / 16.921 | 15.710 | 11.07 | 348.2 |
| [next3-combined-single-subscribed-1.json](benchmarks/next3-combined-single-subscribed-1.json) | 7989 | 5.142 / 8.501 | 7.633 | 15.62 | 351.3 |
| [next3-combined-single-subscribed-2.json](benchmarks/next3-combined-single-subscribed-2.json) | 7991 | 4.568 / 7.364 | 6.560 | 14.98 | 322.9 |
| [next3-idle-subscribed-1.json](benchmarks/next3-idle-subscribed-1.json) | 7984 | 4.954 / 6.747 | 7.404 | 15.32 | 349.2 |
| [next3-idle-subscribed-2.json](benchmarks/next3-idle-subscribed-2.json) | 7992 | 4.927 / 6.972 | 11.877 | 17.50 | 336.4 |
| [next3-minimal-standard-1.json](benchmarks/next3-minimal-standard-1.json) | 7990 | 5.505 / 18.085 | 7.730 | 13.17 | 276.5 |
| [next3-minimal-standard-2.json](benchmarks/next3-minimal-standard-2.json) | 7992 | 4.770 / 7.582 | 6.695 | 11.73 | 276.8 |
| [next3-minimal-subscribed-1.json](benchmarks/next3-minimal-subscribed-1.json) | 7984 | 4.958 / 6.903 | 6.735 | 14.98 | 344.9 |
| [next3-minimal-subscribed-2.json](benchmarks/next3-minimal-subscribed-2.json) | 7993 | 4.976 / 7.170 | 6.945 | 16.01 | 331.4 |

[Summary](benchmarks/next3-summary.json) · [Image/source provenance](benchmarks/next3-provenance.json) · [Native poll microbenchmark](benchmarks/next3-poll-micro.json).

## Что оставляем

Оставляем ограниченный общий commit HTTP batch. Компактный ACK остается опциональным: отдельно subscribed дал выигрыш, standard — нет. Сочетание для готовых батчей снизило steady p95/p99 на 22,9% / 27,2%, ACK p99 на 42,2%, CPU на 13,3%; sampled RAM выросла на 3,4% относительно прежнего последовательного batch/full. Относительно нового batch/full добавление minimal ACK дало еще −5,4% delivery p99 и−17,4% ACK p99. Разница CPU лишь −0,5%: дополнительный выигрыш CPU не доказан.

Для одиночных сообщений сочетание дало CPU−8,6%, но delivery p99+2,4% и RAM+3,2%: общего ускорения доставки нет. Idle дал idle CPU−22,2% и меньше allocations, но whole-run p99+38,8% и RAM+5,0%; патч отменен в `6d6610c`. FULL, ACL и зашифрованные WS-события сохранены. Ответ по умолчанию остается полным.

Это локальные результаты относительно dev `2ce7077`, а не доказательство превосходства над main или альтернативами. Main остается `2917865`; кандидат в dev до сравнения с main и проверки на выделенном Linux/SSD. Далее полезны чередующиеся парные контроли для оценки разброса одиночных сообщений и компромисса batch size/ACK; более насыщенная нагрузка — после отдельного указания. Новая насыщенная нагрузка не запускалась.
