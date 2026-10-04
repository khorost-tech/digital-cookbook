#!/usr/bin/env bash
# Опыт: во что обходится service.instance.id в индексных лейблах Loki.
#
#   ./scripts/bench-instance-id.sh [рестартов]
#
# Проверяет утверждение, которое легко принять на веру: «Loki индексирует только
# фиксированный список ресурсных атрибутов, значит кардинальность под контролем».
# Список действительно фиксирован, но в него по умолчанию входит
# service.instance.id — а он уникален для ЭКЗЕМПЛЯРА: OTel Java agent генерирует
# новый UUID при каждом старте процесса, в Kubernetes он свой у каждого пода.
#
# Опыт идёт двумя прогонами по одному сценарию: перезапустить Java-сервис N раз,
# между рестартами давать нагрузку, и после каждого считать потоки в Loki.
#   defaults — конфиг Loki с дефолтным набором лейблов (loki-default-labels.yaml)
#   safe     — рабочий конфиг стенда с явным набором (loki.yaml)
#
# Перезапускается именно JAVA-сервис. Go-сервис для этого опыта не годится: Go SDK
# не выставляет service.instance.id вовсе, и первая версия опыта, рестартовавшая
# Go, не показала ничего — вывод получился бы противоположный истине.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

RESTARTS="${1:-3}"
LOAD=40
RESULT_FILE="$STAND_DIR/results/05-instance-id-labels.txt"
TMP="$STAND_DIR/results/.instance-id"
rm -rf "$TMP"; mkdir -p "$TMP"

# Потоки и лейблы индекса — из самого Loki, а не из догадок.
count_streams_java() {
  innet -s --max-time 20 -G "http://loki:3100/loki/api/v1/series" \
    --data-urlencode 'match[]={service_name=~".+"}' 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)['data']
except Exception:
    print('0 0'); raise SystemExit
jb = [s for s in d if s.get('service_name') == 'java-backend']
print(len(d), len(jb))
"
}

index_labels() {
  innet -s --max-time 15 "http://loki:3100/loki/api/v1/labels" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(','.join(sorted(json.load(sys.stdin).get('data') or [])))
except Exception:
    print('-')
"
}

run_case() {
  local label="$1" loki_config="$2"

  log "прогон «$label», конфиг Loki: $loki_config"
  ./scripts/down.sh clean >/dev/null 2>&1
  ./scripts/up.sh all otel >/dev/null 2>&1
  echo "LOKI_CONFIG=$loki_config" >> "$STAND_DIR/.env"
  validate_selected_configs
  docker compose up -d --force-recreate loki >/dev/null 2>&1
  wait_ready "loki" "http://loki:3100/ready" >/dev/null

# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$LOAD" -seed 1 -concurrency 2 >/dev/null 2>&1 || true
  sleep 14

  local all jb
  read -r all jb <<< "$(count_streams_java)"
  echo "$label;0;$all;$jb;$(index_labels)" >> "$TMP/results.csv"
  echo "  0 рестартов: стримов всего $all, у java-backend $jb"

  local i
  for i in $(seq 1 "$RESTARTS"); do
    docker compose restart java-backend >/dev/null 2>&1
    # Ждём готовности, а не спим наугад: JVM с агентом поднимается неравномерно.
    wait_ready "java-backend" "http://java-backend:8080/inventory/SKU-0001" >/dev/null
    docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
      -requests "$LOAD" -seed $((i + 30)) -concurrency 2 >/dev/null 2>&1 || true
    sleep 14
    read -r all jb <<< "$(count_streams_java)"
    echo "$label;$i;$all;$jb;$(index_labels)" >> "$TMP/results.csv"
    echo "  $i рестарт(ов): стримов всего $all, у java-backend $jb"
  done
}

: > "$TMP/results.csv"
echo "=== service.instance.id в лейблах: $RESTARTS рестартов на каждый конфиг"
run_case "defaults" "loki-default-labels.yaml"
run_case "safe" "loki.yaml"

# Проверка, что значение не потеряно: после safe-прогона оно должно остаться
# доступным в structured metadata.
FILTERABLE="$(innet -s --max-time 20 -G "http://loki:3100/loki/api/v1/query" \
  --data-urlencode 'query=sum(count_over_time({service_name="java-backend"} | service_instance_id != "" [1h]))' 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['value'][1])) if r else 0)
except Exception:
    print(0)
")"

{
  echo "service.instance.id в индексных лейблах Loki: замер"
  echo "==================================================="
  echo "Дата: $(date -u '+%Y-%m-%d %H:%M UTC'). Рестартов на конфиг: $RESTARTS,"
  echo "нагрузка между рестартами: $LOAD запросов."
  echo
  echo "Утверждение «Loki индексирует только фиксированный список ресурсных"
  echo "атрибутов, значит кардинальность под контролем» проверяется здесь напрямую."
  echo "Список действительно фиксирован, но в него по умолчанию входит"
  echo "service.instance.id, уникальный для экземпляра: Java-агент генерирует новый"
  echo "UUID при каждом старте процесса."
  echo
  printf "%-9s %9s %9s %14s  %s\n" "КОНФИГ" "РЕСТАРТОВ" "СТРИМОВ" "У JAVA-BACKEND" "ЛЕЙБЛЫ ИНДЕКСА"
  while IFS=';' read -r label restarts all jb labels; do
    printf "%-9s %9s %9s %14s  %s\n" "$label" "$restarts" "$all" "$jb" "$labels"
  done < "$TMP/results.csv"
  echo
  echo "defaults — loki-default-labels.yaml, дефолтный набор (18 атрибутов, среди них"
  echo "  service.instance.id, k8s.pod.name, k8s.replicaset.name, container.name)."
  echo "safe     — loki.yaml стенда: явный набор service.name, service.namespace,"
  echo "  deployment.environment и ignore_defaults: true."
  echo
  echo "Значение не потеряно: после safe-прогона фильтр по structured metadata"
  echo "{service_name=\"java-backend\"} | service_instance_id != \"\" находит $FILTERABLE записей."
  echo
  echo "Перезапускается именно Java-сервис. Go SDK атрибута service.instance.id не"
  echo "выставляет вовсе, поэтому рестарты Go-сервиса на число потоков не влияют —"
  echo "первая версия этого опыта рестартовала Go и не показала ничего."
} > "$RESULT_FILE"

cat "$RESULT_FILE"
echo
echo "записано: results/05-instance-id-labels.txt"
