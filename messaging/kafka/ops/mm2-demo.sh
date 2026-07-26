#!/usr/bin/env bash
# mm2-demo.sh — оркестрирует стенд #9 ("гео/DR — MirrorMaker 2, два кластера").
# Как и ecosystem-demo.sh (стенд #7), здесь нет отдельного Go/Java-клиента:
# демонстрация — чистый CLI (kafka-*.sh внутри контейнеров брокеров), потому
# что предмет показа — сама MM2/CLI-механика (репликация топика, трансляция
# offset, active-active), а не клиентская библиотека.
#
# Топология: us-east — уже поднятый 3-брокерный KRaft-кластер (../compose/compose.yml,
# кластер фундамента серии, ЭТИМ скриптом никогда не сносится). us-west —
# новый single-node KRaft-кластер (../mm2/compose-us-west.yml). MM2 —
# dedicated-режим (connect-mirror-maker.sh), отдельный контейнер
# (../mm2/compose-mm2.yml), подключённый к ОБЕИМ compose-сетям сразу.
#
# Требует: us-east уже up (docker compose -f compose/compose.yml up -d).
#
# Запуск: bash ops/mm2-demo.sh <scenario>
#   scenario: setup | replicate | offsets | active-active | lag | all (по умолчанию)
#
# Cleanup: bash ops/mm2-demo.sh cleanup
#   сносит us-west + mm2 (контейнеры, сети, volumes) — us-east НЕ трогает
#   (кластер может понадобиться другим стендам серии позже).

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
MM2_DIR="mm2"
COMPOSE_WEST="$MM2_DIR/compose-us-west.yml"
COMPOSE_MM2="$MM2_DIR/compose-mm2.yml"

TOPIC="orders"
GROUP="orders-cg"
N_INITIAL=200      # первая партия на us-east перед офсет-сценарием
N_COMMITTED=120     # сколько из них "обработает" консьюмер группы G перед failover
N_LAG_BATCH=300     # вторая партия — для замера лага репликации
N_WEST_LOCAL=50      # партия в локальный orders на us-west — для active-active

wait_healthy() {
  local name="$1" tries=0
  echo "[mm2] жду healthy: $name"
  while [ "$(docker inspect "$name" --format '{{.State.Health.Status}}' 2>/dev/null)" != "healthy" ]; do
    tries=$((tries + 1))
    if [ "$tries" -gt 40 ]; then
      echo "[mm2] $name не стал healthy за отведённое время" >&2
      exit 1
    fi
    sleep 3
  done
  echo "[mm2] $name healthy"
}

sum_offsets() {
  # sum_offsets BROKER TOPIC — суммарный latest-offset по всем партициям топика.
  docker exec "$1" /opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 \
    --topic "$2" --time -1 2>/dev/null | awk -F: '{sum+=$3} END{print sum+0}'
}

topic_exists() {
  docker exec "$1" /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list 2>/dev/null | grep -qx "$2"
}

sum_group_col() {
  # sum_group_col BROKER GROUP TOPIC COL — суммирует колонку COL (индексы как в
  # выводе kafka-consumer-groups.sh --describe: 4=CURRENT-OFFSET,
  # 5=LOG-END-OFFSET, 6=LAG) по ВСЕМ строкам заданного TOPIC (все партиции,
  # не только партиция 0 — реальное распределение записей по партициям не
  # гарантировано). Печатает "SUM N_ROWS" (N_ROWS — сколько партиций найдено,
  # 0 если группа/топик ещё не видны в --describe).
  docker exec "$1" /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --describe --group "$2" 2>/dev/null | awk -v t="$3" -v c="$4" '$2==t{sum+=$c; n++} END{print sum+0, n+0}'
}

scenario_setup() {
  echo "=== [mm2] setup: us-west + MM2 (active-passive, mm2.properties) ==="
  if ! docker inspect kafka-cookbook-1 >/dev/null 2>&1; then
    echo "[mm2] us-east (kafka-cookbook-1/2/3) не поднят — сначала: docker compose -f compose/compose.yml up -d" >&2
    exit 1
  fi
  docker compose -f "$COMPOSE_WEST" up -d
  wait_healthy kafka-cookbook-west-1
  docker compose -f "$COMPOSE_WEST" -f "$COMPOSE_MM2" up -d mm2
  echo "[mm2] жду инициализацию трёх коннекторов MM2 (source/checkpoint/heartbeat)..."
  sleep 12
  echo "--- статус коннекторов (по логу, dedicated-режим REST не публикует) ---"
  docker logs kafka-cookbook-mm2 2>&1 | grep -oE "connectorIds=\[[^]]*\]" | tail -3
  if docker logs kafka-cookbook-mm2 2>&1 | grep -qiE "ERROR|Exception" ; then
    echo "[mm2] ⚠️ в логе MM2 есть ERROR/Exception — проверь docker logs kafka-cookbook-mm2" >&2
  fi
}

scenario_replicate() {
  echo "=== [mm2] replicate: топик '$TOPIC' us-east -> us-west (реплицированный 'us-east.$TOPIC') ==="
  if ! topic_exists kafka-cookbook-1 "$TOPIC"; then
    docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
      --create --topic "$TOPIC" --partitions 3 --replication-factor 3 --config min.insync.replicas=2
  else
    echo "[mm2] '$TOPIC' уже существует на us-east — пропускаю create (идемпотентность повторного прогона)"
  fi
  local before
  before=$(sum_offsets kafka-cookbook-1 "$TOPIC")
  echo "[mm2] '$TOPIC' на us-east ДО продюсинга: $before записей"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-verifiable-producer.sh --topic "$TOPIC" \
    --bootstrap-server kafka1:9092,kafka2:9092,kafka3:9092 --max-messages "$N_INITIAL" --acks -1 \
    | tail -1
  local after
  after=$(sum_offsets kafka-cookbook-1 "$TOPIC")
  echo "[mm2] '$TOPIC' на us-east ПОСЛЕ продюсинга: $after записей (+$((after - before)))"

  echo "[mm2] жду репликацию (refresh.topics.interval.seconds=5 + перенос данных)..."
  local tries=0 west_count=0
  while [ "$tries" -lt 20 ]; do
    if topic_exists kafka-cookbook-west-1 "us-east.$TOPIC"; then
      west_count=$(sum_offsets kafka-cookbook-west-1 "us-east.$TOPIC")
      if [ "$west_count" = "$after" ]; then break; fi
    fi
    tries=$((tries + 1))
    sleep 2
  done
  echo "[mm2] 'us-east.$TOPIC' на us-west: $west_count записей (ожидалось $after)"
  if [ "$west_count" = "$after" ]; then
    echo "[assert] OK: us-west содержит 'us-east.$TOPIC' с $after записями == us-east"
  else
    echo "[assert] FAIL: west_count=$west_count != $after" >&2
    exit 1
  fi
}

scenario_offsets() {
  echo "=== [mm2] offsets: трансляция consumer-offset + симуляция failover ==="
  echo "[mm2] группа '$GROUP' читает $N_COMMITTED/$(sum_offsets kafka-cookbook-1 "$TOPIC") записей '$TOPIC' на us-east (коммитит offset)..."
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-console-consumer.sh \
    --bootstrap-server kafka1:9092,kafka2:9092,kafka3:9092 \
    --topic "$TOPIC" --group "$GROUP" --from-beginning --max-messages "$N_COMMITTED" --timeout-ms 30000 \
    | tail -1
  echo "--- committed offset на us-east (группа '$GROUP') ---"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --describe --group "$GROUP"

  echo "[mm2] жду MirrorCheckpointConnector.sync.group.offsets (interval=5s)..."
  sleep 15
  echo "--- транслированный offset на us-west (та же группа '$GROUP', топик 'us-east.$TOPIC') ---"
  docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --describe --group "$GROUP"
  local translated_sum translated_n
  read -r translated_sum translated_n <<< "$(sum_group_col kafka-cookbook-west-1 "$GROUP" "us-east.$TOPIC" 4)"
  echo "[mm2] транслированный committed offset (сумма CURRENT-OFFSET по всем $translated_n партициям) = $translated_sum (source committed = $N_COMMITTED)"
  if [ "$translated_n" -gt 0 ] && [ "$translated_sum" -gt 0 ] && [ "$translated_sum" -le "$N_COMMITTED" ]; then
    echo "[assert] OK: 0 < транслированный offset (сумма по партициям, $translated_sum) <= source committed ($N_COMMITTED) — консервативная трансляция, без потери непрочитанного"
  else
    echo "[assert] FAIL: translated offset (сумма по партициям) вне ожидаемого диапазона: sum=$translated_sum n=$translated_n" >&2
    exit 1
  fi

  echo "[mm2] FAILOVER: consumer группы '$GROUP' стартует на us-west, топик 'us-east.$TOPIC' — БЕЗ --from-beginning (берёт транслированный committed offset по каждой партиции, не полагаемся на 'всё в партиции 0')"
  local topic_total remaining
  topic_total=$(sum_offsets kafka-cookbook-west-1 "us-east.$TOPIC")
  remaining=$(( topic_total - translated_sum ))
  echo "[mm2] осталось дочитать (сумма по всем партициям): $remaining ($topic_total записей на west всего - $translated_sum уже committed)"
  if [ "$remaining" -gt 0 ]; then
    docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-console-consumer.sh \
      --bootstrap-server localhost:9092 --topic "us-east.$TOPIC" --group "$GROUP" \
      --max-messages "$remaining" --timeout-ms 30000 | tail -1
  else
    echo "[mm2] remaining <= 0 — консьюмер уже на конце лога по всем партициям, дочитывать нечего"
  fi

  echo "--- итоговый LAG группы '$GROUP' на us-west (ожидается 0 по каждой партиции) ---"
  local describe_out
  describe_out=$(docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --describe --group "$GROUP")
  echo "$describe_out"

  local lag_sum lag_n
  read -r lag_sum lag_n <<< "$(echo "$describe_out" | awk -v t="us-east.$TOPIC" '$2==t{sum+=$6; n++} END{print sum+0, n+0}')"
  echo "[mm2] суммарный LAG группы '$GROUP' по '$TOPIC'/'us-east.$TOPIC' (все $lag_n партиций) = $lag_sum"
  if [ "$lag_n" -gt 0 ] && [ "$lag_sum" -eq 0 ]; then
    echo "[assert] OK: суммарный LAG по всем партициям == 0 — failover-consumer полностью догнал us-west"
  else
    echo "[assert] FAIL: суммарный LAG != 0 или партиции не найдены: sum=$lag_sum n=$lag_n" >&2
    exit 1
  fi
}

scenario_lag() {
  echo "=== [mm2] lag: характерный прогон замера лага репликации ==="
  local before after target
  before=$(sum_offsets kafka-cookbook-1 "$TOPIC")
  local t0=$(date +%s%3N)
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-verifiable-producer.sh --topic "$TOPIC" \
    --bootstrap-server kafka1:9092,kafka2:9092,kafka3:9092 --max-messages "$N_LAG_BATCH" --acks -1 \
    | tail -1
  local t_produced=$(date +%s%3N)
  target=$(sum_offsets kafka-cookbook-1 "$TOPIC")
  echo "[mm2] продюсинг $N_LAG_BATCH записей занял $((t_produced - t0))ms; us-east '$TOPIC' теперь $target записей"

  local tries=0 west_count=0
  while [ "$tries" -lt 60 ]; do
    west_count=$(sum_offsets kafka-cookbook-west-1 "us-east.$TOPIC")
    if [ "$west_count" = "$target" ]; then break; fi
    tries=$((tries + 1))
    sleep 1
  done
  local t_caught=$(date +%s%3N)
  echo "[mm2] us-west 'us-east.$TOPIC' догнал us-east ($west_count==$target) за $((t_caught - t_produced))ms после завершения продюсинга (host-зависимо)"
}

scenario_active_active() {
  echo "=== [mm2] active-active: репликация в обе стороны, защита от циклов ==="
  if ! topic_exists kafka-cookbook-west-1 "$TOPIC"; then
    docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
      --create --topic "$TOPIC" --partitions 3 --replication-factor 1
  else
    echo "[mm2] '$TOPIC' уже существует локально на us-west — пропускаю create"
  fi

  echo "[mm2] пересоздаю MM2 с mm2-active-active.properties (dedicated-режим не подхватывает новую пару кластеров без рестарта)..."
  MM2_CONFIG=mm2-active-active.properties docker compose -f "$COMPOSE_WEST" -f "$COMPOSE_MM2" up -d --force-recreate mm2
  sleep 15

  local before after
  before=$(sum_offsets kafka-cookbook-west-1 "$TOPIC")
  docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-verifiable-producer.sh --topic "$TOPIC" \
    --bootstrap-server localhost:9092 --max-messages "$N_WEST_LOCAL" --acks -1 | tail -1
  after=$(sum_offsets kafka-cookbook-west-1 "$TOPIC")
  echo "[mm2] локальный '$TOPIC' на us-west: $((after - before)) новых записей"

  echo "[mm2] жду обратную репликацию us-west -> us-east ('us-west.$TOPIC')..."
  local tries=0 east_count=0
  while [ "$tries" -lt 20 ]; do
    if topic_exists kafka-cookbook-1 "us-west.$TOPIC"; then
      east_count=$(sum_offsets kafka-cookbook-1 "us-west.$TOPIC")
      if [ "$east_count" = "$after" ]; then break; fi
    fi
    tries=$((tries + 1))
    sleep 2
  done
  echo "[mm2] 'us-west.$TOPIC' на us-east: $east_count записей (ожидалось $after)"

  echo "--- проверка отсутствия циклов (не должно быть us-west.us-east.$TOPIC / us-east.us-west.$TOPIC) ---"
  local cycle_east cycle_west
  cycle_east=$(docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list | grep -c "^us-west\.us-east\.$TOPIC\$\|^us-east\.us-east\.$TOPIC\$" || true)
  cycle_west=$(docker exec kafka-cookbook-west-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list | grep -c "^us-east\.us-west\.$TOPIC\$\|^us-west\.us-west\.$TOPIC\$" || true)
  echo "[mm2] топики-циклы на us-east: $cycle_east, на us-west: $cycle_west (ожидается 0/0)"
  if [ "$east_count" = "$after" ] && [ "$cycle_east" = "0" ] && [ "$cycle_west" = "0" ]; then
    echo "[assert] OK: 'us-west.$TOPIC' на us-east == $after записей, циклов нет"
  else
    echo "[assert] FAIL: репликация active-active или защита от циклов не сошлись" >&2
    exit 1
  fi
}

scenario_cleanup() {
  echo "=== [mm2] cleanup: сношу us-west + mm2 (us-east НЕ трогаю) ==="
  docker compose -f "$COMPOSE_WEST" -f "$COMPOSE_MM2" rm -sf mm2 2>/dev/null || true
  docker compose -f "$COMPOSE_WEST" down -v
  echo "[mm2] us-west/mm2 снесены. us-east (kafka-cookbook-1/2/3) оставлен как есть."
}

SCENARIO="${1:-all}"
case "$SCENARIO" in
  setup) scenario_setup ;;
  replicate) scenario_replicate ;;
  offsets) scenario_offsets ;;
  lag) scenario_lag ;;
  active-active) scenario_active_active ;;
  cleanup) scenario_cleanup ;;
  all)
    scenario_setup
    scenario_replicate
    scenario_offsets
    scenario_lag
    scenario_active_active
    ;;
  *)
    echo "usage: $0 [setup|replicate|offsets|lag|active-active|cleanup|all]" >&2
    exit 1
    ;;
esac
