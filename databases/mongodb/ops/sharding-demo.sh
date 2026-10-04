#!/usr/bin/env bash
# sharding-demo.sh — оркестрирует стенд #6 серии "MongoDB: глубокое
# погружение" (sharding/): shard key hashed vs ranged, распределение чанков,
# балансировщик, резолюция запроса (targeted vs scatter-gather), resharding —
# на РЕАЛЬНОМ шардированном кластере (mongos + config RS csrs + два шарда-RS
# shard1/shard2), поднимаемом из compose/sharded.yml.
#
# Та же дисциплина, что и в ops/replication-demo.sh, но топология тяжелее:
# кроме up/down нужно ВЖИВУЮ инициировать ТРИ replica set и зарегистрировать
# два шарда в кластере.
#
# Шаги:
#   1. down -v (чистый старт) -> up -d sharded.yml под именем проекта
#      mongodb-cookbook (сеть mongodb-cookbook_default, DNS-имена
#      csrs1/shard1a/shard2a/mongos1).
#   2. Дождаться ping у csrs1/shard1a/shard2a (mongod поднялись).
#   3. rs.initiate() КАЖДОГО RS в своей роли:
#        csrs   (configsvr, 1 узел) на csrs1;
#        shard1 (shardsvr,  1 узел) на shard1a;
#        shard2 (shardsvr,  1 узел) на shard2a;
#      дождаться PRIMARY у каждого.
#   4. Дождаться готовности mongos1 (он подключается к csrs), затем
#      sh.addShard() обоих шардов (compose/init/shard-init.js) через mongos.
#   5. Через mongos: enableSharding(cookbook); shardCollection двух коллекций
#      ОДНОГО датасета orders — orders_hashed {_id:"hashed"} и orders_ranged
#      {_id:1} (ranged на монотонном ObjectID); уменьшить размер чанка до
#      ${CHUNK_MB}MB (чтобы получить осмысленное число чанков и реальную
#      балансировку); ВЫКЛЮЧИТЬ балансировщик (чтобы pre-balance срез отражал
#      чистую раскладку роутера по shard key на вставке).
#   6. Перегенерировать датасет (dataset/main.go, seed=42), проверить counts,
#      импортировать orders в ОБЕ шардированные коллекции через mongos.
#   7. Запустить sharding/main.go (golang:1.25, сеть) — измеряет распределение
#      чанков (pre/post balance), targeting, включает балансировщик и ждёт
#      схождения, пробует resharding. Падает (log.Fatalf) на провале жёсткого
#      ассерта.
#   8. down -v (стенд эфемерный).
#
# Запуск (из mongodb/):
#   bash ops/sharding-demo.sh 2>&1 | tee /tmp/mongo-sharding.txt
# Требует: docker. Поднимает 4 контейнера (csrs1/shard1a/shard2a/mongos1),
# запускает 2 одноразовых контейнера golang:1.25 (dataset gen + стенд).

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> mongodb/
ROOT="$(pwd)"

PROJECT="mongodb-cookbook"
NETWORK="${PROJECT}_default"
COMPOSE_ARGS=(-p "$PROJECT" -f compose/sharded.yml)
# Стенд подключается к РОУТЕРУ mongos, не к отдельному шарду.
MONGO_URI="mongodb://mongos1:27017/"
CHUNK_MB=1   # маленький чанк -> больше чанков -> видимая балансировка

step() { echo; echo "=== $* ==="; }

# mongosh на конкретном сервисе, тихо; "" при недоступности.
msh() { docker compose "${COMPOSE_ARGS[@]}" exec -T "$1" mongosh --quiet --eval "$2" 2>/dev/null || true; }

wait_ping() {
  local svc="$1" ok=0
  for i in $(seq 1 40); do
    if docker compose "${COMPOSE_ARGS[@]}" exec -T "$svc" mongosh --quiet --eval "db.adminCommand('ping')" >/dev/null 2>&1; then
      ok=1; break
    fi
    sleep 2
  done
  if [ "$ok" -ne 1 ]; then echo "  $svc не ответил на ping за отведённое время" >&2; exit 1; fi
  echo "  $svc: ping OK"
}

wait_primary() {
  local svc="$1" ok=0
  for i in $(seq 1 40); do
    if [ "$(msh "$svc" 'db.hello().isWritablePrimary')" = "true" ]; then ok=1; break; fi
    sleep 2
  done
  if [ "$ok" -ne 1 ]; then echo "  $svc не стал PRIMARY за отведённое время" >&2; exit 1; fi
  echo "  $svc: PRIMARY готов"
}

cleanup() {
  echo
  echo "=== down -v (стенд эфемерный) ==="
  docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true
}

step "0 down -v (чистый старт)"
docker compose "${COMPOSE_ARGS[@]}" down -v --remove-orphans 2>&1 || true

step "1 up -d: sharded.yml (проект $PROJECT, сеть $NETWORK)"
docker compose "${COMPOSE_ARGS[@]}" up -d

# С этого момента при любой ошибке — аварийный down -v.
trap cleanup EXIT

step "2 ждём ping у csrs1/shard1a/shard2a"
wait_ping csrs1
wait_ping shard1a
wait_ping shard2a

step "3 rs.initiate() трёх replica set + PRIMARY"
echo "  -> csrs (configsvr, 1 узел)"
msh csrs1 'rs.initiate({_id:"csrs", configsvr:true, members:[{_id:0, host:"csrs1:27017"}]})'
wait_primary csrs1
echo "  -> shard1 (shardsvr, 1 узел)"
msh shard1a 'rs.initiate({_id:"shard1", members:[{_id:0, host:"shard1a:27017"}]})'
wait_primary shard1a
echo "  -> shard2 (shardsvr, 1 узел)"
msh shard2a 'rs.initiate({_id:"shard2", members:[{_id:0, host:"shard2a:27017"}]})'
wait_primary shard2a

step "4 ждём mongos1 (роутер подключается к csrs) + addShard обоих шардов"
ok=0
for i in $(seq 1 40); do
  if docker compose "${COMPOSE_ARGS[@]}" exec -T mongos1 mongosh --quiet --eval "db.adminCommand('ping')" >/dev/null 2>&1; then
    ok=1; break
  fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then echo "  mongos1 не поднялся за отведённое время" >&2; exit 1; fi
echo "  mongos1: доступен"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongos1 mongosh --quiet < compose/init/shard-init.js
nshards="$(msh mongos1 'db.getSiblingDB("config").shards.countDocuments({})')"
echo "  зарегистрировано шардов: $nshards"
if [ "$nshards" != "2" ]; then echo "  ожидалось 2 шарда, получено '$nshards'" >&2; exit 1; fi

step "5 enableSharding + shardCollection (hashed/ranged) + chunksize=${CHUNK_MB}MB + stopBalancer"
docker compose "${COMPOSE_ARGS[@]}" exec -T mongos1 mongosh --quiet --eval "
  sh.enableSharding('cookbook');
  // Размер чанка задаём ДО shardCollection, чтобы балансировка резала мельче.
  db.getSiblingDB('config').settings.updateOne({_id:'chunksize'}, {\$set:{value: ${CHUNK_MB}}}, {upsert:true});
  sh.shardCollection('cookbook.orders_hashed', {_id: 'hashed'});
  sh.shardCollection('cookbook.orders_ranged', {_id: 1});
  sh.stopBalancer();
  print('shard key orders_hashed = ' + JSON.stringify(db.getSiblingDB('config').collections.findOne({_id:'cookbook.orders_hashed'}).key));
  print('shard key orders_ranged = ' + JSON.stringify(db.getSiblingDB('config').collections.findOne({_id:'cookbook.orders_ranged'}).key));
  print('balancer running = ' + sh.getBalancerState());
"

step "6 генерация датасета (dataset/main.go, seed=42) + import orders в ОБЕ коллекции через mongos"
mkdir -p .gocache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/dataset golang:1.25 go run .
lines="$(wc -l < dataset/out/orders.jsonl | tr -d ' ')"
echo "  orders.jsonl: $lines строк"
docker compose "${COMPOSE_ARGS[@]}" cp "dataset/out/orders.jsonl" "mongos1:/tmp/orders.jsonl"
for coll in orders_hashed orders_ranged; do
  echo "  -> mongoimport $coll"
  docker compose "${COMPOSE_ARGS[@]}" exec -T mongos1 mongoimport \
    --uri="$MONGO_URI" --db=cookbook --collection="$coll" \
    --file="/tmp/orders.jsonl"
done

step "7 стенд sharding/main.go (golang:1.25, сеть $NETWORK, через mongos)"
set +e
docker run --rm --network "$NETWORK" \
  -e MONGO_URI="$MONGO_URI" \
  -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/sharding golang:1.25 \
  go run .
STAND_EXIT=$?
set -e

# Явный down -v делает trap на EXIT; здесь только код возврата стенда.
if [ "$STAND_EXIT" -ne 0 ]; then
  echo "sharding: стенд завершился с ошибкой (exit=$STAND_EXIT) — см. лог выше." >&2
  exit "$STAND_EXIT"
fi

echo
echo "sharding-demo: готово. FIXTURE-строки выше -> дословно в FIXTURES.md §6."
