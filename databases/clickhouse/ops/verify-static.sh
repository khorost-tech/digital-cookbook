#!/usr/bin/env bash
# verify-static.sh — быстрый СТАТИЧЕСКИЙ гейт для cookbook clickhouse/.
# Проверяет, что весь код серии собирается и синтаксически валиден, БЕЗ запуска
# тяжёлых live-стендов (20M строк, кластер, Kafka, MinIO). Пригодно для CI/pre-push.
#
# Делает:
#   1. bash -n по всем ops/*.sh (синтаксис скриптов);
#   2. go build ./... + go vet ./... по всем Go-модулям (в golang:1.25);
#   3. mvn -DskipTests package по Java-реакторам (в maven:3.9-eclipse-temurin-25);
#   4. docker compose config по реальным комбинациям compose-файлов (валидация YAML/ссылок).
#
# Запуск:  bash ops/verify-static.sh    (из каталога clickhouse/ или откуда угодно)
# Требует: docker. НЕ поднимает ни одного контейнера-сервиса (только сборочные образы).

set -uo pipefail
# На Git Bash/MSYS docker-аргументы вида `-w /app/dataset` иначе мангаются в
# windows-путь; на Linux/WSL переменная безвредна.
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> clickhouse/
ROOT="$(pwd)"
fail=0
# Длительность каждой единицы печатаем, чтобы «тихие» docker-прогоны (go build,
# mvn package) не выглядели зависанием. mark перед единицей, ok/bad печатают Δсек.
_ti=0
step() { echo; echo "=== $* ==="; }
mark() { _ti=$SECONDS; }
ok()   { echo "  ok   $* ($((SECONDS-_ti))s)"; }
bad()  { echo "  FAIL $* ($((SECONDS-_ti))s)"; fail=1; }

mkdir -p .gocache .m2cache

# 1. Синтаксис bash-скриптов -------------------------------------------------
step "1/4 bash -n: синтаксис ops/*.sh"
for s in ops/*.sh; do
  if bash -n "$s"; then ok "$s"; else bad "$s"; fi
done

# 2. Go: сборка + vet всех модулей ------------------------------------------
step "2/4 go build ./... + go vet ./...  (golang:1.25)"
while IFS= read -r modfile; do
  d="$(dirname "$modfile")"; d="${d#./}"
  mark; printf '  go: %-22s ... ' "$d"
  if docker run --rm \
      -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" \
      -w "/app/$d" golang:1.25 \
      sh -c 'go build -o /dev/null ./... && go vet ./...' >/dev/null 2>&1; then
    printf 'ok (%ds)\n' "$((SECONDS-_ti))"
  else
    printf 'FAIL (%ds)\n' "$((SECONDS-_ti))"; fail=1
    echo "       повтори без >/dev/null: docker run --rm -v \"$ROOT:/app\" -v \"$ROOT/.gocache:/go/pkg/mod\" -w /app/$d golang:1.25 sh -c 'go build -o /dev/null ./... && go vet ./...'"
  fi
done < <(find . -name go.mod -not -path './.gocache/*' -not -path './.gomodcache/*')

# 3. Java: package реактора --------------------------------------------------
# Родительский java/pom.xml — реактор, включающий модуль ../drivers/java, поэтому
# монтируем весь clickhouse/ и запускаем из java/ (иначе ../drivers/java не виден).
step "3/4 mvn -DskipTests package  (maven:3.9-eclipse-temurin-25)"
if docker run --rm \
    -v "$ROOT:/app" -v "$ROOT/.m2cache:/root/.m2" \
    -w /app/java maven:3.9-eclipse-temurin-25 \
    mvn -q -DskipTests package >/dev/null 2>&1; then
  ok "java: реактор java/ (+ drivers/java)"
else
  bad "java: реактор java/  (повтори без >/dev/null: docker run --rm -v \"$ROOT:/app\" -v \"$ROOT/.m2cache:/root/.m2\" -w /app/java maven:3.9-eclipse-temurin-25 mvn -DskipTests package)"
fi

# 4. Compose: валидация конфигов (реальные комбинации стендов) ---------------
step "4/4 docker compose config  (без запуска)"
# Пары «-f …» ровно как их запускают соответствующие стенды.
declare -a COMBOS=(
  "compose/compose.yml"                          # #1/#2/#3/#6
  "compose/compose.yml compose/kafka.yml"        # #4 Kafka→CH
  "compose/cluster.yml"                          # #5 distributed (отдельный кластер)
  "compose/compose.yml compose/minio.yml"        # #7 S3
  "compose/compose.yml compose/decision.yml"     # #8 CH/Timescale/DuckDB/PG
)
for combo in "${COMBOS[@]}"; do
  args=(); for f in $combo; do args+=("-f" "$f"); done
  if docker compose "${args[@]}" config -q >/dev/null 2>&1; then
    ok "compose: $combo"
  else
    bad "compose: $combo"
  fi
done

# Итог -----------------------------------------------------------------------
echo
if [ "$fail" -eq 0 ]; then
  echo "ВСЁ ЗЕЛЁНОЕ ✓  — статическая проверка пройдена (тяжёлые live-стенды НЕ запускались)."
else
  echo "ЕСТЬ ПРОВАЛЫ ✗  — см. FAIL выше."
  exit 1
fi
