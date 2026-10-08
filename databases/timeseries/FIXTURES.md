# FIXTURES — что снято на стенде timeseries

Единственный источник фактов для статьи
[«Time-series БД: TimescaleDB и VictoriaMetrics»](https://khorost.tech/databases/timeseries-databases/).
Сырой вывод — в `fixtures/`, у каждой секции строка `# cmd:` с командой и код возврата.

**Дата прогона: 2026-10-05.** Машина: macOS, Docker Desktop 29.8.0 (arm64), VM Docker 7,7 ГБ.

## Версии (`fixtures/00-probe.txt`)

| Что | Версия |
|---|---|
| PostgreSQL | 18.6 (образ timescale/timescaledb, Alpine) |
| TimescaleDB | 2.30.2, лицензия `timescale`; для 06 — та же версия, `apache` |
| Prometheus | 3.15.0 |
| VictoriaMetrics | v1.153.0, single-node, open source |
| Генератор | Go, pgx v5.11.0, golang/snappy v1.0.0, protobuf v1.36.12 |

## 01 — гипертаблица и обычная таблица (`fixtures/01-chunks.txt`)

30 суток × 200 рядов × 1/мин = 8 640 000 строк; чанк — сутки, 31 чанк.

| Запрос | Чанков читается | Время |
|---|---|---|
| `time > now() - INTERVAL '1 hour'` | 1 (отсечено при планировании) | — |
| вчера: `time >= date_trunc('day', now()) - INTERVAL '1 day' AND time < date_trunc('day', now())` | 1 (`Chunks excluded during startup: 30`) | 30,476 мс |
| вчера: `date_trunc('day', time) = …` | 31 | 445,104 мс |
| вчера: `time::date = current_date - 1` | 31 | — |

Ретеншн по границе суток (одна и та же область): `DELETE` — 148 000 строк, 45,549 мс,
обычная таблица 971 МБ до и после, 148 000 мёртвых строк; `drop_chunks` — 1 чанк,
9,039 мс, гипертаблица 938 → 921 МБ. Строк осталось поровну: 8 492 000.

## 02 — continuous aggregate (`fixtures/02-cagg.txt`)

Гипертаблица `cpu`: 14 суток × 50 рядов × 1/мин, агрегат по часам.

- `materialized_only` по умолчанию — `t`. После refresh до h−2ч и ещё часа данных
  агрегат заканчивается на 08:00 (16 800 строк), сырые данные — на 09:00; с
  `materialized_only = false` — 09:00, 16 850 строк.
- Опоздавшие 60 точек со значением 1000 в часе h−3 сут: агрегат 39,51, сырые 519,76.
  Политика `start_offset => 2 days` + `run_job` — без изменений. Ручной refresh этого
  часа — 519,76 / 519,76.
- `drop_chunks` сырых старше 10 суток (4 чанка): строк агрегата в этой области 4350 до
  и 4350 после; после `refresh_continuous_aggregate(…, NULL, …)` — 0.
- Агрегат `WITH NO DATA` + только политика (`start_offset` 2 суток): 46 часов с
  2026-10-03 12:00 против 250 часов сырых данных с 2026-09-25.

## 03 — байт на точку (`fixtures/03-bytes.txt`, `bytes.tsv`, `bytes.md`)

200 рядов × сутки × 15 с = 1 152 000 точек; данные + индекс.

| Форма | Prometheus | VictoriaMetrics | TimescaleDB | TimescaleDB, сжатие |
|---|---|---|---|---|
| counter | 2,07 | 0,67 | 61,60 | 2,90 |
| gauge2 (0,01) | 7,29 | 1,54 | 61,60 | 8,94 |
| gauge (float64) | 7,30 | 6,46 | 61,60 | 8,94 |
| const | 0,67 | 0,03 | 61,60 | 0,63 |

У TimescaleDB размеры кратны странице 8 КБ; gauge2 и gauge со сжатием совпали с этой точностью.
Prometheus: двухчасовые блоки из
`promtool tsdb create-blocks-from openmetrics`, без последующей компакции.

## 04 — кардинальность (`fixtures/04-cardinality.txt`, `cardinality.tsv`)

`demo_requests{user_id=…}`, одна точка на ряд, снимок через 30 с после ступени.

| Рядов | Prometheus RSS, МБ | VictoriaMetrics RSS, МБ |
|---|---|---|
| 0 | 101 | 29 |
| 10 000 | 103 | 94 |
| 100 000 | 193 | 176 |
| 300 000 | 404 | 269 |
| 1 000 000 | 1099 | 347 |

`prometheus_tsdb_head_series` и `vm_new_timeseries_created_total` совпадают с числом
рядов на каждой ступени; `count(demo_requests)` в обеих — 1 000 000. Значения RSS
VictoriaMetrics шумные (кеши, GC), Prometheus — линейные, ≈1 КБ на ряд.

## 05 — increase() и rate() (`fixtures/05-query.txt`)

Один счётчик, час истории, шаг 15 с, `T` = через 7 с после точки.

| | Prometheus | VictoriaMetrics |
|---|---|---|
| значение в T | 1908 | 1908 |
| `offset 5m` | 1736 | 1736 |
| `increase(…[5m])` | 162,105… | 172 |
| `rate(…[5m])` | 0,5404 | 0,5733 |

Точки в окне `(T−5m, T]`: от 1754 до 1908, 20 штук, первая — через 8 с после начала
окна. 154 × 300 / 285 = 162,1.

## 06 — бесплатные сборки (`fixtures/06-editions.txt`)

- `-oss` TimescaleDB (`timescaledb.license = apache`): гипертаблица, `drop_chunks`,
  `time_bucket` — работают; continuous aggregate и сжатие — `functionality not
  supported under the current "apache" license`; `add_retention_policy` — `not
  supported under the current "apache" license`, подсказка называет её «free
  community feature».
- VictoriaMetrics open source: `-downsampling.period`, `-retentionFilter` — `flag
  provided but not defined` (код 2).
- Prometheus: в `--help` нет ни одного упоминания downsampling.

## 07 — что возвращается обратно (`fixtures/07-precision.txt`)

200 рядов × 50 минут × 15 с = 40 000 точек каждой формы, remote_write → чтение
`demo_<форма>[3000s]` → сравнение с генератором. Во всех шести проверках ответ полон: статус
`success`, 40 000 пар «ряд — время» без пропусков, лишних точек и дубликатов; сравнение
побитное (`math.Float64bits`).

| Форма | Prometheus | VictoriaMetrics |
|---|---|---|
| gauge (float64) | 40 000 из 40 000 побитно | 20 из 40 000 побитно, max относительная ошибка 1e-12 (абсолютная 7,57e-11) |
| gauge2 (0,01) | 40 000 побитно | 40 000 побитно |
| counter | 40 000 побитно | 40 000 побитно |

Размер gauge в VictoriaMetrics из 03 (6,46 байта на точку) получен при хранении с этой
потерей точности; её вклад в размер отдельно не измерялся; Prometheus вернул float64 без потерь. TimescaleDB обратным чтением не проверялась.

## Не проверялось

Пропускная способность записи и чтения под нагрузкой; кластерные топологии;
Thanos/Mimir; платные функции (VictoriaMetrics Enterprise, Tiger Cloud); компакция
блоков Prometheus после backfill.
