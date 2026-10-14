#!/usr/bin/env bash
# Чего стоит одновременность: память и дескрипторы под одинаковой
# IO-нагрузкой. Без этого слово «дешевле» было бы выводом сверх измеренного.
#
# Каждое плечо запускается в СВОЁМ процессе интерпретатора: forkserver от
# плеча процессов иначе переживает закрытие пула и попадает в следующие
# замеры. Цикл живёт в контейнере, см. bench/footprint_loop.sh.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

CONC="${CONC:-64}"
ROUNDS="${ROUNDS:-3}"

for build in gil nogil; do
  out="fixtures/footprint-${build}.txt"
  {
    echo "# След одновременности, сборка: ${build}"
    echo "# одновременность=${CONC} rounds=${ROUNDS} задержка=0.02s"
    echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "# среда: ${PREFLIGHT}"
    echo "#"
  } > "$out"
docker compose exec -T -e BUILD="$build" -e CONC="$CONC" -e ROUNDS="$ROUNDS" -e OUT="/fixtures/footprint-${build}.txt" bench sh /bench/footprint_loop.sh
  echo "готово: $out"
done
