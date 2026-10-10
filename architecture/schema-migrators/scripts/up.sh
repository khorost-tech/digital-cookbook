#!/usr/bin/env bash
#
# up.sh — сеть, образы инструментов, PostgreSQL и CockroachDB.
# Picodata здесь не поднимается: run.sh пересоздаёт её перед каждым инструментом.
#
#   bash scripts/up.sh

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

command -v docker >/dev/null || fail "docker не найден"
docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null

log "сборка smig-goose, smig-liquibase и smig-alembic…"
docker build -q -t smig-goose tools/goose >/dev/null
docker build -q -t smig-liquibase tools/liquibase >/dev/null
docker build -q -t smig-alembic tools/alembic >/dev/null

# Atlas отказывается применять каталог миграций без atlas.sum — файла
# контрольных сумм. Он генерируется из самих миграций и лежит рядом с ними.
for d in migrations invalid partial; do
    docker run --rm -v "$PWD/tools/atlas/$d:/mig" arigaio/atlas:1.3.3-community \
        migrate hash --dir file:///mig >/dev/null
done

docker compose -f compose/pg.yml up -d >/dev/null
docker compose -f compose/crdb.yml up -d >/dev/null
for i in $(seq 1 60); do
    [ "$(q pg postgres 'SELECT 1')" = 1 ] && [ "$(q crdb defaultdb 'SELECT 1' | tail -1)" = 1 ] && break
    sleep 2
done
[ "$(q pg postgres 'SELECT 1')" = 1 ] || fail "PostgreSQL не отвечает"
[ "$(q crdb defaultdb 'SELECT 1' | tail -1)" = 1 ] || fail "CockroachDB не отвечает"
log "готово: PostgreSQL и CockroachDB отвечают; дальше bash scripts/control.sh"
