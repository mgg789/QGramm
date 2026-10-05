# Пакетная выдача полного ответа и предварительные SQL-чтения

[English](performance-batch-reads.md).

## Изменения

Контроль — точный dev `20c3932`: общий FULL commit HTTP batch уже включен, idle-буферы отменены. Во всей серии полный HTTP-ответ; компактный ACK не добавляет отдельную переменную. Main `2917865` здесь не измеряется.

1. Пакетная выдача (`cf2f1ed`): до 16 разных message ID одним ACL-запросом и один раз разобранный ключ получателя на группу. Порядок сохраняется. При Project hooks, повторяющихся ID или ошибке групповой выдачи используется прежний поэлементный путь: отдельные ошибки, эффекты hooks и независимые HPKE-конверты. Send и minimal ACK не меняются.
2. Предварительные чтения: ограниченный снимок operation hash/result и состояния чата/устройства, до 16 элементов и емкости очереди. Flush сбрасывает снимок; повтор operation ID завершает предыдущую группу перед новой проверкой. При Prepare/InTransaction hooks прежний путь и порядок вызовов сохраняются. Свежие ACL, отзыв устройства/пользователя, epoch/pending и dedup/conflict внутри транзакции не меняются. Снимок не разрешает commit и не кешируется между запросами.

Тестовые счетчики: 19 разных full-ответов → 2 чтения; 34 новых minimal-ACK элемента → 6 предварительных чтений. Это отдельные тесты, а не доказательство снижения CPU сервиса. FULL, automatic checkpoint, bindings и поэлементный207 сохранены; PASSIVE и Redis в замерах отсутствуют.

## Методика

Образы до измерения: точный контроль и два независимых наложения исходников. Генератор и runner прежние, заморожены. 60с cooldown, порядок: control1, projection1, prepare1, prepare2, projection2, control2. По 2 повтора; среднее перцентилей отдельных прогонов не является объединенным перцентилем или доверительным интервалом. Комбинация проверяется после отбора обоих отдельных кандидатов.

10 000 WS, 10с idle,100 сообщений/с 30с +1000/с 5с. Subscribed: 5000 парных чатов/10000 подписок, один активный чат. Во всех случаях готовые batch по 10 сообщений, full-ответы, 17-байтный HPKE plaintext. Rate считается в сообщениях: 10/100 HTTP-запросов/с. Задержка начинается до подготовки готового batch; ожидание накопления batch не измеряется. Насыщенная нагрузка не добавляется. Перцентили только успешных запросов; отказы показываются отдельно.

Apple M5 Pro/macOS 26.6.2, генератор на общем хосте, Docker Desktop Linux arm64 VM/4 CPU / 8 GiB, SQLite volume, без TLS. Выделенный Linux/SSD и аудит криптографии отложены. CPU 100%=одно ядро, RAM — sampled max. Docker stats примерно раз в 2с, погрешность фаз сохраняется. Сборки, race-тесты и профилировщики не пересекаются с основными прогонами. Равенство счетчиков не заменяет exact-ID/расшифровку/persistent-device-ACK; контракт проверяется отдельными тестами.

[Воспроизведение](performance-batch-reads.md#reproduction).

## Результаты

6 основных прогонов; 47,890 предложено = принято = событий = история. Ошибок, backpressure, пропусков генератора и неожиданных отключений нет. Строки — среднее двух прогонов относительно full-ответа dev `20c3932` при том же размере batch. Отрицательное изменение означает снижение.

| Variant | Steady delivery p95, ms | p99, ms | Δ p99 | Steady CPU, % | Δ CPU | Δ RAM | Whole-run Δ p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Control / full | 15.159 | 17.988 | — | 11.71 | — | — | — |
| Grouped full response | 15.201 | 18.669 | +3.8% | 12.04 | +2.8% | -0.7% | +5.2% |
| Preliminary reads | 15.130 | 18.400 | +2.3% | 11.43 | -2.3% | -2.1% | +1.5% |

## Каждый прогон

| Raw run | Accepted | Steady p95 / p99, ms | Whole p99, ms | Steady CPU, % | RAM max, MiB |
|---|---:|---:|---:|---:|---:|
| [batchreads-before-subscribed-1.json](benchmarks/batchreads-before-subscribed-1.json) | 7980 | 15.002 / 17.069 | 16.162 | 11.74 | 345.5 |
| [batchreads-before-subscribed-2.json](benchmarks/batchreads-before-subscribed-2.json) | 7980 | 15.316 / 18.907 | 16.797 | 11.67 | 337.4 |
| [batchreads-prepare-subscribed-1.json](benchmarks/batchreads-prepare-subscribed-1.json) | 7980 | 14.652 / 18.211 | 16.462 | 11.06 | 334.5 |
| [batchreads-prepare-subscribed-2.json](benchmarks/batchreads-prepare-subscribed-2.json) | 7990 | 15.609 / 18.589 | 17.007 | 11.80 | 334.2 |
| [batchreads-projection-subscribed-1.json](benchmarks/batchreads-projection-subscribed-1.json) | 7980 | 14.783 / 19.489 | 18.020 | 12.13 | 336.5 |
| [batchreads-projection-subscribed-2.json](benchmarks/batchreads-projection-subscribed-2.json) | 7980 | 15.620 / 17.850 | 16.660 | 11.94 | 341.3 |

[Summary](benchmarks/batchreads-summary.json) · [Image/source provenance](benchmarks/batchreads-provenance.json).

## Отбор

Ни один вариант не показал убедительного ускорения на этой нагрузке. Пакетная выдача: средний steady delivery p99+3,8%, whole-run p99+5,2%, CPU+2,8%, RAM−0,7%. Предварительные чтения: CPU−2,3%, RAM−2,1%, но steady delivery p99+2,3%, whole-run p99+1,5% и ACK p95+4,0%. Контрольный steady p99 колебался 17,069–18,907мс; небольшие отличия не доказывают причинную регрессию или статистическую значимость. Снижение тестовых счетчиков SQL само по себе не оправдывает дополнительную сложность.

Выдача `cf2f1ed` отменена, предварительные чтения оставались в изолированной копии и в рабочий core не перенесены. Оба варианта можно воспроизвести: выдачу из этого commit, предварительные чтения через [архивный patch](benchmarks/batchreads-prepare.patch) на точный `20c3932`. [Provenance](benchmarks/batchreads-provenance.json) и [summary](benchmarks/batchreads-summary.json) фиксируют исходники и hash patch. Итоговый core совпадает с `20c3932`; main остается `2917865`.

Комбинация не запускалась: условие пользователя об успешности обоих отдельных вариантов не выполнено. Это не доказывает невозможность выигрыша комбинации. Более насыщенная нагрузка отложена по запросу; ослабление FULL/ACL/retry/шифрования не применялось.

## Проверки

Для выдачи прошли minimal/full 13-feature `go test -race ./...`, `go vet ./...`, матрица 28 сборок/7 ошибочных сочетаний. Для предварительных чтений прошли narrow default/profile race, полный core race, full 13-feature suite и повтор полного core после исправления hook boundary. Тесты: ограниченные SQL-чтения, порядок/расшифровка full-ответов, независимые envelopes повторов, partial errors, hooks/revoke, concurrent dedup/conflict, pending/epoch, поврежденный saved result, SQL error и прежний порядок prepare-before-flush на границе группы. Независимое ревью одобрило оба варианта до основных замеров. Итоговый core восстановлен, экспериментальные изменения не приняты.
