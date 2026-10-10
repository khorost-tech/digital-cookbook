#!/usr/bin/env bash
#
# 05-failover.sh <движок> — непрерывная запись в один ключ; через 10 с
# гасится (docker kill, без корректного завершения) узел, на котором живёт
# лидер диапазона этого ключа. Шлюз, к которому подключён клиент, не трогаем:
# меряем переизбрание лидера, а не переподключение клиента.
#
#   bash scripts/05-failover.sh crdb   → fixtures/05-failover-crdb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
case "$e" in crdb|yb|tidb) ;; *) fail "только для кластеров: crdb, yb, tidb" ;; esac
require_running "$e"
f="05-failover-$e"
fixture_header "$f" "$e"

# leader_of <ключ> — контейнер, где сейчас лидер (leaseholder) диапазона ключа.
leader_of() {
    local k="$1"
    case "$e" in
    crdb)
        local id
        id="$(docker exec crdb1 cockroach sql --insecure --host=localhost:26257 --format=tsv \
              -e "SELECT lease_holder FROM [SHOW RANGE FROM TABLE kv FOR ROW ($k)]" | tail -1)"
        docker exec crdb1 cockroach node status --insecure --host=localhost:26257 --format=tsv \
            | awk -F'\t' -v id="$id" 'NR>1 && $1==id { split($2, a, ":"); print a[1] }'
        ;;
    yb)
        # Строка таблетки: UUID, диапазон в DocKey, адрес лидера. Ищем таблетку,
        # чья нижняя граница — наибольшая из не превышающих ключ.
        docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 \
            list_tablets ysql.yugabyte kv 0 \
          | awk -v k="$k" 'NR>1 {
                lo = -1
                if (match($0, /\[DocKey\(\[\], \[[0-9]+\]\)/)) { s = substr($0, RSTART, RLENGTH); gsub(/[^0-9]/, "", s); lo = s + 0 }
                if (lo <= k && lo >= best) { best = lo; for (i = 1; i <= NF; i++) if ($i ~ /:9100$/) { split($i, a, ":"); host = a[1] } }
            } BEGIN { best = -2 } END { print host }'
        ;;
    tidb)
        local store
        store="$(docker run --rm --network "$NET" mysql:8.4 mysql -h tidb -P 4000 -u root -N -e "SHOW TABLE test.kv REGIONS" \
            | awk -F'\t' -v k="$k" '{
                lo = -1
                if (match($2, /_r_[0-9]+$/)) { lo = substr($2, RSTART + 3) + 0 }
                if (lo <= k && lo >= best) { best = lo; st = $5 }
            } BEGIN { best = -2 } END { print st }')"
        docker run --rm --network "$NET" mysql:8.4 mysql -h tidb -P 4000 -u root -N \
            -e "SELECT address FROM information_schema.tikv_store_status WHERE store_id = $store" | cut -d: -f1
        ;;
    esac
}

# Раскладка как в 01: лидеры вне шлюза. Ребалансировка могла её сдвинуть.
bash scripts/place-leaders.sh "$e" >/dev/null
sleep 5

gateway=crdb1; [ "$e" = yb ] && gateway=yb1; [ "$e" = tidb ] && gateway=tidb

key=""; victim=""
for s in 0 1 2 3; do
    k=$(( s * 1000000 + 1 ))
    l="$(leader_of "$k")"
    log "ключ $k: лидер на $l"
    if [ -n "$l" ] && [ "$l" != "$gateway" ]; then key="$k"; victim="$l"; break; fi
done
[ -n "$victim" ] || fail "все лидеры на шлюзе $gateway — перезапустите 01-distribution и повторите"

capture "$f" "кого гасим" "echo 'ключ $key, лидер его диапазона на $victim, клиент подключён к $gateway'"

docker rm -f dsql-failover >/dev/null 2>&1 || true
capture "$f" "запись под отказом лидера" \
    "docker run -d --name dsql-failover --network dsql $BENCH_IMAGE failover -engine $e -key $key -duration 40s >/dev/null; sleep 10; docker kill $victim >/dev/null; docker wait dsql-failover >/dev/null; docker logs dsql-failover; docker rm dsql-failover >/dev/null"

log "возвращаем $victim"
docker start "$victim" >/dev/null
sleep 15
log "записано: $FIXTURES_DIR/$f.txt"
