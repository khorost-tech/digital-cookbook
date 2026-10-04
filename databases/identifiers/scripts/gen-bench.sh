#!/usr/bin/env bash
# Артефакт 5 (сводка): прогоняет бенч генерации ID на трёх языках (Go/Java/
# Rust) и печатает их вывод единым контрактом:
#   <lang> <gen> ops_per_sec=<float> monotonic_within_ms=<true|false> byte_sortable=<true|false>
#
# Абсолютные ops/sec МЕЖДУ языками сравнивать осторожно — не апельсины-к-
# апельсинам: у Java нет отдельной фазы warmup (JIT C1/C2 разогревается
# внутри самого замеряемого цикла), у трёх языков разные генераторы
# случайности под капотом (crypto/rand в Go vs SecureRandom/ThreadLocalRandom
# в Java vs getrandom(2) в Rust) и разная нагрузка на аллокатор/GC. ГЛАВНОЕ
# наблюдение этого артефакта — не абсолютные числа, а то, что монотонность и
# побайтовая сортируемость по ТИПУ идентификатора одинаковы во всех трёх
# языках: uuidv4 везде monotonic=false/byte_sortable=false (случаен полностью),
# uuidv7/ulid везде true (время в старших битах), а у Go дополнительно есть
# snowflake (тоже true) — Java/Rust-версии этого генератора в стенде нет.
#
# ТУЛЧЕЙНЫ. По умолчанию берутся go / mvn / cargo из PATH — на машине с
# обычной установкой скрипт запускается без настройки:
#   N=1000000 bash scripts/gen-bench.sh
# Если тулчейн лежит нестандартно (или Rust доступен только под WSL — как на
# Windows-хосте, где снимались числа FIXTURES) — переопределяется переменными:
#   GO=/путь/к/go            по умолчанию: go из PATH
#   MVN=/путь/к/mvn          по умолчанию: mvn из PATH
#   JAVA_HOME=/путь/к/jdk    по умолчанию: системный JDK, который найдёт Maven
#   M2_REPO=/путь/к/.m2      по умолчанию: не передаётся (штатный ~/.m2)
#   CARGO=/путь/к/cargo      по умолчанию: cargo из PATH
#   RUST_VIA_WSL=1           гонять cargo под WSL (Windows-хост без cargo)
#   GOPROXY=...              по умолчанию: https://go.khorost.tech,direct
#
# Отсутствующий тулчейн НЕ пропускается молча: секция помечается SKIP, и в
# конце скрипт завершается ненулевым кодом со сводкой пропущенного — иначе
# неполный прогон легко принять за полный.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
N=${N:-1000000}

GO=${GO:-go}
MVN=${MVN:-mvn}
CARGO=${CARGO:-cargo}
: "${GOPROXY:=https://go.khorost.tech,direct}"

SKIPPED=()
have() { command -v "$1" >/dev/null 2>&1; }

echo "=== Go (n=$N) ==="
if have "$GO"; then
  ( cd gen/go && GOPROXY="$GOPROXY" "$GO" run . -n="$N" )
else
  echo "SKIP Go: '$GO' не найден в PATH — задайте GO=/путь/к/go" >&2
  SKIPPED+=("go")
fi

echo "=== Java (n=$N) ==="
if have "$MVN"; then
  MVN_ARGS=(-q compile exec:java -Dexec.mainClass=tech.khorost.GenBench -Dexec.args="$N")
  if [ -n "${M2_REPO:-}" ]; then MVN_ARGS=(-Dmaven.repo.local="$M2_REPO" "${MVN_ARGS[@]}"); fi
  ( cd gen/java && "$MVN" "${MVN_ARGS[@]}" )
else
  echo "SKIP Java: '$MVN' не найден в PATH — установите Maven или задайте MVN=/путь/к/mvn" >&2
  SKIPPED+=("java")
fi

echo "=== Rust (n=$N) ==="
if [ "${RUST_VIA_WSL:-0}" = "1" ] || ! have "$CARGO"; then
  if have wsl.exe; then
    # HERE — POSIX-путь вида /g/... (git-bash mangling); пересобираем в
    # /mnt/g/... для WSL, откуда смонтирован тот же диск.
    WIN_PATH="$(pwd)/gen/rust"
    WSL_PATH="/mnt/${WIN_PATH#/}"
    echo "(cargo не найден на хосте либо RUST_VIA_WSL=1 — гоним под WSL)" >&2
    wsl.exe -e bash -lc "cd '$WSL_PATH' && cargo run --release -- $N"
  else
    echo "SKIP Rust: '$CARGO' не найден в PATH и wsl.exe недоступен — задайте CARGO=/путь/к/cargo" >&2
    SKIPPED+=("rust")
  fi
else
  ( cd gen/rust && "$CARGO" run --release -- "$N" )
fi

if [ ${#SKIPPED[@]} -gt 0 ]; then
  echo >&2
  echo "НЕПОЛНЫЙ ПРОГОН: пропущено — ${SKIPPED[*]}. Сводка артефакта 5 неполна." >&2
  exit 1
fi
