# pooling — пулинг соединений PostgreSQL (стенд)

Живой стенд к статье «Пулинг соединений PostgreSQL: PgBouncer и pgcat» (серия
«PostgreSQL в проде»). Docker.

Инфраструктурный пулер ПЕРЕД базой (не клиентский пул в приложении — тот разобран в
статье про надёжную работу с PostgreSQL из Go/Java/Rust). Три пулера бок о бок:
**PgBouncer**, **pgcat**, **Odyssey** — цена соединения, режимы пулинга, что ломает
transaction pooling, бенчмарки.

## Топология

    postgres:5432 (внутри) ← pgbouncer:6432 / pgcat:6433 / odyssey:6434
    хостовые порты: postgres 5436, pgbouncer 6432, pgcat 6433, odyssey 6434

Аутентификация упрощена (`trust` в изолированной docker-сети), клиент шлёт
`PGPASSWORD=pooldemo`. Все пулеры настроены на `pool_mode=transaction`, `pool_size=5`.

## Как запускать

    docker compose build && docker compose up -d
    ./run.sh sql/00-setup.sql               # тестовая таблица
    ./run.sh sql/01-connection-cost.sql     # соединение = процесс, max_connections
    ./conn-demo.sh                          # мультиплексирование: 50 клиентов → сколько server
    ./pool-modes.sh                         # session / transaction / statement — семантика
    ./break-prepared.sh                     # prepared: ломались раньше, решено в 1.25.2
    ./bench.sh                              # бенчмарк: перегрузка и переподключение

Go-демонстрации (собирать в хосте):

    cd cmd/break-prepared && go run .       # pgx + prepared через пулер
    cd cmd/listen-notify  && go run .       # LISTEN/NOTIFY теряется в transaction pooling

Снимки прогонов — в `fixtures/`.

## Что показывает стенд

- **цена соединения** — 50 клиентов дают 50 процессов напрямую и ~5 через пулер;
- **режимы** — statement-режим запрещает даже `BEGIN…COMMIT`; transaction — боевой;
- **поломки transaction pooling** — LISTEN/NOTIFY теряется (надёжно), сессионные SET
  обманчиво «работают» при sticky-server; prepared statements современный PgBouncer
  поддерживает штатно (`max_prepared_statements=200`), хотя исторически ломались;
- **бенчмарк** — при перегрузке база отказывает, пулер держит; переподключение вдвое дешевле.

## Версии (зафиксированы, проверено 2026-07)

| Компонент | Версия |
|---|---|
| PostgreSQL | 18.4 |
| PgBouncer | 1.25.2 |
| pgcat | 1.2.0 |
| Odyssey | 1.5.1 |
| Go / pgx | 1.26.3 / v5.10.0 |

Проверено на фактическом тулчейне 2026-07.
