#!/usr/bin/env bash
# rack-quorum-demo.sh — оркестрирует часть стенда #6 ("эксплуатация"):
# rack-awareness (реплики партиции раскладываются по РАЗНЫМ rack) и
# KRaft-кворум контроллера (kafka-metadata-quorum.sh describe --status).
# Чистая работа над БРОКЕРОМ/кластером через kafka-*.sh CLI (docker exec) —
# ни Go, ни Java клиентский код здесь не нужен (см. ../go/ops/main.go).
#
# ⚠️ Топология этого стенда — ОДИН брокер на rack (rack-a/kafka1,
# rack-b/kafka2, rack-c/kafka3, см. ../compose/compose.yml). Это значит, что
# broker-id и rack связаны биекцией: ЛЮБОЙ набор из N РАЗНЫХ broker-id
# автоматически покрывает N РАЗНЫХ rack — не потому что rack-aware
# replica-placement алгоритм что-то "умное" считает, а потому что иначе
# физически невозможно (разных броекров с одинаковым rack просто нет).
# Демонстрация ниже честно фиксирует этот факт как ограничение стенда, а не
# выдаёт тривиальный результат за "умный" алгоритм — см. content-note в
# README. Полноценная демонстрация (когда rack-aware размещение реально
# ВЫБИРАЕТ между несколькими брокерами ОДНОГО rack, чтобы не превысить лимит)
# потребовала бы кластер с несколькими брокерами на rack — вне охвата.
#
# Запуск: bash ops/rack-quorum-demo.sh <scenario>
#   scenario: rack | quorum | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
BROKER="kafka-cookbook-1"

# Статическая карта broker-id -> rack -> compose-контейнер, см. ../compose/compose.yml.
rack_of() {
  case "$1" in
    1) echo "rack-a" ;;
    2) echo "rack-b" ;;
    3) echo "rack-c" ;;
    *) echo "unknown" ;;
  esac
}

kt() {
  docker exec "$BROKER" /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 "$@" 2>&1
}

# check_rack_spread TOPIC — для каждой партиции печатает replicas + racks и
# падает, если хотя бы одна пара реплик партиции делит один rack (в этой
# топологии — сигнал реальной ошибки, т.к. одинаковых rack у разных брокеров
# нет).
check_rack_spread() {
  local topic="$1" desc line part replicas ids id r racks seen dup
  desc=$(kt --describe --topic "$topic")
  echo "$desc"
  while IFS= read -r line; do
    case "$line" in *"Partition:"*) ;; *) continue ;; esac
    part=$(echo "$line" | grep -oE 'Partition: [0-9]+' | grep -oE '[0-9]+')
    replicas=$(echo "$line" | grep -oE 'Replicas: [0-9,]+' | grep -oE '[0-9,]+')
    IFS=',' read -ra ids <<< "$replicas"
    racks=""
    dup=0
    declare -A seen=()
    for id in "${ids[@]}"; do
      r=$(rack_of "$id")
      racks="${racks}${id}:${r} "
      if [ -n "${seen[$r]:-}" ]; then dup=1; fi
      seen[$r]=1
    done
    unset seen
    echo "  partition=$part: replicas=[$replicas] -> ${racks}(разных rack: ${#ids[@]} из ${#ids[@]} реплик $([ $dup -eq 0 ] && echo 'ВСЕ разные' || echo 'ЕСТЬ ПОВТОР!'))"
    if [ "$dup" -eq 1 ]; then
      echo "[assert] FAIL: партиция $part топика $topic — две реплики на ОДНОМ rack" >&2
      return 1
    fi
  done <<< "$desc"
  echo "[assert] OK: топик $topic — во всех партициях реплики на РАЗНЫХ rack"
}

scenario_rack() {
  echo "=================================================================="
  echo "=== rack-awareness: размещение реплик по broker.rack ==="
  echo "=================================================================="

  echo "--- карта broker.rack (подтверждена внутри контейнеров, см. compose.yml) ---"
  local id
  for id in 1 2 3; do
    local live_rack
    live_rack=$(docker exec "kafka-cookbook-$id" /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
      --entity-type brokers --entity-name "$id" --describe --all 2>&1 | grep -oE 'broker\.rack=[a-z0-9-]+' | head -1)
    echo "  broker $id ($(rack_of "$id")): $live_rack"
  done

  echo
  echo "--- топик demo-ops-rack-rf3 (6 партиций, RF=3 — в этой топологии ОБЯЗАН занять ВСЕ 3 rack на каждой партиции) ---"
  kt --delete --topic demo-ops-rack-rf3 >/dev/null 2>&1 || true
  sleep 1
  kt --create --topic demo-ops-rack-rf3 --partitions 6 --replication-factor 3
  sleep 1
  check_rack_spread demo-ops-rack-rf3

  echo
  echo "--- топик demo-ops-rack-rf2 (3 партиции, RF=2 — какие ИМЕННО 2 из 3 rack выбраны, меняется от партиции к партиции) ---"
  kt --delete --topic demo-ops-rack-rf2 >/dev/null 2>&1 || true
  sleep 1
  kt --create --topic demo-ops-rack-rf2 --partitions 3 --replication-factor 2
  sleep 1
  check_rack_spread demo-ops-rack-rf2

  echo
  echo "[content-note] В этой топологии (1 брокер = 1 rack, бекция id<->rack) ЛЮБОЙ набор из N различных"
  echo "broker-id автоматически покрывает N различных rack — это структурная гарантия топологии, а не"
  echo "результат 'умного' решения rack-aware алгоритма избегать конкретных брокеров. Алгоритм реально"
  echo "'выбирает' между рэками только когда на rack несколько брокеров (вне охвата этого стенда)."
}

scenario_quorum() {
  echo "=================================================================="
  echo "=== KRaft-кворум контроллера ==="
  echo "=================================================================="
  echo "--- kafka-metadata-quorum.sh describe --status ---"
  docker exec "$BROKER" /opt/kafka/bin/kafka-metadata-quorum.sh --bootstrap-server localhost:9092 describe --status 2>&1
  echo
  echo "--- kafka-metadata-quorum.sh describe --replication ---"
  docker exec "$BROKER" /opt/kafka/bin/kafka-metadata-quorum.sh --bootstrap-server localhost:9092 describe --replication 2>&1
}

main() {
  local scenario="${1:-all}"
  case "$scenario" in
    rack) scenario_rack ;;
    quorum) scenario_quorum ;;
    all)
      scenario_rack
      echo
      scenario_quorum
      ;;
    *)
      echo "неизвестный сценарий: $scenario (rack|quorum|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО: rack-quorum-demo завершён ==="
}

main "$@"
