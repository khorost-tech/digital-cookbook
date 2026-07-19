# db-indexes — индексы в базах данных (стенд)

Живые стенды к серии «Индексы в базах данных». Docker.

## PostgreSQL (флагман доказательств)
    cd postgres && docker compose up -d
    ./run.sh sql/00-schema.sql        # схема + 2M строк
    ./run.sh sql/01-scan.sql          # seq vs index scan
    # ... остальные эксперименты

Каждый sql-скрипт печатает реальный EXPLAIN (ANALYZE). Версии — см. docker-compose.yml.

## Версии (зафиксированы, проверено 2026-07)

| Компонент | Версия |
|---|---|
| PostgreSQL | 18.4 |
| MongoDB | 8.2.11 |
| Tarantool | 3.7.0 (tt CLI 2.12.0 в образе) |
| Go | 1.26.3 |
| github.com/jackc/pgx/v5 | v5.10.0 |
| JDK | 21 (Temurin) |
| Maven | 3.9 |
| org.hibernate.orm:hibernate-core | 6.6.15.Final |
| org.postgresql:postgresql (JDBC) | 42.7.4 |

Проверено на фактическом тулчейне 2026-07: PostgreSQL 18.4 / MongoDB 8.2.11 / Tarantool 3.7.0.
