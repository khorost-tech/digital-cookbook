#!/usr/bin/env bash
#
# up.sh <движок> — поднять профиль одного движка. Остальные профили (кроме
# pg и mysql — они маленькие) гасятся: три кластера одновременно в память
# Docker Desktop (~8 ГБ) не помещаются.
#
#   bash scripts/up.sh crdb    # CockroachDB, 3 узла
#   bash scripts/up.sh yb      # YugabyteDB, 3 узла, RF=3
#   bash scripts/up.sh tidb    # TiDB: PD + 3 TiKV + TiDB-сервер
#   bash scripts/up.sh ob      # OceanBase, ОДИН observer (mini)
#   bash scripts/up.sh pg      # PostgreSQL 17 — базовая линия
#   bash scripts/up.sh mysql   # MySQL 8.4 — эталон для 04-compat

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

engine="${1:-}"
[ -n "$engine" ] || fail "укажите движок: $ENGINES"
members "$engine" >/dev/null

command -v docker >/dev/null || fail "docker не найден"
docker info >/dev/null 2>&1 || fail "docker-демон не отвечает"

docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
log "сеть $NET есть"

# Образы стенда собираются один раз; при правке кода — bash scripts/up.sh --rebuild.
if ! docker image inspect "$BENCH_IMAGE" >/dev/null 2>&1 || [ "${REBUILD:-0}" = 1 ]; then
    log "сборка $BENCH_IMAGE…"; docker build -q -t "$BENCH_IMAGE" bench >/dev/null
fi
if ! docker image inspect "$NETEM_IMAGE" >/dev/null 2>&1; then
    log "сборка $NETEM_IMAGE…"; docker build -q -t "$NETEM_IMAGE" netem >/dev/null
fi

case "$engine" in
    crdb|yb|tidb|ob)
        for other in crdb yb tidb ob; do
            [ "$other" = "$engine" ] && continue
            if docker compose -f "$(compose_of "$other")" ps -q 2>/dev/null | grep -q .; then
                log "гасим профиль $other, чтобы освободить память"
                docker compose -f "$(compose_of "$other")" down -v >/dev/null 2>&1
            fi
        done ;;
esac

log "поднимаем $engine…"
docker compose -f "$(compose_of "$engine")" up -d >/dev/null

case "$engine" in
crdb)
    # До init `cockroach sql` не падает, а ждёт — поэтому крутим сам init:
    # пока узлы не слушают порт, он отвечает отказом соединения.
    out=""
    for i in $(seq 1 60); do
        out="$(docker exec crdb1 timeout 10 cockroach init --insecure --host=localhost:26257 2>&1 || true)"
        echo "$out" | grep -q -e 'successfully initialized' -e 'already been initialized' && break
        sleep 2
    done
    echo "$out" | grep -q -e 'successfully initialized' -e 'already been initialized' || fail "cockroach init: $out"
    log "crdb: кластер инициализирован"
    wait_until 120 "crdb: 3 живых узла" "[ \$(docker exec crdb1 cockroach node status --insecure --host=localhost:26257 --format=tsv | awk 'NR>1 && \$NF==\"true\"' | wc -l) -eq 3 ]"
    ;;
yb)
    wait_until 300 "yb: три узла отвечают" "for n in yb1 yb2 yb3; do docker exec \$n bin/ysqlsh -h \$n -c 'select 1' >/dev/null || exit 1; done"
    # yugabyted по умолчанию даёт RF=1, пока не попросят иначе. Зоны у узлов
    # разные, поэтому fault_tolerance=zone даёт RF=3 по реплике на узел.
    docker exec yb1 bin/yugabyted configure data_placement --fault_tolerance=zone --rf=3 >/dev/null 2>&1 \
        || log "configure data_placement вернул ошибку — проверим RF ниже"
    ;;
tidb)
    wait_until 240 "tidb: SQL отвечает" "docker run --rm --network $NET mysql:8.4 mysql -h tidb -P 4000 -u root -e 'select 1'"
    wait_until 120 "tidb: 3 TiKV в строю" "[ \$(docker run --rm --network $NET mysql:8.4 mysql -h tidb -P 4000 -u root -N -e \"select count(*) from information_schema.tikv_store_status where store_state_name='Up'\") -eq 3 ]"
    ;;
ob)
    log "OceanBase стартует несколько минут (bootstrap тенанта)…"
    wait_until 900 "ob: boot success" "docker logs ob 2>&1 | grep -q 'boot success'"
    ;;
pg)
    wait_until 60 "pg: healthy" "[ \$(docker inspect -f '{{.State.Health.Status}}' pg) = healthy ]" ;;
mysql)
    wait_until 120 "mysql: healthy" "[ \$(docker inspect -f '{{.State.Health.Status}}' mysql) = healthy ]" ;;
esac

docker stats --no-stream --format 'table {{.Name}}\t{{.MemUsage}}' $(members "$engine") >&2
log "готово. Дальше: bash scripts/probe.sh $engine"
