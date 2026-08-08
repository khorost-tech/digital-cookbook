#!/usr/bin/env bash
# verify-static.sh — быстрый СТАТИЧЕСКИЙ гейт для cookbook scylladb/.
# Проверяет, что весь код серии собирается и синтаксически валиден, БЕЗ запуска
# тяжёлых live-стендов (3-узловой/4-узловой кластеры, ожидание кворума,
# загрузка данных). Пригодно для CI/pre-push.
#
# Делает:
#   1. bash -n по всем ops/*.sh (синтаксис скриптов);
#   2. go build ./... + go vet ./... по ВСЕМ Go-модулям серии (в golang:1.26)
#      — модули обнаруживаются автоматически (find . -name go.mod), а не
#      перечисляются вручную, поэтому шаг не отстаёт при добавлении новых
#      стендов (dataset, modeling, architecture, compaction, consistency-lwt,
#      topology, drivers/go, ops-stand — на момент Task 9);
#   3. mvn -DskipTests package по Java-реактору java/ (в maven:3.9-eclipse-
#      temurin-25) — ОДИН вызов на весь реактор (включает ../drivers/java
#      как <module>), появился с Task 7 (Стенд #6, shard-aware драйверы);
#   4. docker compose config по РЕАЛЬНЫМ комбинациям compose-файлов, которыми
#      пользуются стенды серии (валидация YAML/ссылок).
#
# Финальная версия (Task 9) — покрывает все 7 стендов серии + датасет +
# Java-реактор + все 3 compose-комбинации.
#
# Запуск:  bash ops/verify-static.sh    (из каталога scylladb/ или откуда угодно)
# Требует: docker. НЕ поднимает ни одного контейнера-сервиса ScyllaDB (только
# сборочные образы golang/maven).

set -uo pipefail
# На Git Bash/MSYS docker-аргументы вида `-w /app/dataset` иначе мангаются в
# windows-путь; на Linux/WSL переменная безвредна.
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> scylladb/
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

t0=$SECONDS

# 1. Синтаксис bash-скриптов -------------------------------------------------
step "1/4 bash -n: синтаксис ops/*.sh"
mark
for s in ops/*.sh; do
  if bash -n "$s"; then ok "$s"; else bad "$s"; fi
done

# 2. Go: сборка + vet всех модулей ------------------------------------------
step "2/4 go build ./... + go vet ./...  (golang:1.26)"
while IFS= read -r modfile; do
  d="$(dirname "$modfile")"; d="${d#./}"
  mark; printf '  go: %-22s ... ' "$d"
  if docker run --rm \
      -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" \
      -w "/app/$d" golang:1.26 \
      sh -c 'go build -o /dev/null ./... && go vet ./...' >/dev/null 2>&1; then
    printf 'ok (%ds)\n' "$((SECONDS-_ti))"
  else
    printf 'FAIL (%ds)\n' "$((SECONDS-_ti))"; fail=1
    echo "       повтори без >/dev/null: docker run --rm -v \"$ROOT:/app\" -v \"$ROOT/.gocache:/go/pkg/mod\" -w /app/$d golang:1.26 sh -c 'go build -o /dev/null ./... && go vet ./...'"
  fi
done < <(find . -name go.mod -not -path './.gocache/*' -not -path './.gomodcache/*' | sort)

# 3. Java: package реактора --------------------------------------------------
# java/pom.xml — родительский реактор (packaging=pom), включающий модуль
# ../drivers/java, поэтому монтируем весь scylladb/ и запускаем из java/
# (иначе ../drivers/java не виден Maven'у). Один вызов на весь реактор.
step "3/4 mvn -DskipTests package  (maven:3.9-eclipse-temurin-25)"
mark
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
  "compose/compose.yml"                            # одиночный ДЦ, 3 узла (Стенды #1-4, #6-7)
  "compose/compose.yml compose/monitoring.yml"     # + prometheus на scylla-cookbook-net (Стенд #7)
  "compose/multidc.yml"                            # отдельный 2ДЦ x 2 узла кластер (Стенд #5)
)
for combo in "${COMBOS[@]}"; do
  mark
  args=(); for f in $combo; do args+=("-f" "$f"); done
  if docker compose "${args[@]}" config -q >/dev/null 2>&1; then
    ok "compose: $combo"
  else
    bad "compose: $combo"
  fi
done

# Итог -----------------------------------------------------------------------
echo
echo "Итого: $((SECONDS-t0))s"
if [ "$fail" -eq 0 ]; then
  echo "ВСЁ ЗЕЛЁНОЕ ✓  — статическая проверка пройдена (тяжёлые live-стенды НЕ запускались)."
else
  echo "ЕСТЬ ПРОВАЛЫ ✗  — см. FAIL выше."
  exit 1
fi
