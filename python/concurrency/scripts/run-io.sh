#!/usr/bin/env bash
# IO-нагрузка: те же три модели на тех же двух сборках.
#
# Числа воркеров здесь выше, чем в CPU-сценарии: на ожидании имеет смысл
# именно высокая одновременность, там и проходит граница между моделями.
#
# Осознанное ограничение: у модели процессов число воркеров равно числу
# процессов ОС, и 256 процессов — это уже не сравнение моделей, а исчерпание
# памяти хоста. Поэтому для процессов число воркеров ограничено PROC_MAX,
# и каждый такой случай отмечается в фикстуре отдельной строкой, а не
# выпадает из неё молча.
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
DELAY="${DELAY:-0.02}"
ROUNDS="${ROUNDS:-3}"
WORKERS="${WORKERS:-1 8 64 256}"
PROC_MAX="${PROC_MAX:-64}"
# TAG отделяет прицельные серии от основной матрицы.
TAG="${TAG:-}"

for build in gil nogil; do
  out="fixtures/io${TAG}-${build}.txt"
  py="/opt/py/${build}/bin/python"

  {
    echo "# IO-нагрузка, сборка: ${build}"
    echo "# seed=${SEED} count=${COUNT} delay=${DELAY}s rounds=${ROUNDS} workers=${WORKERS}"
    echo "# ограничение: модель процессов не запускается выше ${PROC_MAX} воркеров"
    echo "# ядер в контейнере: $(docker compose exec -T bench nproc | tr -d '\r')"
    echo "# ядро: $(docker compose exec -T bench uname -sr | tr -d '\r')"
    echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "# среда: ${PREFLIGHT}"
    echo "# порядок моделей в круге — латинский квадрат, как и в CPU-сценарии"
    echo "#"
  } > "$out"

  for w in $WORKERS; do
    for r in $(seq 1 "$ROUNDS"); do
      order=$(docker compose exec -T bench "$py" -c \
        "from measure import latin_square; import run; print(' '.join(run.MODES[i] for i in latin_square($SEED, $r, 3)))" \
        | tr -d '\r')
      echo "# workers=${w} круг=${r} порядок: ${order}" >> "$out"
      for m in $order; do
        if [ "$m" = "processes" ] && [ "$w" -gt "$PROC_MAX" ]; then
          echo "# ПРОПУЩЕНО: processes при workers=${w} > PROC_MAX=${PROC_MAX}" >> "$out"
          continue
        fi
        docker compose exec -T bench "$py" run.py \
          --mode "$m" --workload io --build "$build" \
          --workers "$w" --count "$COUNT" --delay "$DELAY" \
          --seed "$SEED" --round "$r" | tr -d '\r' >> "$out"
      done
    done
  done

  echo "готово: $out"
done
