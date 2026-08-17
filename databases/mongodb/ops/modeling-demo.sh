#!/usr/bin/env bash
# modeling-demo.sh — оркестрирует стенд #1 серии "MongoDB: глубокое
# погружение" (modeling/): embed vs reference + PG jsonb-контраст на
# РЕАЛЬНОМ 3-узловом replica set + PostgreSQL.
#
# Шаги:
#   1. down -v (чистый старт) -> up -d compose/replica-set.yml +
#      compose/postgres.yml под явным именем проекта mongodb-cookbook (даёт
#      предсказуемое имя сети mongodb-cookbook_default и имена сервисов
#      mongo1/mongo2/mongo3/pg — они же DNS-имена внутри сети).
#   2. Дождаться, пока каждый mongod ответит на ping, и pg — accepting
#      connections.
#   3. rs.initiate() (compose/init/rs-init.js) на mongo1, дождаться
#      PRIMARY (db.hello().isWritablePrimary).
#   4. Перегенерировать датасет (dataset/main.go, seed=42, детерминированно)
#      в golang:1.25, docker cp в mongo1, mongoimport трёх коллекций.
#   5. docker cp pg-seed.sql в pg, psql -f (jsonb + нормализованные
#      таблицы).
#   6. Запустить modeling/main.go в golang:1.25 НА СЕТИ
#      mongodb-cookbook_default (--network) — стенд подключается к
#      mongo1/mongo2/mongo3/pg по DNS-именам сервисов, печатает FIXTURE-
#      строки и падает (log.Fatalf, ненулевой exit) при провале ассерта.
#   7. down -v (стенд эфемерный — не остаётся висеть между прогонами).
#
# Запуск (из mongodb/):
#   bash ops/modeling-demo.sh 2>&1 | tee /tmp/mongo-modeling.txt
# Требует: docker. Поднимает 4 контейнера (mongo1/2/3 + pg), сеть,
# запускает 2 одноразовых контейнера golang:1.25 (dataset gen + стенд).

set -euo pipefail
# На Git Bash/MSYS docker-аргументы вида -w /app/modeling иначе мангаются в
# windows-путь; на Linux/WSL переменная безвредна (см. тот же приём в
# ops/verify-static.sh и во всех *-demo.sh серии).
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
ROOT="$(pwd)"

PROJECT="mongodb-cookbook"
NETWORK="${PROJECT}_default"
COMPOSE_ARGS=(-p "$PROJECT" -f compose/replica-set.yml -f compose/postgres.yml)
MONGO_URI="mongodb://mongo1:27017,mongo2:27017,mongo3:27017/?replicaSet=rs0"
PG_DSN="postgres://postgres:cookbook@pg:5432/cookbook?sslmode=disable"

step() { echo; echo "=== $* ==="; }

step "0/7 down -v (чистый старт — предыдущий стенд, если остался, не мешает идемпотентности mongoimport/pg-seed)"
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true

step "1/7 up -d: replica-set.yml + postgres.yml (проект $PROJECT, сеть $NETWORK)"
docker compose "${COMPOSE_ARGS[@]}" up -d

step "2/7 ждём mongo1/mongo2/mongo3 (ping) и pg (pg_isready)"
for svc in mongo1 mongo2 mongo3; do
  ok=0
  for i in $(seq 1 40); do
    if docker compose "${COMPOSE_ARGS[@]}" exec -T "$svc" mongosh --quiet --eval "db.adminCommand('ping')" >/dev/null 2>&1; then
      ok=1; break
    fi
    sleep 2
  done
  if [ "$ok" -ne 1 ]; then echo "  $svc не ответил на ping за отведённое время" >&2; exit 1; fi
  echo "  $svc: ping OK"
done
ok=0
for i in $(seq 1 40); do
  if docker compose "${COMPOSE_ARGS[@]}" exec -T pg pg_isready -U postgres -d cookbook >/dev/null 2>&1; then
    ok=1; break
  fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  pg не стал accepting connections за отведённое время" >&2; exit 1; fi
echo "  pg: accepting connections"

step "3/7 rs.initiate(rs0) на mongo1, ждём PRIMARY"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet < compose/init/rs-init.js
ok=0
for i in $(seq 1 40); do
  primary="$(docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet --eval "db.hello().isWritablePrimary" 2>/dev/null || true)"
  if [ "$primary" = "true" ]; then ok=1; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  rs0 не выбрал PRIMARY за отведённое время" >&2; exit 1; fi
echo "  rs0: PRIMARY готов"

step "4/7 генерация датасета (dataset/main.go, seed=42) в golang:1.25"
mkdir -p .gocache .m2cache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/dataset golang:1.25 go run .

step "5/7 mongoimport: users/products/orders -> mongo1 (rs0)"
for coll in users products orders; do
  docker compose "${COMPOSE_ARGS[@]}" cp "dataset/out/${coll}.jsonl" "mongo1:/tmp/${coll}.jsonl"
  docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongoimport \
    --uri="mongodb://mongo1:27017/?replicaSet=rs0" --db=cookbook --collection="$coll" \
    --file="/tmp/${coll}.jsonl"
done

step "6/7 psql -f pg-seed.sql -> pg (jsonb + нормализованные таблицы)"
docker compose "${COMPOSE_ARGS[@]}" cp dataset/out/pg-seed.sql pg:/tmp/pg-seed.sql
docker compose "${COMPOSE_ARGS[@]}" exec -T pg psql -v ON_ERROR_STOP=1 -U postgres -d cookbook -f /tmp/pg-seed.sql

step "7/7 стенд modeling/main.go (golang:1.25, сеть $NETWORK) — embed vs reference + growth + PG jsonb-контраст"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" -e PG_DSN="$PG_DSN" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/modeling golang:1.25 \
  go run .
STAND_EXIT=$?
set -e

echo
echo "=== down -v (стенд эфемерный, не остаётся между прогонами) ==="
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans

if [ "$STAND_EXIT" -ne 0 ]; then
  echo "modeling: стенд завершился с ошибкой (exit=$STAND_EXIT) — см. лог выше (assert упал или сбой соединения)." >&2
  exit "$STAND_EXIT"
fi

echo
echo "modeling-demo: готово. FIXTURE-строки выше -> дословно в FIXTURES.md §1."
