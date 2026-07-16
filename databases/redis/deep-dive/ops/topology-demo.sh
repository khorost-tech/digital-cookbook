#!/usr/bin/env bash
# Стенд #4: репликация, Cluster, Sentinel — оркестрация полного live-прогона.
#
# Подкоманды:
#   ./ops/topology-demo.sh reshard          <образ>
#   ./ops/topology-demo.sh cluster-failover <образ> [повторов=3]
#   ./ops/topology-demo.sh sentinel-failover <образ> [повторов=3]
#   ./ops/topology-demo.sh split-brain      <образ>
#   ./ops/topology-demo.sh all              <образ> [повторов=3]
#
# Go-клиент (topology/) запускается ВНУТРИ одноразового контейнера,
# подключённого к сети compose-топологии (redis-cluster-net /
# redis-sentinel-net), а не с хоста через опубликованные порты — это не
# стилистический выбор, а необходимость: узлы Cluster объявляют себя по
# своему внутреннему docker-адресу (CLUSTER NODES/MOVED показывают
# 172.20.0.x), который с хоста Windows недостижим напрямую через Docker
# Desktop (проверено: `docker exec redis-c1 redis-cli --cluster create ...`
# создаёт кластер, где `cluster nodes` отдаёт 172.20.0.x-адреса; попытка
# TCP-соединения с хоста на такой адрес — `NOT REACHABLE`). Все docker-level
# проверки (kill, inspect, cluster nodes/sentinel опрос «случилось ли
# событие на самом деле») выполняются СНАРУЖИ, с хоста, этим скриптом — у
# него есть родной доступ к docker; Go-клиент внутри контейнера видит только
# то, что видел бы обычный клиент приложения, живущий в той же docker-сети.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

CMD="${1:?usage: topology-demo.sh <reshard|cluster-failover|sentinel-failover|split-brain|all> <image> [reps]}"
IMAGE="${2:?usage: topology-demo.sh <cmd> <image, напр. redis:8.8 или valkey/valkey:8.1> [reps]}"
REPS="${3:-3}"

case "$IMAGE" in
  redis:*) LABEL="redis" ;;
  valkey/*) LABEL="valkey" ;;
  *) LABEL="$(echo "$IMAGE" | tr '/:' '__')" ;;
esac

LOG="scratchout/topology-${LABEL}.log"
mkdir -p scratchout
touch "$LOG"

GORUNNER="golang:1.26"
GOPROXY_VAL="https://go.khorost.tech,direct"
CLUSTER_ADDRS="redis-c1:6379,redis-c2:6379,redis-c3:6379,redis-c4:6379,redis-c5:6379,redis-c6:6379"
SENTINEL_ADDRS="sentinel-1:26379,sentinel-2:26379,sentinel-3:26379"

log() { echo "$*" | tee -a "$LOG"; }

# build_writer компилирует клиент ОДИН раз в topology/bin/topology
# (gitignored) перед сценарием. Это не оптимизация, а условие корректности
# замера: `go run` внутри контейнера сначала выкачивает модули и
# компилирует — живьём это заняло ~15-30с. Скрипт же убивает мастера через
# `sleep 5` после старта писателя, и с `go run` килл прилетал РАНЬШЕ, чем
# клиент вообще успевал подключиться: писатель падал с `ping failed` (мастер
# уже мёртв), а сценарий не мерил ничего. С предсобранным бинарником старт
# мгновенный, и `sleep 5` действительно означает 5с реальной записи до
# килла.
build_writer() {
  log "### компилирую клиент topology/bin/topology (один раз, до сценариев) ###"
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$HERE/topology:/topology" -w /topology \
    -e GOPROXY="$GOPROXY_VAL" \
    "$GORUNNER" go build -o /topology/bin/topology . 2>&1 | tee -a "$LOG"
  if [ ! -f "$HERE/topology/bin/topology" ]; then
    log "ОШИБКА: сборка клиента не дала бинарник topology/bin/topology"
    exit 1
  fi
}

# run_go запускает ПРЕДСОБРАННЫЙ бинарник в одноразовом контейнере на
# заданной сети. MSYS_NO_PATHCONV — иначе Git Bash на Windows подменяет
# аргумент `-w /topology` (абсолютный путь ВНУТРИ контейнера) на путь
# Windows-хоста.
run_go() {
  local network="$1"; shift
  MSYS_NO_PATHCONV=1 docker run --rm --network "$network" \
    -v "$HERE/topology:/topology" -w /topology \
    -e REDIS_CLUSTER_ADDRS="$CLUSTER_ADDRS" \
    -e REDIS_SENTINEL_ADDRS="$SENTINEL_ADDRS" \
    "$GORUNNER" /topology/bin/topology "$@"
}

get_node_id() { docker exec "$1" redis-cli -c cluster nodes 2>/dev/null | grep myself | awk '{print $1}'; }

list_cluster_masters() {
  for c in redis-c1 redis-c2 redis-c3 redis-c4 redis-c5 redis-c6; do
    if docker exec "$c" redis-cli -c cluster nodes 2>/dev/null | grep myself | grep -qw master; then
      echo "$c"
    fi
  done
}

find_replica_of() {
  local master_id="$1"
  for c in redis-c1 redis-c2 redis-c3 redis-c4 redis-c5 redis-c6; do
    if docker exec "$c" redis-cli -c cluster nodes 2>/dev/null | grep myself | grep -q "slave $master_id"; then
      echo "$c"
      return
    fi
  done
}

# ------------------------------------------------------------------
# Cluster: подъём/снос, ожидание готовности и cluster_state:ok
# ------------------------------------------------------------------

cluster_up() {
  log "### поднимаю Cluster (${IMAGE}) ###"
  REDIS_IMAGE="$IMAGE" docker compose -f compose/cluster.yml up -d 2>&1 | tee -a "$LOG"
  log "жду PING на всех 6 узлах..."
  for c in redis-c1 redis-c2 redis-c3 redis-c4 redis-c5 redis-c6; do
    for i in $(seq 1 30); do
      docker exec "$c" redis-cli ping >/dev/null 2>&1 && break
      sleep 1
    done
  done
  docker exec redis-c1 redis-cli --cluster create \
    redis-c1:6379 redis-c2:6379 redis-c3:6379 redis-c4:6379 redis-c5:6379 redis-c6:6379 \
    --cluster-replicas 1 --cluster-yes 2>&1 | tee -a "$LOG"

  local state=""
  for i in $(seq 1 20); do
    state="$(docker exec redis-c1 redis-cli -c cluster info 2>/dev/null | tr -d '\r' | grep -oE 'cluster_state:[a-z]+' | cut -d: -f2)"
    [ "$state" = "ok" ] && break
    sleep 1
  done
  log "cluster_state после create+settle: $state"
  if [ "$state" != "ok" ]; then
    log "ОШИБКА: cluster_state не стал ok за 20с — кластер не сформировался, дальнейшие шаги бессмысленны"
    exit 1
  fi
  docker exec redis-c1 redis-cli -c cluster slots 2>&1 | tee -a "$LOG"
}

cluster_down() {
  docker compose -f compose/cluster.yml down -v 2>&1 | tee -a "$LOG"
}

# ------------------------------------------------------------------
# Сценарий: resharding вживую во время записи
# ------------------------------------------------------------------

scenario_reshard() {
  log ""
  log "########## reshard: resharding вживую во время записи (${IMAGE}) ##########"
  cluster_up

  local masters
  masters=($(list_cluster_masters))
  log "мастера после create: ${masters[*]}"
  if [ "${#masters[@]}" -lt 2 ]; then
    log "ОШИБКА: меньше 2 мастеров обнаружено, resharding не имеет смысла"
    cluster_down
    return 1
  fi
  local from_c="${masters[0]}" to_c="${masters[1]}"
  local from_id to_id
  from_id="$(get_node_id "$from_c")"
  to_id="$(get_node_id "$to_c")"
  log "перенос 2000 слотов: $from_c ($from_id) -> $to_c ($to_id), одновременно 100000 записей клиентом"

  local writer_log="scratchout/.reshard-writer.tmp.log"
  ( run_go redis-cluster-net -scenario cluster-writes-during-reshard -n 100000 ) > "$writer_log" 2>&1 &
  local writer_pid=$!
  sleep 1
  local reshard_t0 reshard_t1 reshard_rc
  reshard_t0=$(date +%s%3N)
  # Код возврата reshard берём через PIPESTATUS[0] (без этого `| tee` вернул бы
  # свой собственный успех и провал resharding остался бы незамеченным;
  # `|| true` при этом PIPESTATUS не затирает — проверено).
  # Провал НЕ роняет матрицу (set -e обошли через `|| true`): на
  # valkey/valkey:8.1 resharding под нагрузкой живьём падал с `NOREPLICAS Not
  # enough good replicas to write`, и это само по себе результат, который
  # надо зафиксировать, — но терять из-за него остальные три сценария
  # (cluster-failover/sentinel/split-brain) было бы неоправданно.
  docker exec redis-c1 redis-cli --cluster reshard redis-c1:6379 \
    --cluster-from "$from_id" --cluster-to "$to_id" \
    --cluster-slots 2000 --cluster-yes 2>&1 | tee -a "$LOG" || true
  reshard_rc=${PIPESTATUS[0]}
  reshard_t1=$(date +%s%3N)
  if [ "$reshard_rc" = "0" ]; then
    log "redis-cli --cluster reshard заняло $((reshard_t1 - reshard_t0))ms (2000 слотов), код возврата=0"
  else
    log "ВНИМАНИЕ: redis-cli --cluster reshard ЗАВЕРШИЛСЯ С ОШИБКОЙ (код возврата=$reshard_rc) через $((reshard_t1 - reshard_t0))ms — время НЕ является временем успешного переноса 2000 слотов, цитировать его как таковое нельзя; см. вывод выше"
    log "  фактическое распределение слотов на этот момент (сколько успело переехать до ошибки):"
    docker exec redis-c1 redis-cli -c cluster nodes 2>&1 | grep master | tee -a "$LOG"
  fi

  wait "$writer_pid" || true
  cat "$writer_log" | tee -a "$LOG"
  rm -f "$writer_log"

  log "--- cluster slots после reshard ---"
  docker exec redis-c1 redis-cli -c cluster slots 2>&1 | tee -a "$LOG"

  cluster_down
}

# ------------------------------------------------------------------
# Сценарий: Cluster failover (убийство мастера)
# ------------------------------------------------------------------

scenario_cluster_failover() {
  local reps="$1"
  log ""
  log "########## cluster-failover: убийство мастера, $reps повтор(ов) (${IMAGE}) ##########"

  for rep in $(seq 1 "$reps"); do
    log ""
    log "--- cluster-failover прогон $rep/$reps ---"
    cluster_up

    local masters
    masters=($(list_cluster_masters))
    local victim="${masters[0]}"
    local witness="${masters[1]}"
    local victim_id
    victim_id="$(get_node_id "$victim")"
    local replica
    replica="$(find_replica_of "$victim_id")"
    log "жертва=$victim (id=$victim_id), свидетель=$witness, реплика жертвы=$replica"
    if [ -z "$replica" ]; then
      log "ОШИБКА: не нашли реплику жертвы — прогон недействителен"
      cluster_down
      continue
    fi
    # id реплики фиксируем ДО килла — это cluster-id, он не меняется, когда
    # контейнер станет мастером, но после смерти жертвы find_replica_of/
    # get_node_id для реплики можно спрашивать только у ДРУГИХ узлов.
    local replica_id
    replica_id="$(get_node_id "$replica")"

    local writer_log="scratchout/.cf-writer.tmp.log"
    ( run_go redis-cluster-net -scenario cluster-failover-writes -duration 90s ) > "$writer_log" 2>&1 &
    local writer_pid=$!
    sleep 5

    local kill_t0
    kill_t0=$(date +%s%3N)
    docker kill -s SIGKILL "$victim" 2>&1 | tee -a "$LOG"

    # Подтверждаем реальный SIGKILL (Status=exited, ExitCode=137=128+9) —
    # не предполагаем, что docker kill синхронно означает «уже мёртв на
    # уровне docker inspect».
    local status=""
    for i in $(seq 1 20); do
      status="$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$victim" 2>/dev/null)"
      [ "$status" = "exited 137" ] && break
      sleep 0.5
    done
    log "docker inspect $victim после kill: status=$status"
    if [ "$status" != "exited 137" ]; then
      log "ОШИБКА: контейнер не подтвердил SIGKILL (ожидался 'exited 137') — прогон недействителен"
    fi

    # Опрос промоушена на СВИДЕТЕЛЕ (другой живой мастер) через
    # `cluster nodes | grep <id реплики>` — ждём, пока строка сменит role
    # с slave на master. Это server-side ground truth, не предположение.
    # id реплики зафиксирован ДО килла (переменная replica_id уже известна
    # заранее — это cluster-id, он не меняется, когда контейнер станет
    # мастером).
    local promote_deadline=$(( $(date +%s%3N) + 40000 ))
    local promoted_at_ms=""
    while [ "$(date +%s%3N)" -lt "$promote_deadline" ]; do
      local nodeline
      nodeline="$(docker exec "$witness" redis-cli -c cluster nodes 2>/dev/null | grep "^$replica_id " || true)"
      if echo "$nodeline" | grep -q " master "; then
        promoted_at_ms=$(date +%s%3N)
        log "промоушен подтверждён свидетелем ($witness): T+$((promoted_at_ms - kill_t0))ms — $nodeline"
        break
      fi
      sleep 0.5
    done
    if [ -z "$promoted_at_ms" ]; then
      log "ОШИБКА: реплика $replica (id=$replica_id) не стала master за 40с по данным свидетеля $witness — прогон недействителен"
    fi

    wait "$writer_pid" || true
    cat "$writer_log" | tee -a "$LOG"
    rm -f "$writer_log"

    log "--- cluster nodes после failover (со свидетеля $witness) ---"
    docker exec "$witness" redis-cli -c cluster nodes 2>&1 | tee -a "$LOG"

    cluster_down
  done
}

# ------------------------------------------------------------------
# Sentinel: подъём/снос
# ------------------------------------------------------------------

sentinel_up() {
  log "### поднимаю Sentinel (${IMAGE}) ###"
  REDIS_IMAGE="$IMAGE" docker compose -f compose/sentinel.yml up -d 2>&1 | tee -a "$LOG"
  for i in $(seq 1 30); do
    docker exec redis-master redis-cli ping >/dev/null 2>&1 && break
    sleep 1
  done
  for i in $(seq 1 30); do
    local ok=1
    for s in sentinel-1 sentinel-2 sentinel-3; do
      docker exec "$s" redis-cli -p 26379 sentinel get-master-addr-by-name mymaster >/dev/null 2>&1 || ok=0
    done
    [ "$ok" = "1" ] && break
    sleep 1
  done
  log "sentinel get-master-addr-by-name (sentinel-1): $(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>&1 | tr '\n' ' ')"
}

sentinel_down() {
  docker compose -f compose/sentinel.yml down -v 2>&1 | tee -a "$LOG"
}

# ------------------------------------------------------------------
# Сценарий: Sentinel failover (убийство мастера)
# ------------------------------------------------------------------

scenario_sentinel_failover() {
  local reps="$1"
  log ""
  log "########## sentinel-failover: убийство redis-master, $reps повтор(ов) (${IMAGE}) ##########"

  for rep in $(seq 1 "$reps"); do
    log ""
    log "--- sentinel-failover прогон $rep/$reps ---"
    sentinel_up

    log "sentinel_tilt на всех 3 узлах ДО килла (диагностика — TILT на этом хосте наблюдался живьём, см. отчёт):"
    for s in sentinel-1 sentinel-2 sentinel-3; do
      log "  $s: $(docker exec "$s" redis-cli -p 26379 info sentinel 2>/dev/null | grep -E 'sentinel_tilt:|sentinel_total_tilt:' | tr '\n' ' ')"
    done

    local writer_log="scratchout/.sf-writer.tmp.log"
    ( run_go redis-sentinel-net -scenario sentinel-failover -duration 45s ) > "$writer_log" 2>&1 &
    local writer_pid=$!
    sleep 5

    local kill_t0
    kill_t0=$(date +%s%3N)
    docker kill -s SIGKILL redis-master 2>&1 | tee -a "$LOG"

    local status=""
    for i in $(seq 1 20); do
      status="$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' redis-master 2>/dev/null)"
      [ "$status" = "exited 137" ] && break
      sleep 0.5
    done
    log "docker inspect redis-master после kill: status=$status"
    if [ "$status" != "exited 137" ]; then
      log "ОШИБКА: redis-master не подтвердил SIGKILL — прогон недействителен"
    fi

    # Ground truth со стороны host: опрашиваем sentinel-1 напрямую,
    # независимо от того, что увидел (или не увидел) Go-клиент. Дедлайн
    # 80с (не 30с) — сознательно щедрый: на этом хосте живьём наблюдался
    # TILT-режим Sentinel (`INFO sentinel` -> sentinel_tilt). Sentinel входит
    # в TILT, когда задержка итерации его event loop превысила 2с (либо часы
    # прыгнули назад) — на нагруженном виртуализованном хосте первое
    # случается регулярно. В TILT Sentinel продолжает собирать информацию, но
    # перестаёт ДЕЙСТВОВАТЬ, то есть failover не выполняется вообще; короткий
    # дедлайн тут превратил бы реальную, но медленную, деградацию в
    # фиктивную «ошибку скрипта».
    local promote_deadline=$(( $(date +%s%3N) + 80000 ))
    local host_promoted_ms="" host_promoted_addr=""
    local initial_addr
    initial_addr="$(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>/dev/null | tr '\n' ' ')"
    while [ "$(date +%s%3N)" -lt "$promote_deadline" ]; do
      local addr
      addr="$(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>/dev/null | tr '\n' ' ')"
      if [ -n "$addr" ] && [ "$addr" != "$initial_addr" ]; then
        host_promoted_ms=$(date +%s%3N)
        host_promoted_addr="$addr"
        log "host-опрос sentinel-1: адрес сменился на T+$((host_promoted_ms - kill_t0))ms: '$initial_addr' -> '$addr'"
        break
      fi
      sleep 0.3
    done
    if [ -z "$host_promoted_ms" ]; then
      log "НАБЛЮДЕНИЕ: sentinel-1 не сообщил новый адрес мастера за 80с (host-опрос) — см. sentinel_tilt выше; прогон фиксируется как «промоушен не случился в отведённое время», не как ошибка скрипта"
      log "  sentinel_tilt на момент дедлайна: $(docker exec sentinel-1 redis-cli -p 26379 info sentinel 2>/dev/null | grep -E 'sentinel_tilt:|sentinel_total_tilt:' | tr '\n' ' ')"
    fi

    wait "$writer_pid" || true
    cat "$writer_log" | tee -a "$LOG"
    rm -f "$writer_log"

    log "--- INFO replication на исходном redis-master после рестарта (проверка демоушена) ---"
    REDIS_IMAGE="$IMAGE" docker compose -f compose/sentinel.yml up -d redis-master 2>&1 | tee -a "$LOG"
    sleep 3
    docker exec redis-master redis-cli info replication 2>&1 | grep -E '^role:|^master_host:' | tee -a "$LOG"

    sentinel_down
  done
}

# ------------------------------------------------------------------
# Сценарий: split-brain
# ------------------------------------------------------------------

scenario_split_brain() {
  log ""
  log "########## split-brain: партиция redis-master от Sentinel-кворума (${IMAGE}) ##########"
  sentinel_up

  # redis-master заранее «двурукий»: остаётся на redis-sentinel-net (видят
  # Sentinel+реплики) И дополнительно подключается ко второй, отдельной
  # сети (redis-splitbrain-direct), где находится ТОЛЬКО изолированный
  # писатель. `docker network disconnect redis-sentinel-net redis-master`
  # снимает ТОЛЬКО первый интерфейс — второй остаётся живым, поэтому
  # изолированный писатель продолжает реально видеть мастер по TCP, пока
  # Sentinel и реплики его не видят вовсе. Обычный `docker network
  # disconnect` без второй сети убрал бы ОБА пути одновременно (мы это
  # проверили: прямое TCP-соединение с хоста на internal-IP убитого
  # контейнера — NOT REACHABLE, интерфейс контейнера выдёргивается
  # полностью), так что без dual-homing «клиент, продолжающий писать в
  # изолированный мастер» продемонстрировать в этой среде было бы нельзя.
  docker network create --subnet 172.29.0.0/16 redis-splitbrain-direct >/dev/null 2>&1 || true
  docker network connect --ip 172.29.0.10 redis-splitbrain-direct redis-master
  log "redis-master подключён ко второй сети redis-splitbrain-direct (dual-homed), его адрес там: 172.29.0.10"

  local writer_log="scratchout/.sb-writer.tmp.log"
  # REDIS_ADDR — статический IP мастера во ВТОРОЙ сети (172.29.0.10), а не
  # имя: имя мёртвого/отключённого от сети контейнера в Docker DNS
  # таймаутит, и писатель мерил бы DNS, а не доступность мастера.
  MSYS_NO_PATHCONV=1 docker run --rm --network redis-splitbrain-direct --name splitbrain-writer \
    -v "$HERE/topology:/topology" -w /topology \
    -e REDIS_ADDR="172.29.0.10:6379" \
    "$GORUNNER" /topology/bin/topology -scenario split-brain-writer -duration 40s > "$writer_log" 2>&1 &
  local writer_pid=$!
  sleep 3

  local part_t0
  part_t0=$(date +%s%3N)
  docker network disconnect redis-sentinel-net redis-master 2>&1 | tee -a "$LOG"
  log "T+0: redis-master отключён от redis-sentinel-net (Sentinel/реплики его больше не видят); redis-splitbrain-direct остаётся живой"

  # Пока идёт партиция, проверяем ground truth с host-стороны: видит ли
  # Sentinel деградацию мастера, промоутит ли новую реплику.
  local initial_addr
  initial_addr="$(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>/dev/null | tr '\n' ' ')"
  log "адрес мастера по Sentinel ДО партиции: $initial_addr"

  local promote_deadline=$(( $(date +%s%3N) + 60000 ))
  local promoted_ms="" promoted_addr=""
  local diag_next=$(( $(date +%s%3N) + 10000 ))
  while [ "$(date +%s%3N)" -lt "$promote_deadline" ]; do
    local addr
    addr="$(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>/dev/null | tr '\n' ' ')"
    if [ -n "$addr" ] && [ "$addr" != "$initial_addr" ]; then
      promoted_ms=$(date +%s%3N)
      promoted_addr="$addr"
      log "Sentinel сменил адрес мастера на T+$((promoted_ms - part_t0))ms: '$initial_addr' -> '$addr'"
      break
    fi
    # Каждые 10с — диагностика ПОЧЕМУ промоушена всё ещё нет. Без этих
    # строк «Sentinel не промоутил» — это констатация без причины, из
    # которой нельзя отличить «Sentinel не увидел мастер мёртвым» от
    # «увидел, но в TILT» от «увидел, но не набрал кворум».
    if [ "$(date +%s%3N)" -ge "$diag_next" ]; then
      log "  диагностика T+$(( $(date +%s%3N) - part_t0 ))ms: $(docker exec sentinel-1 redis-cli -p 26379 info sentinel 2>/dev/null | grep -E 'sentinel_tilt:|master0:' | tr '\n' ' ')"
      log "    флаги мастера по sentinel-1: $(docker exec sentinel-1 redis-cli -p 26379 sentinel master mymaster 2>/dev/null | sed -n '/^flags$/{n;p;}')"
      diag_next=$(( $(date +%s%3N) + 10000 ))
    fi
    sleep 0.5
  done
  if [ -z "$promoted_ms" ]; then
    log "Sentinel НЕ сменил адрес мастера за 60с партиции — фиксируем как наблюдение (см. отчёт), не как ошибку скрипта"
    log "  финальное состояние sentinel-1: $(docker exec sentinel-1 redis-cli -p 26379 info sentinel 2>/dev/null | grep -E 'sentinel_tilt:|sentinel_total_tilt:|master0:' | tr '\n' ' ')"
  fi

  wait "$writer_pid" || true
  log "--- лог изолированного писателя (пишет напрямую в старый мастер через redis-splitbrain-direct) ---"
  cat "$writer_log" | tee -a "$LOG"
  rm -f "$writer_log"

  # Пока писатель работал (40с), партиция уже какое-то время длится.
  # Воссоединяем сеть и смотрим, что происходит со старым мастером.
  local reunite_t0
  reunite_t0=$(date +%s%3N)
  # `--ip 172.28.0.10` ОБЯЗАТЕЛЕН. Без него docker выдаёт вернувшемуся
  # контейнеру НОВЫЙ адрес из пула, а Sentinel мониторит именно 172.28.0.10 —
  # старый мастер возвращается «не туда», Sentinel его никогда не находит, не
  # шлёт ему REPLICAOF и не демоутит. Первый прогон именно так и выглядел
  # («старый мастер НЕ стал slave за 60с, 80 ключей на месте») — и это была бы
  # ложная находка про поведение Redis/Valkey, хотя на деле — дефект стенда:
  # настоящая сетевая партиция адрес узла не меняет.
  docker network connect --ip 172.28.0.10 redis-sentinel-net redis-master 2>&1 | tee -a "$LOG"
  log "T+0 (от воссоединения): redis-master снова подключён к redis-sentinel-net"

  local demoted_ms=""
  local demote_deadline=$(( $(date +%s%3N) + 60000 ))
  while [ "$(date +%s%3N)" -lt "$demote_deadline" ]; do
    local role
    role="$(docker exec redis-master redis-cli info replication 2>/dev/null | tr -d '\r' | grep '^role:' | cut -d: -f2)"
    if [ "$role" = "slave" ]; then
      demoted_ms=$(date +%s%3N)
      log "старый мастер стал slave (демоушен подтверждён) на T+$((demoted_ms - reunite_t0))ms после воссоединения"
      break
    fi
    sleep 0.5
  done
  if [ -z "$demoted_ms" ]; then
    log "старый мастер НЕ стал slave за 60с после воссоединения — role=$(docker exec redis-master redis-cli info replication 2>/dev/null | tr -d '\r' | grep '^role:')"
  fi

  log "--- INFO replication старого мастера после воссоединения ---"
  docker exec redis-master redis-cli info replication 2>&1 | tee -a "$LOG"

  log "--- проверка: остались ли ключи splitbrain:marker:* на старом мастере после ресинка ---"
  local marker_count
  marker_count="$(docker exec redis-master redis-cli --scan --pattern 'splitbrain:marker:*' 2>/dev/null | wc -l | tr -d ' ')"
  log "splitbrain:marker:* на СТАРОМ мастере (после демоушена/ресинка): $marker_count ключей"

  # Ключевой вопрос всего сценария: сколько записей, которые изолированный
  # мастер ПОДТВЕРДИЛ клиенту (вернул OK), пережило воссоединение. Считать
  # надо на НОВОМ мастере — том, который Sentinel назначил победителем и на
  # который смотрят все нормальные клиенты. Если старый мастер
  # демоутится и делает ресинк с нового, его расходящаяся история
  # затирается, и разница между «подтверждено клиенту» и «есть на новом
  # мастере» — это и есть цена split-brain, ради которой сценарий писался.
  local cur_addr cur_ip
  cur_addr="$(docker exec sentinel-1 redis-cli -p 26379 sentinel get-master-addr-by-name mymaster 2>/dev/null | tr '\n' ' ')"
  cur_ip="$(echo "$cur_addr" | awk '{print $1}')"
  log "--- текущий мастер по данным Sentinel после воссоединения: $cur_addr"
  if [ -n "$cur_ip" ]; then
    local new_master_count
    new_master_count="$(docker exec sentinel-1 redis-cli -h "$cur_ip" -p 6379 --scan --pattern 'splitbrain:marker:*' 2>/dev/null | wc -l | tr -d ' ')"
    log "splitbrain:marker:* на НОВОМ мастере ($cur_ip) — то есть уцелело для обычных клиентов: $new_master_count ключей из 80 подтверждённых изолированному мастеру"
  else
    log "не удалось определить текущий адрес мастера через Sentinel — сравнение старый/новый мастер невозможно"
  fi

  docker network disconnect redis-splitbrain-direct redis-master 2>/dev/null || true
  docker network rm redis-splitbrain-direct 2>/dev/null || true
  sentinel_down
}

# ------------------------------------------------------------------

build_writer

case "$CMD" in
  reshard)
    scenario_reshard
    ;;
  cluster-failover)
    scenario_cluster_failover "$REPS"
    ;;
  sentinel-failover)
    scenario_sentinel_failover "$REPS"
    ;;
  split-brain)
    scenario_split_brain
    ;;
  all)
    scenario_reshard
    scenario_cluster_failover "$REPS"
    scenario_sentinel_failover "$REPS"
    scenario_split_brain
    ;;
  *)
    echo "unknown command: $CMD (ожидается reshard|cluster-failover|sentinel-failover|split-brain|all)" >&2
    exit 1
    ;;
esac

log ""
log "[topology-demo] готово: $LOG"
