#!/usr/bin/env bash
# Замер последствий высокой кардинальности в лейблах Loki.
#
#   ./scripts/bench-cardinality.sh [запросов]
#
# Правило «trace_id в лейбл класть нельзя» есть в любой документации. Здесь оно
# ПРОВЕРЯЕТСЯ: два одинаковых прогона, разница только в том, уезжает ли trace_id
# в лейбл, и сравниваются число стримов, память Loki и время запроса.
#
# Стенд между прогонами поднимается заново с чистыми томами: остаточные стримы
# прошлого прогона исказили бы обе величины.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

REQUESTS="${1:-600}"
# Свой файл, а не общий: раньше скрипт писал в 04-logs-correlation.txt через `>`
# и стирал разделы, которые туда дописывают correlation-probe.sh и ручные опыты.
RESULT_FILE="$STAND_DIR/results/06-cardinality.txt"
TMP="$STAND_DIR/results/.cardinality"
rm -rf "$TMP"; mkdir -p "$TMP"

loki_metric() {
  local metric="$1"
  innet -s --max-time 15 "http://loki:3100/metrics" 2>/dev/null |
    awk -v m="$metric" '$1 ~ "^"m"($|{)" { sum += $2 } END { printf "%.0f", sum+0 }'
}

loki_mem_mib() {
  docker stats --no-stream --format "{{.MemUsage}}" ops-loki 2>/dev/null |
    awk '{ split($0, a, " / "); v=a[1]; u=v;
           gsub(/[A-Za-z]+/,"",v); gsub(/[0-9.]+/,"",u);
           if (u=="GiB") v=v*1024; printf "%.1f", v+0 }'
}

# Число потоков (stream) — то, что и раздувается кардинальностью. Берём из
# метрики самого Loki, а не из догадок.
count_streams() { loki_metric "loki_ingester_memory_streams"; }

# Время запроса по логам: МЕДИАНА из пяти замеров, а не один вызов.
#
# Один замер здесь бесполезен: на объёмах стенда разброс перекрывает эффект. Три
# последовательных прогона дали 51/79, 64/48 и 31/62 мс — то есть в одном из них
# вариант с раздутым индексом оказался БЫСТРЕЕ обычного. Медиана стабильнее, но и
# она не делает эту величину доказательной: см. оговорку в отчёте.
query_time_ms() {
  local query="$1" i t times=""
  for i in 1 2 3 4 5; do
    t="$(innet -s -o /dev/null -w "%{time_total}" --max-time 60 -G \
      "http://loki:3100/loki/api/v1/query_range" \
      --data-urlencode "query=$query" --data-urlencode "limit=100" 2>/dev/null |
      awk '{ printf "%.0f", $1 * 1000 }')"
    times="$times $t"
  done
  echo "$times" | tr ' ' '\n' | grep -E '^[0-9]+$' | sort -n |
    awk '{ v[NR] = $1 } END { print (NR ? v[int((NR + 1) / 2)] : 0) }'
}

# ЧЕГО ЗДЕСЬ НЕТ И ПОЧЕМУ.
#
# Была попытка добавить качественный критерий: проходит ли агрегирующий запрос
#   count(count by (service_instance_id) (count_over_time({service_name=~".+"}[24h])))
# При раздутом индексе Loki отвечал «maximum number of series (500) reached», и
# это выглядело как идеальное последствие кардинальности — не «медленнее на N мс»,
# а «запрос перестал работать».
#
# Проверка на чистом стенде эту версию убила: тот же запрос упирается в лимит и
# при ДВУХ потоках, и даже при группировке по service_name — то есть дело в окне
# [24h] и объёме записей, а не в числе лейблов. За [5m] тот же запрос проходит.
# Критерий измерял стоимость самой агрегации, а не кардинальность индекса, и
# поэтому убран.
#
# Кардинальность лейблов правильно смотреть не агрегациями, а напрямую:
#   /loki/api/v1/series?match[]={...}         — сколько потоков
#   /loki/api/v1/label/<name>/values          — сколько значений у лейбла
# Первое здесь и используется, второе — в bench-instance-id.sh.

run_case() {
  local label="$1" otelcol_config="$2" loki_config="$3"

  log "прогон «$label»: Collector $otelcol_config, Loki $loki_config"
  ./scripts/down.sh clean >/dev/null 2>&1
  ./scripts/up.sh all otel >/dev/null 2>&1
  # Конфиги подменяются после подъёма: остальная часть стенда поднимается
  # одинаково в обоих случаях.
  #
  # Подменять надо ОБА. Одного OTTL-выражения в Collector недостаточно:
  # trace_id доедет в атрибуте ресурса, но Loki по умолчанию индексирует только
  # фиксированный белый список ресурсных атрибутов, а остальное складывает в
  # structured metadata. Первая редакция замера этого не учитывала и показала
  # «2 стрима против 2» — то есть никакого эффекта, хотя ломала честно.
  {
    echo "OTELCOL_CONFIG=$otelcol_config"
    echo "LOKI_CONFIG=$loki_config"
  } >> "$STAND_DIR/.env"

  # Валидация ПОСЛЕ дописывания .env — иначе этот замер обходил бы проверку,
  # которую up.sh делает для обычных конфигов: up.sh валидирует свой набор, а
  # затем сюда подставляются другие файлы. При ошибке в них пользователь получил
  # бы ранний выход с подавленным выводом docker вместо внятной диагностики.
  # Функция общая, живёт в lib.sh.
  validate_selected_configs

  docker compose up -d --force-recreate otelcol loki >/dev/null 2>&1
  wait_ready "otelcol" "http://otelcol:13133" >/dev/null
  wait_ready "loki" "http://loki:3100/ready" >/dev/null

# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$REQUESTS" -seed 42 -concurrency 6 -run "card-$label" >/dev/null 2>&1 || true

  # Ждём, пока логи осядут: batch у Collector 1 с, плюс запись в Loki.
  sleep 20

  local streams mem qtime labels
  streams="$(count_streams)"
  mem="$(loki_mem_mib)"
  qtime="$(query_time_ms '{service_name="go-frontend"} |= `заказ создан`')"
  labels="$(innet -s --max-time 15 "http://loki:3100/loki/api/v1/labels" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)['data']
    print(len(d), ','.join(sorted(d)))
except Exception:
    print('0 -')
")"

  echo "$label;$streams;$mem;$qtime;$labels" >> "$TMP/results.csv"
  echo "  стримов: $streams, память Loki: $mem МиБ, запрос: $qtime мс"
  echo "  лейблов индекса: $labels"
}

: > "$TMP/results.csv"
echo "=== Замер кардинальности: $REQUESTS запросов на каждый прогон"
run_case "normal" "config.yaml" "loki.yaml"
run_case "highcard" "config-high-cardinality.yaml" "loki-highcard.yaml"

{
  echo "Кардинальность лейблов Loki: замер последствий"
  echo "=============================================="
  echo "Дата: $(date -u '+%Y-%m-%d %H:%M UTC'). Нагрузка: $REQUESTS запросов, seed 42."
  echo "Оба прогона одинаковы во всём, кроме двух настроек: во втором Collector"
  echo "перекладывает trace_id из записи в атрибут ресурса (OTTL), а Loki получает"
  echo "конфиг, где этот атрибут объявлен лейблом индекса. Обе правки обязательны:"
  echo "одной первой недостаточно, потому что Loki индексирует не любой присланный"
  echo "ресурсный атрибут, а только перечисленные в otlp_config."
  echo
  echo "ВАЖНО про базовую линию. В обоих прогонах индексных лейблов задан ЯВНЫЙ"
  echo "безопасный набор (service.name, service.namespace, deployment.environment,"
  echo "ignore_defaults: true). Дефолтный список Loki для этой роли НЕ годится: в"
  echo "него входит service.instance.id, который меняется при каждом перезапуске"
  echo "процесса — см. отдельный опыт с рестартами ниже. Так что список ограничивает"
  echo "набор атрибутов, но сам по себе низкой кардинальности не гарантирует."
  echo "Стенд между прогонами поднят с чистыми томами."
  echo
  printf "%-10s %10s %14s %12s %s\n" "ПРОГОН" "СТРИМОВ" "ОЗУ LOKI, МиБ" "ЗАПРОС, мс" "ЛЕЙБЛЫ ИНДЕКСА"
  while IFS=';' read -r label streams mem qtime labels; do
    printf "%-10s %10s %14s %12s %s\n" "$label" "$streams" "$mem" "$qtime" "$labels"
  done < "$TMP/results.csv"
  echo
  echo "Стримы считаны из метрики самого Loki (loki_ingester_memory_streams),"
  echo "память — снимком docker stats --no-stream, время запроса — медиана пяти"
  echo "вызовов /loki/api/v1/query_range на одном и том же LogQL."
  echo
  echo "ЧТО ЗДЕСЬ ДОКАЗАТЕЛЬНО, А ЧТО НЕТ"
  echo "Устойчиво воспроизводится ОДНО: число потоков, 2 против 1202 в каждом прогоне."
  echo "Память и время запроса на таком объёме — шум. Три прогона подряд дали по"
  echo "времени 51/79, 64/48 и 31/62 мс: в одном из них вариант с раздутым индексом"
  echo "оказался БЫСТРЕЕ обычного. Базовая линия по памяти гуляла 80,0-86,8 МиБ, что"
  echo "шире разницы с highcard. Поэтому вывод делается только по числу потоков, а"
  echo "остальные колонки приводятся как есть — чтобы был виден разброс, а не"
  echo "подогнанная под ожидание картинка."
} > "$RESULT_FILE"

cat "$RESULT_FILE"
echo
echo "записано: results/04-logs-correlation.txt"
