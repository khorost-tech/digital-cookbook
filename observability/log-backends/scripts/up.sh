#!/usr/bin/env bash
# Поднять стенд и дождаться готовности трёх бэкендов.
#
#   ./scripts/up.sh              # конфигурации по умолчанию (первый прогон)
#   ./scripts/up.sh highcard     # уникальное поле в метке и потоке (замер кардинальности)
#   ./scripts/up.sh isolated     # то же + политика буфера у Loki (замер изоляции)
#   ./scripts/up.sh tuned        # с настройкой каждого бэкенда (второй прогон)
#   ./scripts/up.sh default ui   # плюс Grafana для человека
#
# Режим пишется в .env, а не экспортируется: Git Bash переписывает значения,
# похожие на POSIX-путь, а .env docker compose читает сам, без конверсии.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

MODE="${1:-default}"
UI="${2:-}"

case "$MODE" in
  default)
    cat > .env <<ENVEOF
# Сгенерирован scripts/up.sh, режим: default
VECTOR_CONFIG=agent.yaml
LOKI_CONFIG=loki.yaml
ENVEOF
    ;;
  highcard)
    # Уникальное поле объявлено меткой Loki и частью потока VictoriaLogs.
    # Конфигурация намеренно неправильная, нужна замеру кардинальности.
    cat > .env <<ENVEOF
# Сгенерирован scripts/up.sh, режим: highcard
VECTOR_CONFIG=agent-highcard.yaml
LOKI_CONFIG=loki.yaml
ENVEOF
    ;;
  isolated)
    # Та же намеренная ошибка с меткой, что в highcard, но у синка Loki явная
    # политика буфера when_full: drop_newest. Нужен замеру изоляции отказа.
    cat > .env <<ENVEOF
# Сгенерирован scripts/up.sh, режим: isolated
VECTOR_CONFIG=agent-isolated.yaml
LOKI_CONFIG=loki.yaml
ENVEOF
    ;;
  tuned)
    cat > .env <<ENVEOF
# Сгенерирован scripts/up.sh, режим: tuned
VECTOR_CONFIG=agent-tuned.yaml
LOKI_CONFIG=loki-tuned.yaml
ENVEOF
    ;;
  *)
    echo "неизвестный режим: $MODE (ожидается default, highcard, isolated или tuned)" >&2
    exit 2
    ;;
esac

PROFILES=()
[ "$UI" = "ui" ] && PROFILES=(--profile ui)

log "подъём (режим: $MODE)"
docker compose "${PROFILES[@]}" up -d

log "ожидание готовности"
ready() {
  local name="$1" url="$2" code
  for _ in $(seq 1 60); do
    code="$(innet -s -o /dev/null -w '%{http_code}' --max-time 5 "$url" 2>/dev/null || true)"
    if [ "$code" = "200" ]; then
      echo "  готов: $name ($code)"
      return 0
    fi
    sleep 3
  done
  echo "  НЕ ГОТОВ: $name (последний код: ${code:-нет ответа})" >&2
  return 1
}

ready loki         "http://loki:3100/ready"
ready opensearch   "http://opensearch:9200/_cluster/health"
ready victorialogs "http://victorialogs:9428/health"

log "готово. Порты: Loki 9401, OpenSearch 9402, VictoriaLogs 9403, Vector API 9406"
[ "$UI" = "ui" ] && echo "     Grafana для человека: http://127.0.0.1:9405"
exit 0
