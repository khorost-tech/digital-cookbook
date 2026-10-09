#!/usr/bin/env bash
#
# place-leaders.sh <движок> — разнести лидеров четырёх диапазонов kv по
# узлам, НЕ являющимся шлюзом: диапазоны 0 и 2 — на второй узел, 1 и 3 —
# на третий. Печатает каждую выполненную команду.
#
# Зачем: у TiDB шлюз (tidb-сервер) лидеров не держит в принципе. Чтобы
# сравнение было честным, у всех движков путь операции один и тот же:
# клиент → шлюз → лидер на другом узле.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: crdb | yb | tidb"
require_running "$e"

run() { echo "\$ $*"; bash -c "$*" | sed 's/^/  /'; }

YBA="docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100"
case "$e" in
tidb)
    echo "# TiDB: шлюз — отдельный tidb-сервер без данных, лидеры регионов живут"
    echo "# только на TiKV. Путь клиент → шлюз → лидер сетевой при любой раскладке,"
    echo "# переносить нечего."
    exit 0 ;;
yb)
    # Балансировщик YugabyteDB выравнивает лидеров по ВСЕМ узлам и вернул бы
    # часть на yb1. Предпочтительные зоны z2 и z3 держат лидеров вне шлюза
    # штатно, а leader_stepdown даёт точную раскладку внутри них.
    run "$YBA set_preferred_zones local.r1.z2 local.r1.z3"
    ;;
esac

for s in 0 1 2 3; do
    k=$(( s * 1000000 + 1 ))
    if [ $(( s % 2 )) -eq 0 ]; then target=2; else target=3; fi
    case "$e" in
    crdb)
        rid="$(docker exec crdb1 cockroach sql --insecure --host=localhost:26257 --format=tsv \
               -e "SELECT range_id FROM [SHOW RANGE FROM TABLE kv FOR ROW ($k)]" | tail -1)"
        nid="$(docker exec crdb1 cockroach node status --insecure --host=localhost:26257 --format=tsv \
               | awk -F'\t' -v h="crdb$target:26257" 'NR>1 && $2==h {print $1}')"
        echo "# диапазон $s (ключ $k): range $rid → crdb$target (node_id $nid)"
        run "docker exec crdb1 cockroach sql --insecure --host=localhost:26257 -e 'ALTER RANGE $rid RELOCATE LEASE TO $nid'"
        ;;
    yb)
        line="$($YBA list_tablets ysql.yugabyte kv | sed -n "$(( s + 2 ))p")"
        tablet="$(echo "$line" | awk '{print $1}')"
        cur="$(echo "$line" | grep -o 'yb[0-9]:9100' | head -1 | cut -d: -f1)"
        ts="$($YBA list_all_tablet_servers | awk -v h="yb$target:9100" '$2==h {print $1}')"
        echo "# диапазон $s (ключ $k): таблетка $tablet, лидер на $cur → yb$target"
        if [ "$cur" = "yb$target" ]; then echo "  уже там"; else run "$YBA leader_stepdown $tablet $ts"; fi
        ;;
    *) fail "перенос лидеров для $e не реализован" ;;
    esac
done
