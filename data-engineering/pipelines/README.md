# data-engineering/pipelines — батч-пайплайн против эталона

Стенд к статье [«Батч-пайплайны и оркестрация: ETL vs ELT, Airflow, dbt»](https://khorost.tech/databases/data-batch-pipelines-orchestration/).
PostgreSQL 18 — источник (`shop.app`) и хранилище (`dwh`: `raw`, `staging`,
`marts`); Airflow 3.3 (LocalExecutor) с dbt-core 1.12 и dbt-postgres 1.11 в
отдельном окружении. Генератор с фиксированным зерном строит магазин за 14 дней
(01–14.09.2026) и **сам** считает эталон — дневную выручку, число заказов и
позиций — по своим данным, а не по базе. Каждая ось сверяет хранилище с
эталоном: совпало ли, сколько строк лишних или потерянных. Время прогонов не
публикуется.

## Что внутри

```
compose.yml       PostgreSQL + Airflow (api-server, dag-processor, scheduler)
generator/gen.py  источник и эталон; поздние заказы с заданным опозданием
pipeline/load.py  выгрузка дня и загрузка в raw: append, delete-insert,
                  delete-batch, merge; искусственное падение посреди загрузки
airflow/dags/     elt_daily, etl_daily, interval_demo, sched_*, bf_*, retry_*
dbt/              staging, витрины daily_revenue и daily_revenue_inc
scripts/          раннеры осей, разборы с контролями, проверки, диверсии
fixtures/         замеры *.jsonl; manifests/ — сводки сессий; RUN-COMPLETE
expected.json     гипотезы до прогона и факты проб
```

## Как прогнать

Нужны Docker и Python 3.8+.

```sh
bash scripts/run-all.sh             # всё разом, маркер последним
FRESH=1 bash scripts/run-all.sh     # то же с пустых томов
```

Один вариант — одной командой (её показывает статья):

```sh
python3 scripts/run-elt.py elt                       # ось 1: ETL против ELT
python3 scripts/run-idempotency.py delete-batch crash-reload   # ось 2
python3 scripts/run-interval.py manual-trigger       # ось 3: интервал данных
python3 scripts/run-backfill.py fail-ordered         # ось 4: backfill
python3 scripts/run-late.py ingest-inc               # ось 5: поздние данные
python3 scripts/run-retries.py r3-append-k1          # ось 6: повторы
```

Проверки и диверсии:

```sh
python3 -m unittest discover -s scripts -p "test_*.py"
python3 scripts/sabotage.py
```

## Что замерено

Сырые выводы — в `fixtures/`; две сессии полного прогона (вторая — с пустых
томов) совпали по опубликованному (`fixtures/manifests/`).

**Ось 1 — ETL против ELT** (`elt.jsonl`). Три схемы: ETL с сохранением только
агрегата (`etl_daily`), ETL с подробными фактами заказа (`etl_facts_daily`),
ELT с сохранением raw (`elt_daily`). На правиле «выручка без отменённых» все
совпадают с эталоном во все 14 дней. Правило меняется — без возвратов. ELT
пересчитывает историю одним `dbt run` из `raw`, ETL с фактами — SQL над
фактом заказа: 0 запросов к источнику; ETL с агрегатом выгружает каждый день
заново: 28 запросов (счёт — `pg_stat_statements` базы источника). Все
результаты совпадают с эталоном нового правила: пересчёт без источника
определяет сохранённая детализация, а не место преобразования.

**Ось 2 — идемпотентность загрузки** (`idempotency.jsonl`). Повтор дня: `append`
удваивает 686 позиций, остальные режимы — эталон. Падение повторной загрузки
после 5 пакетов: `append` — 250 дублей, после ещё одного повтора 936;
`delete-insert` в одной транзакции — откат, день целый; `delete-batch` — день
наполовину пуст (250 из 686) до следующего повтора; `merge` — день целый.

**Ось 3 — интервал данных** (`interval.jsonl`). Задача по `data_interval_start`
при backfill обрабатывает каждый из 14 дней ровно раз; «вчера по часам» —
один день 14 раз, 13 дней — ни разу. В Airflow 3 `@daily` по умолчанию — без
интервала: обычный запуск в полночь получает начало интервала, равное моменту
срабатывания, — день, который только начался (с `CronDataIntervalTimetable` —
прошедшие сутки). Ручной `airflow dags trigger --logical-date` получает
`data_interval_start` = момент запуска, а не логическую дату.

**Ось 4 — backfill** (`backfill.jsonl`). `airflow backfill create` за 14 дней —
14 запусков, эталон; повтор без `--reprocess-behavior` — 0 новых запусков, с
`completed` — 14. Сбой 05.09: без `depends_on_past` — 13 успешно и 1 сбой; с
ним — 4, 1 и 9 невыполненных. Для DAG с `depends_on_past` backfill без
`--reprocess-behavior` отказан всегда — и при обработанной дате в диапазоне, и
на чистом диапазоне (`ordered-clean`); `--run-backwards` тоже.

**Ось 5 — поздние данные** (`late.jsonl`). 20 заказов за 05.09 приходят через
2 дня. Выгрузка по времени события теряет их; окно пересмотра в 1 день — тоже,
в 2 дня — находит. Выгрузка по времени поступления с витриной, пересчитываемой
целиком, — эталон; с инкрементальной витриной без окна — теряет 20, с окном или
полным пересчётом — эталон.

**Ось 6 — повторы** (`retries.jsonl`). Задача, падающая дважды, при
`retries=3` успешна на 3-й попытке, при `retries=1` — сбой на 2-й. Повтор
неидемпотентной загрузки оставляет 250 дублей, идемпотентной — эталон.

## Чего эти числа не значат

- **Ничего о скорости и масштабе.** 14 дней по ~300 заказов; один PostgreSQL.
- **Версии:** Airflow 3.3.2, dbt-core 1.12.5, dbt-postgres 1.11.0, PostgreSQL
  18.6. Поведение интервалов, ручного запуска и backfill — свойство Airflow 3;
  в Airflow 2 оно другое.
- **Время Airflow управляемо:** запуски — `airflow dags test` с логической
  датой, `airflow backfill create`, `airflow dags trigger`; реальное расписание
  опыт не ждёт. История стенда в прошлом (сентябрь 2026), чтобы backfill
  создавал все 14 запусков при любой дате прогона.
