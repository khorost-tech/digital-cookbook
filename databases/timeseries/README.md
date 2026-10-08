# Стенд timeseries

TimescaleDB, Prometheus и VictoriaMetrics на одних и тех же временных рядах — к статье
[«Time-series БД: TimescaleDB и VictoriaMetrics»](https://khorost.tech/databases/timeseries-databases/).

Стенд отвечает на вопросы, которые в документации обычно разбросаны по разным
страницам: что отсекает чанки в плане запроса и что это отсечение ломает; что
continuous aggregate показывает, а чего нет; сколько байт на диске стоит одна
точка и от чего это зависит; сколько памяти стоит один ряд; почему один и тот же
PromQL-запрос даёт в двух системах разные ответы; что есть в бесплатных сборках.

Всё, что попало в статью, — в [`FIXTURES.md`](FIXTURES.md) и в `fixtures/` вместе с
командой, давшей каждый вывод (строка `# cmd:`), и её кодом возврата.

## Состав

| Компонент | Образ |
|---|---|
| TimescaleDB 2.30.2 на PostgreSQL 18.6 | `timescale/timescaledb:2.30.2-pg18` (TSL) и `…-pg18-oss` (Apache-2.0) |
| Prometheus 3.15.0 | `prom/prometheus:v3.15.0`, приём по remote_write включён |
| VictoriaMetrics 1.153.0, single-node | `victoriametrics/victoria-metrics:v1.153.0` |
| Генератор `gen/` | Go: pgx `CopyFrom`, remote_write (protowire + snappy), OpenMetrics |

Все узлы — по одному экземпляру. Данные генератора детерминированы
(фиксированный seed) и привязаны к часу запуска, поэтому даты в фикстурах будут
другими, а соотношения — теми же.

## Требования

Docker с compose, около 4 ГБ памяти для самого тяжёлого сценария (04). Порты хоста
не публикуются — всё ходит внутри сети `tsdb`. Go на хосте не нужен: генератор
собирается в контейнере.

## Как запускать

```bash
bash scripts/up.sh               # образ генератора, TimescaleDB, Prometheus, VictoriaMetrics
bash scripts/probe.sh            # версии
bash scripts/01-chunks.sh        # отсечение чанков; DELETE против drop_chunks
bash scripts/02-cagg.sh          # continuous aggregate: свежие, опоздавшие и удалённые данные
bash scripts/03-bytes.sh         # байт на точку по форме ряда в трёх системах
bash scripts/04-cardinality.sh   # память на ряд: 10 тыс. → 1 млн рядов
bash scripts/05-query.sh         # increase()/rate() в Prometheus и VictoriaMetrics
bash scripts/06-editions.sh      # что работает в -oss TimescaleDB и open source VictoriaMetrics
bash scripts/07-precision.sh     # точки читаются обратно: что вернули Prometheus и VictoriaMetrics
bash scripts/down.sh
```

Каждый сценарий сам пересоздаёт нужные ему сервисы с пустым хранилищем, поэтому
их можно запускать в любом порядке и по отдельности.

## Методика

- **Один набор точек — во все системы.** Генератор выдаёт одинаковые значения для
  TimescaleDB (COPY), Prometheus (OpenMetrics → `promtool tsdb create-blocks-from
  openmetrics`) и VictoriaMetrics (`/api/v1/import/prometheus` или remote_write).
- **Формы ряда (03):** `counter` — счётчик с целыми приращениями 0..19; `gauge2` —
  синусоида с шумом, округлённая до 0,01; `gauge` — та же синусоида без округления,
  полный float64; `const` — константа. 200 рядов × сутки × шаг 15 с = 1 152 000 точек.
- **Размер (03)** — данные вместе с индексом: у Prometheus сумма `SIZE` блоков из
  `promtool tsdb list`, у VictoriaMetrics сумма `vm_data_size_bytes` после
  `force_flush` и `force_merge`, у TimescaleDB `hypertable_size()`.
- **Память (04)** — собственные метрики процессов: `process_resident_memory_bytes`
  и `go_memstats_heap_inuse_bytes`, через 30 с после каждой ступени. У каждого ряда
  одна точка — это нижняя граница цены ряда.
- **Состояние хранилищ (03):** Prometheus — двухчасовые блоки от `promtool` без
  компакции сервером; VictoriaMetrics — после `force_flush` и `force_merge`. WAL,
  кеши и временные файлы в размер не входят.
- **Точность (07):** 200 рядов × 50 минут каждой формы пишутся по remote_write в
  Prometheus и VictoriaMetrics и читаются обратно запросом `demo_<форма>[3000s]`;
  сначала проверяется полнота ответа (статус, пропуски, лишние точки, дубликаты),
  затем каждая точка сравнивается с исходной побитно.
- **Запрос (05)** выполняется одной и той же командой `promtool query instant` в
  обе системы — VictoriaMetrics отвечает на Prometheus HTTP API.

## Чего стенд не делает

- Не меряет пропускную способность записи и чтения под нагрузкой.
- Не проверяет кластерные топологии (VictoriaMetrics cluster, мультиузловой
  TimescaleDB, HA-пары Prometheus) и долгосрочное хранение через Thanos/Mimir.
- Не проверяет платные функции (VictoriaMetrics Enterprise, Tiger Cloud) — только
  то, как бесплатные сборки на них отвечают.
- Не сравнивает с ClickHouse: это сделано на стенде
  [`databases/clickhouse`](../clickhouse/) (подкаталог `decision/`).
