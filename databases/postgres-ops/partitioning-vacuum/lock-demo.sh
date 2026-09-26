#!/usr/bin/env bash
# Живая демонстрация разницы блокировок. Пока идёт операция, в цикле шлём точечные
# SELECT-ы и считаем, сколько прошло и какова макс. задержка.
#   VACUUM FULL → блокирует чтение на ВСЮ операцию (одна проба висит до конца).
#   pg_repack   → читатели проходят почти всё время; задержку дают короткие служебные
#                 окна ACCESS EXCLUSIVE (setup в начале, swap в конце), а не вся операция.
# Запуск: ./lock-demo.sh  (контейнер должен быть поднят). ~2M строк, ~30 c.
set -uo pipefail
cd "$(dirname "$0")"
PSQL="docker compose exec -T postgres psql -U postgres -d opsdemo -qtAX"

prepare() {   # создать и раздуть таблицу. VACUUM нельзя внутри многооператорного -c
              # (транзакционный блок), да он тут и не нужен — нужен именно bloat.
  $PSQL -c "DROP TABLE IF EXISTS lock_demo;
            CREATE TABLE lock_demo (id bigint PRIMARY KEY, payload text)
                WITH (autovacuum_enabled=false);
            INSERT INTO lock_demo SELECT g, repeat('x',200) FROM generate_series(1,2000000) g;
            UPDATE lock_demo SET payload=payload||'y';" >/dev/null
}

# Пока жив процесс $1, шлём точечные SELECT-ы и печатаем: сколько прошло / макс. задержка.
probe() {
  local op_pid=$1 count=0 maxd=0
  while kill -0 "$op_pid" 2>/dev/null; do
    local start end d
    start=$(date +%s.%N)
    $PSQL -c "SELECT 1 FROM lock_demo WHERE id=1;" >/dev/null 2>&1
    end=$(date +%s.%N)
    d=$(awk -v s="$start" -v e="$end" 'BEGIN { printf "%.2f", e - s }')
    count=$((count + 1))
    awk -v d="$d" -v m="$maxd" 'BEGIN { exit !(d > m) }' && maxd=$d
    awk -v d="$d" 'BEGIN { exit !(d > 0.5) }' && echo "      [проба #${count}: ${d} c]"
  done
  echo "    проб прошло: ${count}, макс. задержка точечного SELECT: ${maxd} c"
}

echo "### VACUUM FULL — держит ACCESS EXCLUSIVE всю операцию"
prepare
$PSQL -c "VACUUM FULL lock_demo;" &
probe $!
wait

echo "### pg_repack — онлайн, блокирует лишь на короткий финальный swap"
prepare
docker compose exec -T postgres pg_repack -U postgres -d opsdemo -t lock_demo --no-order >/dev/null 2>&1 &
probe $!
wait
