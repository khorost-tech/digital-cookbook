#!/usr/bin/env bash
# ops-demo.sh — оркестрирует стенд #7 серии "MongoDB: глубокое погружение"
# (ops-stand/ + java/ops/): драйверы (пул соединений/maxPoolSize, retryable
# writes переживают РЕАЛЬНЫЙ step-down primary), change streams/CDC
# (watch()+resumeAfter, Go И Java-зеркало на ОДНОМ живом кластере) и backup
# (mongodump/mongorestore round-trip) — на РЕАЛЬНОМ 3-узловом replica set
# (rs0), поверх уже импортированного датасета.
#
# Шаги (тот же приём, что и ops/aggregation-demo.sh, плюс backup-блок):
#   1. down -v (чистый старт) -> up -d compose/replica-set.yml под явным
#      именем проекта mongodb-cookbook.
#   2. Дождаться, пока каждый mongod ответит на ping.
#   3. rs.initiate() (compose/init/rs-init.js) на mongo1, дождаться PRIMARY.
#   4. Перегенерировать датасет (dataset/main.go, seed=42) в golang:1.25,
#      docker cp в mongo1, mongoimport трёх коллекций.
#   5. Собрать Java-реактор (mvn -DskipTests package, включая новый модуль
#      java/ops) — ДО запуска стендов.
#   6. Запустить ops-stand/main.go фаза "core" (golang:1.25, сеть) — пул
#      соединений, change streams (Go), retryable writes с реальным
#      step-down (последний внутри фазы — сам себя дожидается стабилизации
#      primary перед выходом). Падает (log.Fatalf) при провале ассерта.
#   7. Запустить java/ops/target/ops.jar (maven:3.9-eclipse-temurin-25,
#      та же сеть) — Java-зеркало change streams на СВОЕЙ коллекции
#      (cs_demo_java), ПРОТИВ ТОГО ЖЕ живого (уже стабилизированного после
#      шага 6) кластера.
#   8. BACKUP: mongodump --db=cookbook (полный дамп, включая demo-
#      коллекции — не мешает, backup-verify сверяет только users/products/
#      orders) из mongo1 в /tmp/ops-backup внутри контейнера, засекаем
#      время; затем mongorestore с --nsFrom/--nsTo в cookbook_restored,
#      засекаем время.
#   9. Запустить ops-stand/main.go фаза "backup-verify" (golang:1.25, сеть)
#      — сравнивает counts cookbook/cookbook_restored/manifest.json.
#  10. down -v (стенд эфемерный — не остаётся висеть между прогонами).
#
# Запуск (из mongodb/):
#   bash ops/ops-demo.sh 2>&1 | tee /tmp/mongo-ops.txt
# Требует: docker. Поднимает 3 контейнера (mongo1/2/3), сеть, запускает
# одноразовые контейнеры golang:1.25 (dataset gen + Go-стенд ×2 фазы) и
# maven:3.9-eclipse-temurin-25 (сборка java/ + Java-стенд).

set -euo pipefail
# На Git Bash/MSYS docker-аргументы вида -w /app/ops-stand иначе мангаются
# в windows-путь; на Linux/WSL переменная безвредна (тот же приём, что и во
# всех *-demo.sh серии).
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
ROOT="$(pwd)"

PROJECT="mongodb-cookbook"
NETWORK="${PROJECT}_default"
COMPOSE_ARGS=(-p "$PROJECT" -f compose/replica-set.yml)
MONGO_URI="mongodb://mongo1:27017,mongo2:27017,mongo3:27017/?replicaSet=rs0"
RESTORED_DB="cookbook_restored"

step() { echo; echo "=== $* ==="; }

step "0/10 down -v (чистый старт)"
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true

step "1/10 up -d: replica-set.yml (проект $PROJECT, сеть $NETWORK)"
docker compose "${COMPOSE_ARGS[@]}" up -d

step "2/10 ждём mongo1/mongo2/mongo3 (ping)"
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

step "3/10 rs.initiate(rs0) на mongo1, ждём PRIMARY"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet < compose/init/rs-init.js
ok=0
for i in $(seq 1 40); do
  primary="$(docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet --eval "db.hello().isWritablePrimary" 2>/dev/null || true)"
  if [ "$primary" = "true" ]; then ok=1; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  rs0 не выбрал PRIMARY за отведённое время" >&2; exit 1; fi
echo "  rs0: PRIMARY готов"

step "4/10 генерация датасета (dataset/main.go, seed=42) в golang:1.25 + mongoimport: users/products/orders -> mongo1 (rs0)"
mkdir -p .gocache .m2cache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/dataset golang:1.25 go run .
for coll in users products orders; do
  docker compose "${COMPOSE_ARGS[@]}" cp "dataset/out/${coll}.jsonl" "mongo1:/tmp/${coll}.jsonl"
  docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongoimport \
    --uri="mongodb://mongo1:27017/?replicaSet=rs0" --db=cookbook --collection="$coll" \
    --file="/tmp/${coll}.jsonl"
done

step "5/10 сборка java-реактора (mvn -DskipTests package, maven:3.9-eclipse-temurin-25, включая новый модуль java/ops) — ДО измерения латентности стендов"
docker run --rm -v "$ROOT:/app" -v "$ROOT/.m2cache:/root/.m2" -w /app/java maven:3.9-eclipse-temurin-25 \
  mvn -q -DskipTests package

step "6/10 Go-стенд ops-stand/main.go, фаза core (golang:1.25, сеть $NETWORK) — пул соединений/change streams/retryable writes+step-down"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/ops-stand golang:1.25 \
  go run . core
GO_CORE_EXIT=$?
set -e
if [ "$GO_CORE_EXIT" -ne 0 ]; then
  echo "ops-stand (core): стенд завершился с ошибкой (exit=$GO_CORE_EXIT) — см. лог выше." >&2
  echo "=== down -v (аварийная очистка) ==="
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans
  exit "$GO_CORE_EXIT"
fi

step "7/10 Java-зеркало java/ops/target/ops.jar (maven:3.9-eclipse-temurin-25, сеть $NETWORK) — change streams на своей коллекции (cs_demo_java)"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" \
  -v "$ROOT:/app" -v "$ROOT/.m2cache:/root/.m2" -w /app/java/ops maven:3.9-eclipse-temurin-25 \
  java -jar target/ops.jar
JAVA_EXIT=$?
set -e
if [ "$JAVA_EXIT" -ne 0 ]; then
  echo "ops (java, change streams): стенд завершился с ошибкой (exit=$JAVA_EXIT) — см. лог выше." >&2
  echo "=== down -v (аварийная очистка) ==="
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans
  exit "$JAVA_EXIT"
fi

step "8/10 BACKUP: mongodump --db=cookbook -> mongorestore --nsFrom=cookbook.* --nsTo=${RESTORED_DB}.* (mongo1, uri на весь rs0)"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 rm -rf /tmp/ops-backup
t0=$SECONDS
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongodump \
  --uri="$MONGO_URI" --db=cookbook --out=/tmp/ops-backup
dump_elapsed=$((SECONDS - t0))
echo "  mongodump: ${dump_elapsed}s"
echo "FIXTURE ops: backup_dump_duration=${dump_elapsed}s"

t0=$SECONDS
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongorestore \
  --uri="$MONGO_URI" --nsFrom="cookbook.*" --nsTo="${RESTORED_DB}.*" /tmp/ops-backup
restore_elapsed=$((SECONDS - t0))
echo "  mongorestore: ${restore_elapsed}s"
echo "FIXTURE ops: backup_restore_duration=${restore_elapsed}s"

step "9/10 Go-стенд ops-stand/main.go, фаза backup-verify (golang:1.25, сеть $NETWORK) — counts cookbook vs ${RESTORED_DB} vs manifest.json"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" -e RESTORED_DB="$RESTORED_DB" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/ops-stand golang:1.25 \
  go run . backup-verify
BACKUP_VERIFY_EXIT=$?
set -e

echo
echo "=== 10/10 down -v (стенд эфемерный, не остаётся между прогонами) ==="
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans

if [ "$BACKUP_VERIFY_EXIT" -ne 0 ]; then
  echo "ops-stand (backup-verify): counts НЕ совпали (exit=$BACKUP_VERIFY_EXIT) — см. лог выше." >&2
  exit "$BACKUP_VERIFY_EXIT"
fi

echo
echo "ops-demo: готово. FIXTURE-строки выше (ops: / ops-java:) -> дословно в FIXTURES.md §7."
