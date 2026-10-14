#!/usr/bin/env bash
# CPU-нагрузка: три модели исполнения на двух сборках Python.
#
# Порядок моделей внутри каждого круга задаёт латинский квадрат, а не
# фиксированный список: при фиксированном порядке первая модель систематически
# получает более холодную машину, и это смещение не убирается повторами.
# За три круга каждая модель занимает каждую позицию ровно один раз.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

# Среда проверяется до замера, а не предполагается чистой.
# Результат проверки идёт в шапку фикстуры: чистота среды — часть
# происхождения чисел, а не устная гарантия.
PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

SEED="${SEED:-0}"
COUNT="${COUNT:-1000}"
SIZE="${SIZE:-25000}"
ROUNDS="${ROUNDS:-3}"
WORKERS="${WORKERS:-1 2 4 8}"
# TAG отделяет отдельные серии замеров: основную матрицу от прицельных
# прогонов вроде измерения цены free-threading на одном воркере.
TAG="${TAG:-}"

for build in gil nogil; do
  out="fixtures/cpu${TAG}-${build}.txt"
  py="/opt/py/${build}/bin/python"

  {
    echo "# CPU-нагрузка, сборка: ${build}"
    echo "# seed=${SEED} count=${COUNT} size=${SIZE} rounds=${ROUNDS} workers=${WORKERS}"
    echo "# ядер в контейнере: $(docker compose exec -T bench nproc | tr -d '\r')"
    echo "# ядро: $(docker compose exec -T bench uname -sr | tr -d '\r')"
    echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "# среда: ${PREFLIGHT}"
    echo "# порядок моделей в круге — латинский квадрат perm(n) = (n, n+1, n+2) mod 3,"
    echo "# где n = seed + номер круга; seed сдвигает начало, но не нарушает баланс."
    echo "#"
  } > "$out"

  for w in $WORKERS; do
    for r in $(seq 1 "$ROUNDS"); do
      order=$(docker compose exec -T bench "$py" -c \
        "from measure import latin_square; import run; print(' '.join(run.MODES[i] for i in latin_square($SEED, $r, 3)))" \
        | tr -d '\r')
      echo "# workers=${w} круг=${r} порядок: ${order}" >> "$out"
      for m in $order; do
        docker compose exec -T bench "$py" run.py \
          --mode "$m" --workload cpu --build "$build" \
          --workers "$w" --count "$COUNT" --size "$SIZE" \
          --seed "$SEED" --round "$r" | tr -d '\r' >> "$out"
      done
    done
  done

  echo "готово: $out"
done
