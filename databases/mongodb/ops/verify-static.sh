#!/usr/bin/env bash
# verify-static.sh — быстрый СТАТИЧЕСКИЙ гейт для cookbook mongodb/.
# Проверяет, что весь код серии собирается и синтаксически валиден, БЕЗ запуска
# тяжёлых live-стендов (replica set, sharded-кластер, PostgreSQL). Пригодно для
# CI/pre-push. Адаптирован из ../clickhouse/ops/verify-static.sh.
#
# Делает:
#   1. bash -n по всем ops/*.sh (синтаксис скриптов);
#   2. go build ./... + go vet ./... по всем Go-модулям (в golang:1.25),
#      если go.mod ещё нет (Task 1) — шаг мягко пропускается, не FAIL;
#   3. mvn -DskipTests package по java/ (в maven:3.9-eclipse-temurin-25),
#      если java/pom.xml ещё нет (Task 1) — шаг мягко пропускается, не FAIL;
#   4. docker compose config по реальным комбинациям compose-файлов (валидация
#      YAML/ссылок) — единственный шаг, который ДОЛЖЕН быть зелёным уже в Task 1.
#
# Запуск:  bash ops/verify-static.sh    (из каталога mongodb/ или откуда угодно)
# Требует: docker. НЕ поднимает ни одного контейнера-сервиса (только сборочные образы).

set -uo pipefail
# На Git Bash/MSYS docker-аргументы вида `-w /app/dataset` иначе мангаются в
# windows-путь; на Linux/WSL переменная безвредна.
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
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

# 2. Go: сборка + vet всех модулей (Tasks 2+ добавляют go.mod) ---------------
step "2/4 go build ./... + go vet ./...  (golang:1.25)"
found_go=0
while IFS= read -r modfile; do
  found_go=1
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
if [ "$found_go" -eq 0 ]; then
  echo "  ok   нет go-модулей (появятся в Task 2+)"
fi

# 3. Java: package реактора (Tasks 2+ добавляют java/pom.xml) ---------------
step "3/4 mvn -DskipTests package  (maven:3.9-eclipse-temurin-25)"
if [ -f java/pom.xml ]; then
  mark
  if docker run --rm \
      -v "$ROOT:/app" -v "$ROOT/.m2cache:/root/.m2" \
      -w /app/java maven:3.9-eclipse-temurin-25 \
      mvn -q -DskipTests package >/dev/null 2>&1; then
    ok "java: реактор java/"
  else
    bad "java: реактор java/  (повтори без >/dev/null: docker run --rm -v \"$ROOT:/app\" -v \"$ROOT/.m2cache:/root/.m2\" -w /app/java maven:3.9-eclipse-temurin-25 mvn -DskipTests package)"
  fi
else
  echo "  ok   нет java/pom.xml (появится в Task 2+)"
fi

# 4. Compose: валидация конфигов (реальные комбинации стендов) ---------------
step "4/4 docker compose config  (без запуска)"
# Комбинации ровно как их будут запускать соответствующие стенды серии.
declare -a COMBOS=(
  "compose/replica-set.yml"
  "compose/replica-set.yml compose/postgres.yml"
  "compose/sharded.yml"
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
