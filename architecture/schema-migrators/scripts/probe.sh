#!/usr/bin/env bash
#
# probe.sh — фактические версии СУБД и инструментов и отдельные снимки того,
# что зависит от редакции. Всё, что статья говорит о версиях, берётся отсюда.
#
#   bash scripts/probe.sh   → fixtures/00-probe.txt
#
# Picodata должна быть запущена: после полной матрицы она остаётся поднятой,
# иначе — docker compose -f compose/pico.yml up -d.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=00-probe
fixture_header "$f"
capture "$f" "PostgreSQL" "docker run --rm --network smig postgres:18.4 psql 'postgres://postgres:smig@pg:5432/postgres' -tAc 'SELECT version()'"
capture "$f" "CockroachDB" "docker run --rm --network smig postgres:18.4 psql 'postgres://root@crdb:26257/defaultdb?sslmode=disable' -tAc 'SELECT version()'"
capture "$f" "Picodata" "docker exec smig-pico picodata --version"
capture "$f" "goose" "docker run --rm smig-goose --version"
capture "$f" "Flyway" "docker run --rm flyway/flyway:13.9.0 version"
capture "$f" "Liquibase" "docker run --rm -e LIQUIBASE_ANALYTICS_ENABLED=false smig-liquibase --version"
capture "$f" "Atlas" "docker run --rm arigaio/atlas:1.3.3-community version"
capture "$f" "Alembic и драйверы" "docker run --rm --entrypoint python smig-alembic -c \"import alembic, sqlalchemy, psycopg; print('alembic', alembic.__version__, '| SQLAlchemy', sqlalchemy.__version__, '| psycopg', psycopg.__version__)\""

# Редакции: что отличается в бесплатной поставке. Без фильтров: фикстура
# хранит полный вывод и настоящий код возврата инструмента.
capture "$f" "Liquibase 5.0.4: официальный образ без драйвера" "docker run --rm --network smig -e LIQUIBASE_ANALYTICS_ENABLED=false liquibase/liquibase:5.0.4 --url='jdbc:postgresql://pg:5432/postgres' --username=postgres --password='smig' status --changelog-file=none.sql"
capture "$f" "Atlas community: migrate down" "docker run --rm arigaio/atlas:1.3.3-community migrate down --help"
log "записано: $FIXTURES_DIR/$f.txt"
