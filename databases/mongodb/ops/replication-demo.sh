#!/usr/bin/env bash
# replication-demo.sh — оркестрирует стенд #5 серии "MongoDB: глубокое
# погружение" (replication/): oplog, write concern (w:1 vs w:majority), read
# concern/причинная согласованность (read-your-writes через secondary) — и
# НАСТОЯЩИЙ failover (docker stop контейнера primary, реальные перевыборы) на
# РЕАЛЬНОМ 3-узловом replica set (rs0) + опциональный PG-контраст
# (compose/postgres.yml, wal_level "из коробки").
#
# Шаги (та же дисциплина, что и ops/modeling-demo.sh/wiredtiger-demo.sh, плюс
# отдельный failover-блок, специфичный для этого стенда):
#   1. down -v (чистый старт) -> up -d replica-set.yml + postgres.yml под
#      явным именем проекта mongodb-cookbook (сеть mongodb-cookbook_default,
#      DNS-имена mongo1/mongo2/mongo3/pg).
#   2. Дождаться ping у mongo1/2/3 и accepting connections у pg.
#   3. rs.initiate() (compose/init/rs-init.js) на mongo1, дождаться PRIMARY.
#   4. Перегенерировать датасет (dataset/main.go, seed=42) + mongoimport трёх
#      коллекций — тот же полный датасет, что и у остальных стендов серии
#      (стенд сам не зависит от users/products/orders, но топология и
#      дисциплина едины по всей серии).
#   5. Запустить replication/main.go ФАЗА "core" (golang:1.25, сеть) — на ЕЩЁ
#      ЗДОРОВОМ кластере: oplog/write concern/причинная согласованность/
#      PG-контраст. Падает (log.Fatalf) при провале ассерта.
#   6. ОПРЕДЕЛИТЬ текущий primary (polling db.hello().isWritablePrimary по
#      mongo1/mongo2/mongo3 — НЕ полагаемся на priority из rs-init.js
#      напрямую, хотя на свежем кластере это почти всегда mongo1).
#   7. FAILOVER: `docker stop` контейнера primary, затем polling
#      db.hello().isWritablePrimary на ДВУХ ВЫЖИВШИХ узлах (2 из 3 — всё ещё
#      большинство исходного набора голосующих членов, кворум для выборов
#      сохраняется) до появления нового primary — засекается приблизительное
#      время перевыборов (секундная точность, bash $SECONDS).
#   8. Запустить replication/main.go ФАЗА "failover-write" (golang:1.25,
#      сеть) — ПОСЛЕ того, как новый primary уже подтверждён шагом 7:
#      единственный ассерт — реальная запись проходит на новый кластер.
#   9. down -v (стенд эфемерный — не остаётся висеть между прогонами;
#      работает независимо от того, что один из трёх контейнеров уже
#      остановлен шагом 7 — down -v убирает все контейнеры проекта разом).
#
# Запуск (из mongodb/):
#   bash ops/replication-demo.sh 2>&1 | tee /tmp/mongo-replication.txt
# Требует: docker. Поднимает 4 контейнера (mongo1/2/3 + pg), сеть, запускает
# 3 одноразовых контейнера golang:1.25 (dataset gen + стенд ×2 фазы).

set -euo pipefail
# На Git Bash/MSYS docker-аргументы вида -w /app/replication иначе мангаются
# в windows-путь; на Linux/WSL переменная безвредна (тот же приём, что и во
# всех *-demo.sh серии).
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
ROOT="$(pwd)"

PROJECT="mongodb-cookbook"
NETWORK="${PROJECT}_default"
COMPOSE_ARGS=(-p "$PROJECT" -f compose/replica-set.yml -f compose/postgres.yml)
MONGO_URI="mongodb://mongo1:27017,mongo2:27017,mongo3:27017/?replicaSet=rs0"
PG_DSN="postgres://postgres:cookbook@pg:5432/cookbook?sslmode=disable"

step() { echo; echo "=== $* ==="; }

# hello_is_primary <svc> — true/false строкой, "" при недоступности узла.
hello_is_primary() {
  docker compose "${COMPOSE_ARGS[@]}" exec -T "$1" mongosh --quiet --eval "db.hello().isWritablePrimary" 2>/dev/null || true
}

step "0/9 down -v (чистый старт — предыдущий стенд, если остался, не мешает идемпотентности)"
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true

step "1/9 up -d: replica-set.yml + postgres.yml (проект $PROJECT, сеть $NETWORK)"
docker compose "${COMPOSE_ARGS[@]}" up -d

step "2/9 ждём mongo1/mongo2/mongo3 (ping) и pg (pg_isready)"
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

step "3/9 rs.initiate(rs0) на mongo1, ждём PRIMARY"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongosh --quiet < compose/init/rs-init.js
ok=0
for i in $(seq 1 40); do
  if [ "$(hello_is_primary mongo1)" = "true" ]; then ok=1; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  rs0 не выбрал PRIMARY за отведённое время" >&2; exit 1; fi
echo "  rs0: PRIMARY готов"

step "4/9 генерация датасета (dataset/main.go, seed=42) + mongoimport: users/products/orders -> mongo1 (rs0)"
mkdir -p .gocache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/dataset golang:1.25 go run .
for coll in users products orders; do
  docker compose "${COMPOSE_ARGS[@]}" cp "dataset/out/${coll}.jsonl" "mongo1:/tmp/${coll}.jsonl"
  docker compose "${COMPOSE_ARGS[@]}" exec -T mongo1 mongoimport \
    --uri="mongodb://mongo1:27017/?replicaSet=rs0" --db=cookbook --collection="$coll" \
    --file="/tmp/${coll}.jsonl"
done

step "5/9 стенд replication/main.go, фаза core (golang:1.25, сеть $NETWORK) — oplog/write concern/причинная согласованность/PG-контраст"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" -e PG_DSN="$PG_DSN" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/replication golang:1.25 \
  go run . core
CORE_EXIT=$?
set -e
if [ "$CORE_EXIT" -ne 0 ]; then
  echo "replication (core): стенд завершился с ошибкой (exit=$CORE_EXIT) — см. лог выше." >&2
  echo "=== down -v (аварийная очистка) ==="
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans
  exit "$CORE_EXIT"
fi

step "6/9 определяем текущий primary (polling db.hello().isWritablePrimary)"
primary_before=""
for svc in mongo1 mongo2 mongo3; do
  if [ "$(hello_is_primary "$svc")" = "true" ]; then primary_before="$svc"; break; fi
done
if [ -z "$primary_before" ]; then
  echo "  не удалось определить текущий primary перед failover" >&2
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans
  exit 1
fi
echo "  текущий primary: $primary_before"

survivors=()
for svc in mongo1 mongo2 mongo3; do
  [ "$svc" = "$primary_before" ] || survivors+=("$svc")
done

step "7/9 FAILOVER: docker stop $primary_before, ждём нового primary среди ${survivors[*]}"
t0=$SECONDS
docker compose "${COMPOSE_ARGS[@]}" stop "$primary_before"

new_primary=""
for i in $(seq 1 60); do
  for svc in "${survivors[@]}"; do
    if [ "$(hello_is_primary "$svc")" = "true" ]; then new_primary="$svc"; break 2; fi
  done
  sleep 1
done
election_elapsed=$((SECONDS - t0))

if [ -z "$new_primary" ]; then
  echo "  новый primary НЕ избран за отведённое время (${election_elapsed}s) — failover провалился" >&2
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans
  exit 1
fi
echo "  новый primary: $new_primary (перевыборы заняли ~${election_elapsed}s, остановленный узел: $primary_before)"
echo "FIXTURE replication: failover_stopped_primary=$primary_before failover_new_primary=$new_primary failover_election_time_approx=${election_elapsed}s"

step "8/9 стенд replication/main.go, фаза failover-write (golang:1.25, сеть $NETWORK) — запись ПОСЛЕ re-election"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/replication golang:1.25 \
  go run . failover-write
FAILOVER_EXIT=$?
set -e

echo
echo "=== 9/9 down -v (стенд эфемерный; работает независимо от того, что $primary_before уже остановлен) ==="
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans

if [ "$FAILOVER_EXIT" -ne 0 ]; then
  echo "replication (failover-write): запись после re-election НЕ прошла (exit=$FAILOVER_EXIT) — см. лог выше." >&2
  exit "$FAILOVER_EXIT"
fi

echo
echo "replication-demo: готово. FIXTURE-строки выше -> дословно в FIXTURES.md §5."
