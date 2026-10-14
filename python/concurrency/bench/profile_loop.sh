#!/bin/sh
# Цикл профилирования ЦЕЛИКОМ внутри контейнера.
#
# Раньше каждый круг делал четыре отдельных docker compose exec, и на семи
# кругах это давало 28 запусков клиента Docker из внешней оболочки. Такая
# цепочка оказалась хрупкой: оболочка молча умирала на середине, не оставляя
# ни строки в stderr. Один exec вместо двадцати восьми и надёжнее, и быстрее.
set -eu

ITERS="${ITERS:-3000}"
ROUNDS="${ROUNDS:-7}"
RATE="${RATE:-200}"
PY=/opt/py/gil/bin/python
OUT="${OUT:-/fixtures/profile.txt}"

cd /bench

r=1
while [ "$r" -le "$ROUNDS" ]; do
  base=$($PY profile_target.py "$ITERS" 2>&1 | grep -o 'elapsed_s=[0-9.]*')
  echo "круг=${r} режим=без_профайлера ${base}" >> "$OUT"

  cp=$($PY -m cProfile -o /tmp/prof.out profile_target.py "$ITERS" 2>&1 | grep -o 'elapsed_s=[0-9.]*')
  echo "круг=${r} режим=cProfile ${cp}" >> "$OUT"

  ps=$(/opt/py/gil/bin/py-spy record --format raw --rate "$RATE" --output /tmp/pyspy.raw -- $PY profile_target.py "$ITERS" 2>&1 | grep -o 'elapsed_s=[0-9.]*')
  echo "круг=${r} режим=py-spy ${ps}" >> "$OUT"

  # Доли снимаются в КАЖДОМ круге: по одной точке отличить эффект от
  # разброса невозможно, и на этом стенде это уже один раз обмануло.
  echo "круг=${r} доли=$($PY profile_report.py /tmp/prof.out /tmp/pyspy.raw)" >> "$OUT"

  echo "круг ${r} из ${ROUNDS} готов" >&2
  r=$((r + 1))
done
