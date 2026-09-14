#!/usr/bin/env bash
# Прогон http-transport по набору полос.
#
# Полосы задаются аргументами в Мбит/с и должны выбираться по обе стороны от
# расчётного порога B* (модуль analysis): смысл сценария — проверить, там ли
# фактический перелом, а не показать, что сжатие «работает».
#
# ВНИМАНИЕ, отклонение от исходного плана задачи. План предполагал ограничение
# полосы через `tc qdisc ... tbf` на интерфейсе контейнера-сервера (для этого
# в compose был предусмотрен NET_ADMIN). На этой машине (Docker Desktop для
# Windows, бэкенд WSL2, ядро 5.15.167.4-microsoft-standard-WSL2) это
# проверено и не работает: `tc qdisc add dev eth0 root tbf ...` и `... netem
# ...` возвращают "Specified qdisc kind is unknown", а
# /lib/modules/<ядро>/kernel/net/sched/ в этом ядре отсутствует целиком —
# sch_tbf/sch_netem не собраны ни встроенными, ни модулями, modprobe в
# контейнере тоже нет. Это ограничение конкретного окружения, а не общее
# свойство Linux.
#
# Взамен полоса ограничивается на уровне приложения — сервер (см.
# httpdemo/throttle.go) паузами выдерживает целевой темп записи тела ответа.
# Полоса передаётся клиенту флагом -bwmbit и пробрасывается на сервер
# параметром запроса. tc/netem здесь больше не используется.
set -euo pipefail
cd "$(dirname "$0")/.."

BANDWIDTHS="${*:-500 1000 1500 2000 2500 3000 3500 5000}"
REPS="${REPS:-5}"
OUT="${OUT:-scratchout/http-transport.csv}"

mkdir -p "$(dirname "$OUT")"
echo "bandwidth_mbit,encoding,wire_bytes,total_ms,reps" > "$OUT"

for bw in $BANDWIDTHS; do
    echo "== полоса ${bw} Мбит/с"
    docker exec compression-client sh -c \
        "cd /w && go run . -mode client -url http://compression-http:8080/products -reps ${REPS} -bwmbit ${bw}" \
        | sed "s/^/${bw},/" >> "$OUT"
done

echo "готово: $OUT"
