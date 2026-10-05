#!/usr/bin/env bash
# Поднять стенд и дождаться готовности всех компонентов.
#
# Использование:
#   ./scripts/up.sh                    # только стек наблюдаемости и инфраструктура
#   ./scripts/up.sh all               # плюс приложения, телеметрия включена
#   ./scripts/up.sh all off           # плюс приложения БЕЗ телеметрии (базовая линия)
#   ./scripts/up.sh all http          # телеметрия Go по OTLP/HTTP вместо gRPC
#   ./scripts/up.sh all otel http     # + НАМЕРЕННЫЙ разрыв трейса на HTTP-границе
#   ./scripts/up.sh all otel nats     # + НАМЕРЕННЫЙ разрыв на асинхронной границе
#   ./scripts/up.sh all otel logctx   # + логи без контекста запроса (пустой trace_id)
#   ./scripts/up.sh all tail          # телеметрия + tail sampling в Collector
#   ./scripts/up.sh all spanmetrics   # телеметрия + span-метрики и exemplars (ст. 7)
#   ./scripts/up.sh all cardinality      # + взрыв кардинальности метрики (ст. 8)
#   ./scripts/up.sh all cardinality-cut  # + тот же взрыв, но лейбл режется в Collector
#   ./scripts/up.sh all otel none go   # + профилирование Go
#   ./scripts/up.sh all otel none both # + профилирование Go и Java
#
# Третий аргумент — воспроизводимая поломка для опытов статей 2 и 3. Поломки
# задаются переменными, а не правкой кода: опыт должен повторяться одной
# командой, иначе его никто не повторит.
#
# Режим телеметрии задаётся ПЕРЕМЕННЫМИ, а не отдельными образами: иначе замер
# накладных расходов сравнивал бы разные сборки, а не одну и ту же под разными
# настройками.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

MODE="${1:-stack}"
TELEMETRY="${2:-otel}"
BREAK="${3:-none}"
# Профилирование — четвёртый аргумент: off (по умолчанию), go, both. Отдельно от
# телеметрии потому, что у него отдельная цена, и замер сравнивает именно её.
PROFILING="${4:-off}"

# Режим пишется в .env, а НЕ экспортируется переменными окружения. Причина
# платформенная: Git Bash (MSYS) переписывает значения, похожие на POSIX-путь,
# в путь Windows. Экспортированное -javaagent:/app/opentelemetry-javaagent.jar
# доезжало до JVM как -javaagent:C:/Program Files/Git/app/opentelemetry-javaagent.jar,
# и контейнер падал с "Unrecognized option: Files/Git/app/...".
# Файл .env docker compose читает сам, никакой конверсии на пути нет.
write_env() {
  # Поломки: каждая выключает ровно одну вещь, чтобы следствие было однозначным.
  local plain_http=false plain_nats=false log_noctx=false
  case "$BREAK" in
    none) ;;
    http)   plain_http=true ;;   # HTTP-клиент без пропагатора: трейс рвётся на границе сервисов
    nats)   plain_nats=true ;;   # публикация без заголовков: рвётся асинхронная ветка
    logctx) log_noctx=true ;;    # логи без контекста запроса: trace_id пуст
    *) echo "неизвестная поломка: $BREAK (ожидается none, http, nats или logctx)" >&2; exit 2 ;;
  esac

  # Профилирование. Java-агент добавляется в ту же переменную, что и агент OTel:
  # два javaagent в одной JVM сосуществуют нормально, точки инструментирования у
  # них разные.
  local go_prof=false java_agent="$1"
  case "$PROFILING" in
    off) ;;
    go)  go_prof=true ;;
    both)
      go_prof=true
      if [ -z "$java_agent" ]; then
        # Без агента OTel профилирование Java всё равно возможно: агенты
        # независимы. Но тогда трейсов не будет, и связка «из спана в профиль»
        # проверяться не сможет — об этом стоит сказать вслух, а не молча.
        echo "ВНИМАНИЕ: профилирование Java без агента OTel — связку спан/профиль не проверить" >&2
        java_agent="-javaagent:/app/pyroscope.jar"
      else
        java_agent="$java_agent -javaagent:/app/pyroscope.jar"
      fi
      ;;
    *) echo "неизвестный режим профилирования: $PROFILING (ожидается off, go или both)" >&2; exit 2 ;;
  esac

  cat > "$STAND_DIR/.env" <<ENVEOF
# Сгенерирован scripts/up.sh, телеметрия: $TELEMETRY, поломка: $BREAK, профили: $PROFILING
JAVA_BACKEND_JAVA_TOOL_OPTIONS=$java_agent
OTEL_SDK_DISABLED=$2
OTEL_GRPC_ENDPOINT=$3
OTEL_HTTP_ENDPOINT=$4
GO_OTLP_PROTOCOL=$5
PLAIN_HTTP_CLIENT=$plain_http
PLAIN_NATS_PUBLISH=$plain_nats
LOG_WITHOUT_CONTEXT=$log_noctx
PROFILING_ENABLED=$go_prof
ENVEOF
}

case "$TELEMETRY" in
  otel)
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false" \
      "http://otelcol:4317" "http://otelcol:4318" "grpc"
    ;;
  http)
    # Go SDK по HTTP вместо gRPC: у экспортёров разные модули, но переменные
    # общие, протокол переключается OTEL_EXPORTER_OTLP_PROTOCOL.
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false" \
      "http://otelcol:4318" "http://otelcol:4318" "http/protobuf"
    ;;
  cardinality)
    # Опыт статьи 8: приложение вешает уникальный order_id в ЛЕЙБЛ метрики.
    # Верхняя граница числа значений задаётся CARDINALITY_BOMB_MAX (по умолчанию
    # 500): без границы серии множатся до исчерпания памяти Prometheus, и замер
    # не доживает до результата.
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false"       "http://otelcol:4317" "http://otelcol:4318" "grpc"
    echo "CARDINALITY_BOMB_MAX=${CARDINALITY_BOMB_MAX:-500}" >> "$STAND_DIR/.env"
    ;;
  cardinality-cut)
    # Тот же взрыв, но лейбл срезается в Collector. Приложение при этом НЕ
    # меняется: в жизни лишний лейбл обычно приходит из чужого кода, и правка
    # приложения — не всегда доступный вариант.
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false"       "http://otelcol:4317" "http://otelcol:4318" "grpc"
    echo "CARDINALITY_BOMB_MAX=${CARDINALITY_BOMB_MAX:-500}" >> "$STAND_DIR/.env"
    echo "OTELCOL_CONFIG=config-cardinality.yaml" >> "$STAND_DIR/.env"
    ;;
  spanmetrics)
    # Span-метрики: RED считается коннектором из спанов. Приложения при этом
    # сэмплируют ВСЁ — иначе метрики окажутся занижены ещё до опыта с сэмплингом,
    # и разобраться, что именно их занизило, будет нельзя.
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false"       "http://otelcol:4317" "http://otelcol:4318" "grpc"
    echo "OTELCOL_CONFIG=config-spanmetrics.yaml" >> "$STAND_DIR/.env"
    # Второй генератор span-метрик — в самом Tempo. Оба пути поднимаются
    # одновременно намеренно: сравнивать их числа можно только на одном
    # и том же потоке спанов, иначе разница будет разницей нагрузки.
    echo "TEMPO_CONFIG=tempo-metrics-generator.yaml" >> "$STAND_DIR/.env"
    ;;
  tail)
    # Tail sampling: решение принимает Collector после сбора трейса, поэтому
    # приложения должны сэмплировать ВСЁ — иначе до Collector дойдёт уже
    # прореженный поток и смысл политик потеряется.
    write_env "-javaagent:/app/opentelemetry-javaagent.jar" "false"       "http://otelcol:4317" "http://otelcol:4318" "grpc"
    echo "OTELCOL_CONFIG=config-tail-sampling.yaml" >> "$STAND_DIR/.env"
    ;;
  off)
    # Базовая линия: у Go — штатная OTEL_SDK_DISABLED, у Java — просто нет
    # -javaagent. Приложения при этом обязаны работать полностью.
    write_env "" "true" "http://otelcol:4317" "http://otelcol:4318" "grpc"
    ;;
  *)
    echo "неизвестный режим телеметрии: $TELEMETRY (ожидается otel, http, tail, spanmetrics, cardinality, cardinality-cut или off)" >&2
    exit 2
    ;;
esac

validate_selected_configs

# Pyroscope живёт в профиле compose и поднимается ТОЛЬКО когда профилирование
# включено: статьям 4 и 5 он не нужен, а готовность у него занимает минуты.
COMPOSE_PROFILE_ARGS=()
if [ "$PROFILING" != "off" ]; then
  COMPOSE_PROFILE_ARGS=(--profile profiling)
fi

log "подъём (состав: $MODE, телеметрия: $TELEMETRY, поломка: $BREAK, профили: $PROFILING)"
if [ "$MODE" = "all" ]; then
  docker compose "${COMPOSE_PROFILE_ARGS[@]}" up -d
  # Пересоздание приложений обязательно: без него смена переменных режима молча
  # не применяется к уже запущенным контейнерам, и замер сравнивает одно и то же.
  docker compose up -d --force-recreate go-frontend java-backend
  # Collector пересоздаётся ТОЖЕ, и по отдельной причине. Конфиг приезжает в
  # контейнер bind-mount'ом, а читается только при старте: `docker compose up`
  # видит неизменившееся определение сервиса и оставляет контейнер как есть.
  # Правка YAML при этом молча НЕ применяется.
  #
  # Поймано на опыте с кардинальностью: конфиг был исправлен, замер показал
  # прежнее поведение, и вывод «резка не работает» уже готовился в отчёт —
  # пока в логах Collector не обнаружился процессор из старой редакции.
  docker compose up -d --force-recreate otelcol
else
  docker compose "${COMPOSE_PROFILE_ARGS[@]}" up -d otelcol tempo loki prometheus grafana postgres nats
fi

log "ожидание готовности"
wait_ready "tempo"      "http://tempo:3200/ready"
wait_ready "loki"       "http://loki:3100/ready"
wait_ready "prometheus" "http://prometheus:9090/-/ready"
wait_ready "grafana"    "http://grafana:3000/api/health"
wait_ready "otelcol"    "http://otelcol:13133"
wait_ready "nats"       "http://nats:8222/healthz"
# Pyroscope ждём ТОЛЬКО когда он поднят, и с большим запасом.
#
# Готовность у него — три последовательные фазы, каждая со своим ожиданием
# (замерено по телу ответа /ready): metastore 15 с, ingester 15 с после
# готовности, segment-writer 30 с после готовности. Минимум минута чистого
# ожидания, на этой машине фактически около четырёх минут после старта
# контейнера. 60 попыток по 2 с (120 с) НЕ ХВАТАЛО — подъём падал по таймауту,
# хотя Pyroscope становился ready чуть позже. Теперь 180 попыток (360 с), а
# wait_ready при отказе печатает тело ответа с причиной.
if [ "$PROFILING" != "off" ]; then
  wait_ready "pyroscope"  "http://pyroscope:4040/ready" 180
fi
wait_ready "alertmanager" "http://alertmanager:9093/-/ready"
wait_ready "alert-webhook" "http://alert-webhook:9099/health"
if [ "$MODE" = "all" ]; then
  wait_ready "go-frontend"  "http://go-frontend:8080/health"
  wait_ready "java-backend" "http://java-backend:8080/inventory/SKU-0001"
fi

log "готово. Grafana для человека: http://127.0.0.1:9305"
