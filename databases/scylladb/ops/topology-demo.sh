#!/usr/bin/env bash
# topology-demo.sh — Стенд #5 серии "ScyllaDB: глубокое погружение", живая
# демонстрация НА ОТДЕЛЬНОМ multi-DC кластере (compose/multidc.yml, сеть
# scylla-multidc-net, узлы dc1a/dc1b/dc2a/dc2b) -- НЕ трогает одиночный ДЦ
# кластер Task 1 (compose/compose.yml, scylla1/2/3, сеть scylla-cookbook-net,
# telemetry.readings=672000 строк, нужен целым для Task 7/8/9).
#
# Порядок ВАЖЕН и выбран НЕ произвольно -- живая находка при разработке
# этого стенда: `nodetool decommission` (тест tablets-миграции, шаг 3 ниже)
# НЕОБРАТИМО убирает узел из кольца, поэтому DC-failover тест (шаг 2, нужен
# ЖИВОЙ ПОЛНЫЙ DC2 из 2 узлов, чтобы честно смоделировать "весь ДЦ упал")
# запускается ПЕРВЫМ, пока оба узла DC2 ещё на месте. Порядок наоборот
# (сперва decommission dc2b, потом "остановить весь DC2") подменил бы
# "остановить весь DC2" на "остановить единственный оставшийся узел DC2" --
# другой, менее честный сценарий.
#
#   1. nodetool status -- подтвердить 4xUN в 2 ДЦ.
#   2. DC failover: остановить ОБА узла DC2, показать что LOCAL_QUORUM в DC1
#      продолжает работать, а EACH_QUORUM -- нет; поднять DC2 обратно.
#   3. tablets: снимок распределения tablets (go run . -scenario tablets)
#      ДО, decommission dc2b, снимок ПОСЛЕ -- живая миграция.
#
# Требует: живой multi-DC кластер (docker compose -f compose/multidc.yml up -d),
# дождаться 4xUN (см. README «Стенд #5»). Перед шагом 3 нужен УЖЕ созданный
# keyspace telemetry_mdc (DC1:2,DC2:2) -- создаётся `go run . -scenario multidc`
# ИЛИ идемпотентно этим же скриптом ниже (шаг 2 создаёт его сам, если ещё нет).
#
# Запуск (из scylladb/):
#   bash ops/topology-demo.sh | tee scratchout/tablets.txt

set -uo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> scylladb/
COMPOSE="compose/multidc.yml"
NET="scylla-multidc-net"
GOIMG="golang:1.26"

step() { echo; echo "=== $* ==="; }

run_go() {
  # $1 = SCYLLA_HOSTS, остальное -- аргументы topology
  local hosts="$1"; shift
  docker run --rm --network "$NET" \
    -v "$(pwd):/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app/topology \
    -e "SCYLLA_HOSTS=$hosts" "$GOIMG" \
    sh -c "go run . $*"
}

step "0/6 Базовое состояние: nodetool status (ожидание 4xUN, 2 ДЦ)"
docker exec dc1a nodetool status

step "1/6 Схема telemetry_mdc (идемпотентно, на случай отдельного запуска без сценария multidc)"
docker exec dc1a cqlsh -e "CREATE KEYSPACE IF NOT EXISTS telemetry_mdc WITH replication = {'class':'NetworkTopologyStrategy','DC1':2,'DC2':2}"
docker exec dc1a cqlsh -e "CREATE TABLE IF NOT EXISTS telemetry_mdc.mdc_bench (id text PRIMARY KEY, val double)"
docker exec dc1a cqlsh -e "INSERT INTO telemetry_mdc.mdc_bench (id, val) VALUES ('failover-marker', 0.0)"

step "2/6 DC failover: остановить ОБА узла DC2 (dc2a, dc2b) -- полный отказ ДЦ"
docker compose -f "$COMPOSE" stop dc2a dc2b
echo "Ждём, пока gossip на dc1a увидит DC2 как DN (до 60с)..."
for i in $(seq 1 20); do
  DN_COUNT=$(docker exec dc1a nodetool status 2>/dev/null | grep -cE '^DN')
  if [ "$DN_COUNT" = "2" ]; then break; fi
  sleep 3
done
docker exec dc1a nodetool status

echo
echo "-- LOCAL_QUORUM (DC1) при упавшем DC2 -- запись+чтение через dc1a --"
docker exec dc1a cqlsh -e "CONSISTENCY LOCAL_QUORUM; INSERT INTO telemetry_mdc.mdc_bench (id, val) VALUES ('failover-local-quorum', 1.0);"
LQ_WRITE_RC=$?
docker exec dc1a cqlsh -e "CONSISTENCY LOCAL_QUORUM; SELECT id, val FROM telemetry_mdc.mdc_bench WHERE id='failover-local-quorum';"
LQ_READ_RC=$?
echo "LOCAL_QUORUM write rc=$LQ_WRITE_RC read rc=$LQ_READ_RC (ожидание: 0/0 -- LOCAL_QUORUM не ждёт удалённый упавший DC2)"

echo
echo "-- EACH_QUORUM при упавшем DC2 -- та же операция, ожидание ОТКАЗА --"
docker exec dc1a cqlsh -e "CONSISTENCY EACH_QUORUM; INSERT INTO telemetry_mdc.mdc_bench (id, val) VALUES ('failover-each-quorum', 2.0);"
EQ_WRITE_RC=$?
echo "EACH_QUORUM write rc=$EQ_WRITE_RC (ожидание: НЕ 0 -- EACH_QUORUM требует кворум КАЖДОГО ДЦ, включая мёртвый DC2)"

step "3/6 Восстановление DC2"
docker compose -f "$COMPOSE" start dc2a dc2b
echo "Ждём кворума 4xUN (до 3 минут)..."
for i in $(seq 1 36); do
  UN_COUNT=$(docker exec dc1a nodetool status 2>/dev/null | grep -cE '^UN')
  if [ "$UN_COUNT" = "4" ]; then break; fi
  sleep 5
done
docker exec dc1a nodetool status

echo
echo "-- Ассерт DC failover --"
if [ "$LQ_WRITE_RC" = "0" ] && [ "$LQ_READ_RC" = "0" ]; then
  echo "OK: LOCAL_QUORUM (DC1) отработал при упавшем DC2 (write rc=$LQ_WRITE_RC, read rc=$LQ_READ_RC)"
else
  echo "FAIL: LOCAL_QUORUM (DC1) НЕ отработал при упавшем DC2 (write rc=$LQ_WRITE_RC, read rc=$LQ_READ_RC)"
fi
if [ "$EQ_WRITE_RC" != "0" ]; then
  echo "OK: EACH_QUORUM корректно отказал при упавшем DC2 (rc=$EQ_WRITE_RC)"
else
  echo "FAIL(честно): EACH_QUORUM НЕ отказал (rc=$EQ_WRITE_RC) -- см. README «Стенд #5» за живым разбором"
fi

step "4/6 tablets: распределение ДО decommission (go run . -scenario tablets)"
run_go "dc1a:9042,dc1b:9042,dc2a:9042,dc2b:9042" "-scenario tablets -rows 5000"

step "5/6 decommission dc2b -- ПРОТИВОРЕЧИЕ С telemetry_mdc (DC1:2,DC2:2), живая находка"
echo "ЖИВАЯ НАХОДКА: decommission узла ДЦ, где keyspace требует RF=2 в этом ДЦ, а после ухода"
echo "узла в ДЦ останется всего 1 -- ScyllaDB ОТКАЗЫВАЕТ decommission (см. README «Стенд #5»)."
echo "Честный обходной путь -- ПЕРЕД decommission понизить RF DC2 у telemetry_mdc до 1"
echo "(та же операция, которую в проде делают перед выводом предпоследнего узла ДЦ)."
docker exec dc1a cqlsh -e "ALTER KEYSPACE telemetry_mdc WITH replication = {'class':'NetworkTopologyStrategy','DC1':2,'DC2':1};"
docker exec dc2b nodetool decommission
DECOMM_RC=$?
echo "nodetool decommission dc2b: rc=$DECOMM_RC"
docker exec dc1a nodetool status

step "6/6 tablets: распределение ПОСЛЕ decommission (только 3 живых узла)"
run_go "dc1a:9042,dc1b:9042,dc2a:9042" "-scenario tablets -rows 5000"

echo
echo "-- Ассерт tablets --"
if [ "$DECOMM_RC" = "0" ]; then
  echo "OK: decommission dc2b завершился успешно (rc=0) -- сравни счётчики dc2a/dc2b ДО/ПОСЛЕ выше"
  echo "    (ожидание: dc2b -> 0 реплик, dc2a -> все реплики, ранее бывшие на dc2b)"
else
  echo "FAIL: decommission dc2b завершился с ошибкой (rc=$DECOMM_RC)"
fi

echo
echo "Готово. multi-DC кластер сейчас 3 живых узла (DC1x2, DC2x1) -- teardown делает README/бриф отдельно"
echo "(docker compose -f $COMPOSE down -v), кластер этого стенда полностью одноразовый."
