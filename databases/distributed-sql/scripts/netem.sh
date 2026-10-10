#!/usr/bin/env bash
#
# netem.sh <движок> <RTT, мс> — задержка ТОЛЬКО между узлами кластера.
#
# В сетевом пространстве каждого узла вешается prio-дисциплина с четвёртой
# полосой, на ней netem с задержкой RTT/2, и фильтры u32 отправляют в эту
# полосу только пакеты к соседям по кластеру. Путь клиент → шлюз остаётся
# чистым: замер видит цену межузловых раунд-трипов, а не задержку до клиента.
#
# Задержка НАЗНАЧЕННАЯ, а не измеренная: так моделируются узлы в разных
# зонах (2 мс) или регионах (30 мс). Скрипт печатает фактический RTT до и после.
#
#   bash scripts/netem.sh crdb 30   # RTT 30 мс между узлами
#   bash scripts/netem.sh crdb 0    # снять задержку

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

engine="${1:-}"; rtt="${2:-}"
[ -n "$engine" ] && [ -n "$rtt" ] || fail "использование: netem.sh <движок> <RTT мс>"
require_running "$engine"
read -r -a nodes <<<"$(members "$engine")"
[ "${#nodes[@]}" -ge 2 ] || fail "$engine — одиночный сервер, задерживать нечего"

# Половина RTT на исходящем трафике каждого узла, в микросекундах.
half_us=$(( rtt * 1000 / 2 ))

for n in "${nodes[@]}"; do
    script="tc qdisc del dev eth0 root 2>/dev/null || true"
    if [ "$rtt" -gt 0 ]; then
        script+="; tc qdisc add dev eth0 root handle 1: prio bands 4"
        script+="; tc qdisc add dev eth0 parent 1:4 handle 40: netem delay ${half_us}us"
        for p in "${nodes[@]}"; do
            [ "$p" = "$n" ] && continue
            script+="; tc filter add dev eth0 protocol ip parent 1:0 prio 1 u32 match ip dst $(ip_of "$p")/32 flowid 1:4"
        done
    fi
    docker run --rm --net "container:$n" --cap-add NET_ADMIN "$NETEM_IMAGE" sh -c "$script"
done
log "назначен RTT ${rtt} мс между: ${nodes[*]}"

# Контроль: узел → узел (должен вырасти) и клиент → шлюз (должен остаться прежним).
a="${nodes[0]}"; b="${nodes[1]}"; gw="${nodes[${#nodes[@]}-1]}"
[ "$engine" = crdb ] && gw=crdb1
[ "$engine" = yb ] && gw=yb1
printf '%s -> %s: ' "$a" "$b"
docker run --rm --net "container:$a" "$NETEM_IMAGE" ping -c 5 -q "$(ip_of "$b")" | tail -1
printf 'клиент -> %s: ' "$gw"
docker run --rm --network "$NET" "$NETEM_IMAGE" ping -c 5 -q "$(ip_of "$gw")" | tail -1
