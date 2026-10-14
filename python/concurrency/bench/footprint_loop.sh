#!/bin/sh
# Каждое плечо — В СВОЁМ процессе интерпретатора.
#
# Иначе forkserver, поднятый плечом процессов, переживает закрытие пула и
# попадает в следующие замеры. Здесь он умирает вместе со своим процессом,
# а перед каждым запуском проверяется, что посторонних python не осталось.
set -eu

BUILD="${BUILD:?}"
CONC="${CONC:-64}"
ROUNDS="${ROUNDS:-3}"
OUT="${OUT:-/fixtures/footprint-${BUILD}.txt}"
PY="/opt/py/${BUILD}/bin/python"

cd /bench

count_python() {
  n=0
  for d in /proc/[0-9]*; do
    if [ -r "$d/comm" ] && head -c 6 "$d/comm" 2>/dev/null | grep -q '^python'; then
      n=$((n + 1))
    fi
  done
  echo "$n"
}

r=1
while [ "$r" -le "$ROUNDS" ]; do
  # Порядок моделей сдвигается каждый круг.
  case $(( r % 3 )) in
    0) order="asyncio threads processes" ;;
    1) order="threads processes asyncio" ;;
    *) order="processes asyncio threads" ;;
  esac
  for mode in $order; do
    # Ждём, пока хвост предыдущего плеча уйдёт. forkserver усыновляется PID 1
    # и умирает в течение секунды после родителя, поэтому мгновенная проверка
    # ложно срабатывает. Но ждём ОГРАНИЧЕННО: если через 15 секунд процессы
    # ещё живы, это уже не гонка, а утечка, и мерить нельзя.
    waited=0
    while [ "$(count_python)" -ne 0 ]; do
      if [ "$waited" -ge 15 ]; then
        echo "ОТКАЗ: перед плечом ${mode} в контейнере $(count_python) процессов python не ушли за 15 с — это утечка, а не гонка" >&2
        exit 2
      fi
      sleep 1
      waited=$((waited + 1))
    done
    "$PY" run_footprint.py "$BUILD" "$mode" "$CONC" "$r" >> "$OUT"
  done
  echo "круг ${r} из ${ROUNDS} готов" >&2
  r=$((r + 1))
done
