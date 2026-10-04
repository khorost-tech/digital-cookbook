#!/usr/bin/env bash
# Три конфигурации JSON-движка на одном и том же payload.
#
#   ./scripts/bench-all.sh [прогонов]
#
#   v1-old    — encoding/json на прежнем движке   (сборка с GOEXPERIMENT=nojsonv2)
#   v1-on-v2  — encoding/json как есть в 1.27     (умолчание)
#   v2-api    — явный encoding/json/v2            (умолчание, другие бенчи)
#
# Разница v1-old -> v1-on-v2 — цена обновления тулчейна для кода, который не
# правили. Разница v1-on-v2 -> v2-api — цена переписывания. Первую величину
# нельзя получить, меряя только крайние конфигурации.
#
# ПРОГОНОВ ПО УМОЛЧАНИЮ ДВА: разброс внутри одного прогона не описывает
# воспроизводимость.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUNS="${1:-2}"
OUT="results/01-bench.txt"
mkdir -p results
: > "$OUT"

run_mode() {
  local mode="$1" experiment="$2" pattern="$3" label="$4"

  # Контроль применения ДО замера: собранный не в том режиме бинарник померяет
  # не то, а выглядеть это будет как «настройка ничего не изменила».
  if ! GOTOOLCHAIN=go1.27.0 GOEXPERIMENT="$experiment" \
       go run ./cmd/buildmode-check -want "$mode" >/dev/null 2>&1; then
    echo "ОШИБКА: режим $mode не применился (GOEXPERIMENT=$experiment) — сравнивать нечего." >&2
    exit 1
  fi

  echo "=== $label (прогонов: $RUNS) ===" >> "$OUT"
  for i in $(seq 1 "$RUNS"); do
    echo "--- прогон $i ---" >> "$OUT"

    # go test возвращает rc=0, даже если паттерн -bench не совпал ни с одной
    # функцией: пустой набор бенчей — это тоже "успешный" запуск с точки
    # зрения тестового рантайма. Опечатка в имени, переименование бенчмарки
    # или файл, случайно исключённый build-тегом, — и секция отчёта тихо
    # останется без единой строки чисел, а скрипт как ни в чём не бывало
    # пойдёт дальше и в конце скажет "Отчёт: ...". Это не "ничего не нашлось",
    # это молчаливый ложный зелёный: результат неотличим от настоящего
    # успеха, пока кто-то не откроет файл и не заметит дыру руками. Поэтому
    # прогон пишется во временный файл и проверяется на "ns/op" ДО того, как
    # попадёт в общий отчёт — проверяем именно этот прогон, не файл целиком:
    # первая нормальная секция иначе замаскирует пустую вторую.
    tmp="$(mktemp)"
    GOTOOLCHAIN=go1.27.0 GOEXPERIMENT="$experiment" \
      go test -run '^$' -bench "$pattern" -benchmem -count=1 . > "$tmp" 2>&1
    cat "$tmp" >> "$OUT"

    if ! grep -q 'ns/op' "$tmp"; then
      echo "ОШИБКА: прогон $i секции '$label' (паттерн $pattern) не дал ни одной строки с ns/op — паттерн разошёлся с именами бенчей или бенчи вообще не собрались. Замеру верить нельзя." >&2
      rm -f "$tmp"
      exit 1
    fi
    rm -f "$tmp"
  done
  echo >> "$OUT"
}

{
  echo "Три конфигурации JSON на одном payload"
  echo "Тулчейн: $(GOTOOLCHAIN=go1.27.0 go version)"
  echo
} >> "$OUT"

run_mode v1-old   "nojsonv2" 'BenchmarkV1' "v1-old: encoding/json на прежнем движке"
run_mode v1-on-v2 ""         'BenchmarkV1' "v1-on-v2: encoding/json в 1.27 без правок кода"
run_mode v1-on-v2 ""         'BenchmarkV2' "v2-api: явный encoding/json/v2"

echo "Отчёт: $OUT"
