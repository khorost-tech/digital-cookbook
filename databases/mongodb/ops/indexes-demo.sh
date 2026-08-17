#!/usr/bin/env bash
# indexes-demo.sh — оркестрирует стенд #3 серии "MongoDB: глубокое
# погружение" (indexes/): multikey, правило ESR (Equality->Sort->Range),
# partial/TTL индексы, covered query — explain("executionStats") на РЕАЛЬНОМ
# 3-узловом replica set (rs0). Без PostgreSQL (тот же приём, что и
# ops/wiredtiger-demo.sh — контраст с PG индексами остаётся ссылкой в тексте
# статьи, не отдельным измерением этого стенда).
#
# Шаги (тот же приём, что и ops/modeling-demo.sh / ops/wiredtiger-demo.sh):
#   1. down -v (чистый старт) -> up -d compose/replica-set.yml под явным
#      именем проекта mongodb-cookbook (даёт предсказуемое имя сети
#      mongodb-cookbook_default и имена сервисов mongo1/mongo2/mongo3 — они
#      же DNS-имена внутри сети).
#   2. Дождаться, пока каждый mongod ответит на ping.
#   3. rs.initiate() (compose/init/rs-init.js) на mongo1, дождаться
#      PRIMARY (db.hello().isWritablePrimary).
#   4. Перегенерировать датасет (dataset/main.go, seed=42, детерминированно)
#      в golang:1.25, docker cp в mongo1, mongoimport трёх коллекций.
#   5. Запустить indexes/main.go в golang:1.25 НА СЕТИ
#      mongodb-cookbook_default (--network) — стенд подключается к
#      mongo1/mongo2/mongo3 по DNS-именам сервисов, создаёт индексы, гоняет
#      explain-запросы, печатает FIXTURE-строки и падает (log.Fatalf,
#      ненулевой exit) при провале ассерта. ВНИМАНИЕ: сценарий TTL внутри
#      стенда реально ждёт до 120s (poll фонового TTL monitor'а сервера) —
#      это ожидаемо увеличивает время прогона этого шага.
#   6. down -v (стенд эфемерный — не остаётся висеть между прогонами).
#
# Запуск (из mongodb/):
#   bash ops/indexes-demo.sh 2>&1 | tee /tmp/mongo-indexes.txt
# Требует: docker. Поднимает 3 контейнера (mongo1/2/3), сеть, запускает 2
# одноразовых контейнера golang:1.25 (dataset gen + стенд).

set -euo pipefail
# На Git Bash/MSYS docker-аргументы вида -w /app/indexes иначе мангаются в
# windows-путь; на Linux/WSL переменная безвредна (см. тот же приём в
# ops/verify-static.sh и ops/modeling-demo.sh/ops/wiredtiger-demo.sh).
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
ROOT="$(pwd)"

PROJECT="mongodb-cookbook"
NETWORK="${PROJECT}_default"
COMPOSE_ARGS=(-p "$PROJECT" -f compose/replica-set.yml)
MONGO_URI="mongodb://mongo1:27017,mongo2:27017,mongo3:27017/?replicaSet=rs0"

step() { echo; echo "=== $* ==="; }

step "0/6 down -v (чистый старт — предыдущий стенд, если остался, не мешает идемпотентности mongoimport)"
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true

step "1/6 up -d: replica-set.yml (проект $PROJECT, сеть $NETWORK)"
docker compose "${COMPOSE_ARGS[@]}" up -d

step "2/6 ждём mongo1/mongo2/mongo3 (ping)"
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

step "3/6 rs.initiate(rs0) на mongo1, ждём PRIMARY"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet < compose/init/rs-init.js
ok=0
for i in $(seq 1 40); do
  primary="$(docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet --eval "db.hello().isWritablePrimary" 2>/dev/null || true)"
  if [ "$primary" = "true" ]; then ok=1; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  rs0 не выбрал PRIMARY за отведённое время" >&2; exit 1; fi
echo "  rs0: PRIMARY готов"

step "4/6 генерация датасета (dataset/main.go, seed=42) в golang:1.25 + mongoimport: users/products/orders -> mongo1 (rs0)"
mkdir -p .gocache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/dataset golang:1.25 go run .
for coll in users products orders; do
  docker compose "${COMPOSE_ARGS[@]}" cp "dataset/out/${coll}.jsonl" "mongo1:/tmp/${coll}.jsonl"
  docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongoimport \
    --uri="mongodb://mongo1:27017/?replicaSet=rs0" --db=cookbook --collection="$coll" \
    --file="/tmp/${coll}.jsonl"
done

step "5/6 стенд indexes/main.go (golang:1.25, сеть $NETWORK) — multikey/ESR/partial/TTL/covered explain вживую (сценарий TTL ждёт до 120s)"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/indexes golang:1.25 \
  go run .
STAND_EXIT=$?
set -e

echo
echo "=== 6/6 down -v (стенд эфемерный, не остаётся между прогонами) ==="
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans

if [ "$STAND_EXIT" -ne 0 ]; then
  echo "indexes: стенд завершился с ошибкой (exit=$STAND_EXIT) — см. лог выше (assert упал или сбой соединения)." >&2
  exit "$STAND_EXIT"
fi

echo
echo "indexes-demo: готово. FIXTURE-строки выше -> дословно в FIXTURES.md §3."
