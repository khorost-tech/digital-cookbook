#!/usr/bin/env bash
# Контроль на постороннюю нагрузку: litestream тикает раз в секунду и участвует
# в чекпойнтах WAL рядом с SQLite-файлом всё время основных замеров, а у
# Postgres-плеч такого сайдкара нет. Этот скрипт повторяет ТОЛЬКО insert-сценарий
# при полной долговечности (тот же n, та же матрица гарантий, что в
# run-latency.sh) с ОСТАНОВЛЕННЫМ litestream — сравнение с основным прогоном
# показывает, объясняет ли сайдкар разрыв SQLite/Postgres или нет.
#
# Важно: использует ТУ ЖЕ сбалансированную ротацию (латинский квадрат порядка 3,
# см. run-latency.sh), что и основные прогоны — иначе сравнение контроля с
# основным прогоном снова шло бы через разные методики, что и было
# исходной претензией ревью.
#
# ПРЕДУСЛОВИЕ: litestream должен быть остановлен ДО запуска
# (`docker compose stop litestream`), остальные сервисы — живы. Этот скрипт
# сам litestream не трогает — так проще не перепутать порядок операций с
# другими сценариями, которые в это же время могут держать стенд поднятым.
set -euo pipefail
export MSYS_NO_PATHCONV=1

N="${N:-20000}"
SEED="${SEED:-777}"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT:-$SELF_DIR/../fixtures/latency-no-litestream.txt}"

run() { docker compose exec -T bench bench -arm "$1" -op insert -n "$N" -sync "$2" -pg-sync "$3"; }

ARM=(sqlite pg-uds pg-tcp)
SYNC=(FULL FULL FULL)
PGSYNC=(on on on)

# Тот же латинский квадрат, что в run-latency.sh: perm(n) = (n, n+1, n+2) mod 3.
perm() {
  local n="$1"
  local rot=$(( ((n % 3) + 3) % 3 ))
  local a=$rot b c
  b=$(( (rot + 1) % 3 ))
  c=$(( (rot + 2) % 3 ))
  echo "$a $b $c"
}

{
  echo "== контроль: litestream ОСТАНОВЛЕН, insert при полной долговечности, n=$N, ротация как в основном прогоне =="
  date -u +"прогон: %Y-%m-%dT%H:%M:%SZ"
  echo "seed: $SEED (та же схема ротации — латинский квадрат — что и в run-latency.sh)"
  for round in 1 2 3; do
    echo "-- круг $round --"
    permN="$(( SEED + round ))"
    order="$(perm "$permN")"
    echo "порядок: $order [индексы sqlite=0 pg-uds=1 pg-tcp=2]"
    for i in $order; do
      run "${ARM[$i]}" "${SYNC[$i]}" "${PGSYNC[$i]}"
    done
  done
} | tee "$OUT"

echo "фикстура записана: $OUT"
