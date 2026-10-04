#!/usr/bin/env bash
# Автоматический failover и замер RTO. Убиваем текущего лидера; Patroni через etcd
# выбирает нового и промоутит его; HAProxy переключает запись на него. Меряем время
# от гибели лидера до первой успешной записи (RTO). Затем возвращаем узел — он входит
# репликой (pg_rewind), а не вторым лидером: split-brain невозможен, лидер один в DCS.
# Запуск: ./failover-demo.sh
set -uo pipefail
cd "$(dirname "$0")"

# найти имя лидера через живую ноду
find_leader() {
  docker compose exec -T "$1" patronictl -c /etc/patroni/patroni.yml list -f json 2>/dev/null \
    | python3 -c "import sys,json;print(next(m['Member'] for m in json.load(sys.stdin) if m['Role']=='Leader'))"
}

leader=$(find_leader patroni1)
# управляющая нода — любая, кроме лидера (её patronictl/psql переживёт гибель лидера)
control=patroni1; [ "$leader" = "patroni1" ] && control=patroni2

echo "### текущий лидер: $leader (управляем через $control)"
docker compose exec -T "$control" patronictl -c /etc/patroni/patroni.yml list

echo
echo "### убиваем лидера $leader и замеряем RTO"
docker compose kill "$leader" >/dev/null 2>&1
start=$(date +%s.%N)
tries=0
while true; do
  if docker compose exec -T "$control" \
       psql "host=haproxy port=5000 user=postgres password=hapass dbname=postgres connect_timeout=1" \
       -tAc "INSERT INTO ha_demo(val) VALUES('after-failover')" >/dev/null 2>&1; then
    break
  fi
  tries=$((tries+1)); sleep 0.3
done
end=$(date +%s.%N)
rto=$(awk -v s="$start" -v e="$end" 'BEGIN{printf "%.1f", e-s}')
echo "    RTO = ${rto} c (запись снова проходит через HAProxy :5000)"

echo
echo "### новый расклад кластера (лидер сменился, один лидер в DCS)"
docker compose exec -T "$control" patronictl -c /etc/patroni/patroni.yml list

echo
echo "### возвращаем $leader — он входит РЕПЛИКОЙ, а не вторым лидером"
docker compose start "$leader" >/dev/null 2>&1
sleep 20
docker compose exec -T "$control" patronictl -c /etc/patroni/patroni.yml list
