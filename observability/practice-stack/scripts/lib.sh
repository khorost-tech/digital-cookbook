#!/usr/bin/env bash
# Общие функции стенда. Подключается из остальных скриптов: source lib.sh
set -euo pipefail

STAND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_PROJECT="practice-stack"
NETWORK="${COMPOSE_PROJECT}_default"
CURL_IMAGE="curlimages/curl:latest"

# Интерпретатор Python для разбора JSON в проверках.
#
# Имя выбирается, а не берётся жёстко: в Git Bash на Windows команда называется
# `python`, а в обычном Linux (Ubuntu 24.04 в WSL) её нет вовсе — есть только
# `python3`. Скрипты, звавшие `python`, падали в WSL до того, как успевали
# что-либо проверить.
PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  if command -v python3 >/dev/null 2>&1; then
    PYTHON_BIN="python3"
  elif command -v python >/dev/null 2>&1; then
    PYTHON_BIN="python"
  fi
fi

# Preflight: без Python проверки бессмысленны, поэтому падаем сразу и внятно, а
# не посреди прогона с невнятной ошибкой интерпретатора.
require_tools() {
  local missing=0
  if [ -z "$PYTHON_BIN" ]; then
    echo "ОШИБКА: нужен python3 (или python) — им разбираются ответы бэкендов." >&2
    echo "  Ubuntu/Debian: sudo apt install python3" >&2
    missing=1
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "ОШИБКА: нужен docker." >&2
    missing=1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    echo "ОШИБКА: нужен docker compose (плагин v2)." >&2
    missing=1
  fi
  [ "$missing" = "0" ] || exit 1
}
require_tools

# Обращение к сервисам ТОЛЬКО изнутри docker-сети. Проброс портов Docker Desktop
# на машине сборки работает с перебоями: один и тот же запрос к Loki через хост
# давал 200, 200, таймаут (см. results/00-feasibility.txt). Порты на хост
# объявлены для человека, автоматике на них опираться нельзя.
innet() {
  docker run --rm --network "$NETWORK" "$CURL_IMAGE" "$@"
}

# curl внутри сети с шаблоном вывода кода ответа
http_code() {
  local url="$1" code
  # Код возвращается ОДИН раз: раньше при неуспехе curl печатал "000" и туда же
  # добавлялся echo "000", отчего в отчёте появлялось "000000".
  code="$(innet -s -o /dev/null -w "%{http_code}" --max-time 10 "$url" 2>/dev/null)" || code=""
  [ -n "$code" ] && echo "$code" || echo "000"
}

# Ждать готовности по эндпоинту. Критерий — /ready и health, а НЕ отсутствие
# слова error в логах: у Tempo, Loki, Grafana и PostgreSQL слово error есть в
# логах штатного старта (см. results/00-feasibility.txt, п. 5).
wait_ready() {
  local name="$1" url="$2" tries="${3:-40}" i=0 code reason
  while [ "$i" -lt "$tries" ]; do
    code="$(http_code "$url")"
    if [ "$code" = "200" ]; then
      echo "  готов: $name ($code)"
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  # Тело ответа при отказе печатается обязательно: у Pyroscope именно в нём
  # написано, ЧЕГО он ждёт («Segment Writer not ready: waiting for 30s after
  # being ready»). Без этой строки диагноз выглядит как «просто не поднялся», и
  # непонятно, ждать дальше или искать поломку.
  reason="$(innet -s --max-time 10 "$url" 2>/dev/null | head -1 | cut -c1-120)"
  echo "  НЕ ГОТОВ: $name — последний код $code после $tries попыток ($((tries * 2))с)" >&2
  [ -n "$reason" ] && echo "            причина: $reason" >&2
  return 1
}

# Число трейсов по запросу TraceQL.
#
# Окно времени обязательно: без start/end поиск Tempo возвращает пусто даже при
# доехавших трейсах — это легко принять за потерю данных.
#
# Лимит заведомо больше любого прогона стенда. Со значением 200 подсчёт молча
# обрезался ровно на 200, и прогон из 1000 запросов выглядел как «все трейсы на
# месте»: число совпадало с ожиданием только потому, что упиралось в лимит.
tempo_search_count() {
  local traceql="$1" now start end
  now="$(date +%s)"
  start=$((now - 900))
  end=$((now + 120))
  innet -s --max-time 25 -G "http://tempo:3200/api/search" \
    --data-urlencode "q=$traceql" \
    --data-urlencode "start=$start" --data-urlencode "end=$end" \
    --data-urlencode "limit=5000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(len(json.load(sys.stdin).get('traces') or []))
except Exception:
    print(0)
"
}

# Валидация ИМЕННО ТЕХ конфигов, которые сейчас поедут в контейнеры. Вызывается
# ПОСЛЕ выбора режима — иначе смысл теряется.
#
# Раньше проверка стояла до выбора и всегда смотрела на config.yaml. В режиме
# tail стек затем поднимался с config-tail-sampling.yaml, а замер кардинальности
# подменяет ещё и конфиг Loki: проверялся один файл, а работал другой. Теперь
# имена читаются из .env, который к этому моменту уже записан.
validate_selected_configs() {
  local otelcol_cfg loki_cfg
  # `|| true` обязателен: если строки в .env нет (обычный режим её не пишет),
  # grep возвращает 1, и при set -e скрипт падает МОЛЧА — подъём обрывается на
  # ровном месте. Поймано на себе сразу после добавления этой проверки.
  otelcol_cfg="$(grep -E '^OTELCOL_CONFIG=' "$STAND_DIR/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
  loki_cfg="$(grep -E '^LOKI_CONFIG=' "$STAND_DIR/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
  otelcol_cfg="${otelcol_cfg:-config.yaml}"
  loki_cfg="${loki_cfg:-loki.yaml}"

  log "валидация выбранных конфигов: otelcol=$otelcol_cfg, loki=$loki_cfg"
  MSYS_NO_PATHCONV=1 docker run --rm -v "$STAND_DIR/otelcol/$otelcol_cfg:/c.yaml:ro" \
    otel/opentelemetry-collector-contrib:0.158.0 validate --config=/c.yaml
  MSYS_NO_PATHCONV=1 docker run --rm -v "$STAND_DIR/loki/$loki_cfg:/etc/loki/loki.yaml:ro" \
    grafana/loki:3.7.5 -config.file=/etc/loki/loki.yaml -verify-config
  MSYS_NO_PATHCONV=1 docker run --rm --entrypoint promtool -v "$STAND_DIR/prometheus/prometheus.yml:/p.yml:ro" \
    prom/prometheus:v3.13.2 check config /p.yml >/dev/null
  MSYS_NO_PATHCONV=1 docker run --rm --entrypoint /tempo -v "$STAND_DIR/tempo/tempo.yaml:/t.yaml:ro" \
    grafana/tempo:3.0.2 -config.file=/t.yaml -config.verify=true
  echo "  конфиги приняты"
}

log() { echo "== $*"; }
