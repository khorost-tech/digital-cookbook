#!/usr/bin/env bash
set -euo pipefail
# Сквозная проверка всего demo-стенда (неинтерактивно):
#   1. docker compose up -d
#   2. Ожидание healthcheck (~45 сек)
#   3. cluster_status (проверить 3 ноды)
#   4. Go producer (-n 20) + consumer (с ограничением по времени)
#   5. retry-demo.sh (AUTO)
#   6. delayed-retry-demo.sh (AUTO)
#   7. docker compose down -v
#
# Использование:
#   bash scripts/smoke.sh

export AUTO=1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

ts() {
  date '+%H:%M:%S'
}

fail() {
  echo "[$(ts)] ОШИБКА: $1" >&2
  echo "[$(ts)] Останавливаем стенд..." >&2
  docker compose -f "$ROOT_DIR/docker-compose.yml" down -v 2>/dev/null || true
  exit 1
}

echo "============================================================"
echo " Smoke test: demo-стенд RabbitMQ HA-кластер"
echo " Время старта: $(ts)"
echo "============================================================"
echo ""

# ── 1. Поднимаем кластер ──
echo "[$(ts)] Шаг 1 — docker compose up -d..."
docker compose -f "$ROOT_DIR/docker-compose.yml" up -d
echo ""

# ── 2. Ждём healthcheck ──
echo "[$(ts)] Шаг 2 — ожидаем healthcheck (до 90 сек)..."
ELAPSED=0
TIMEOUT=90
while true; do
  ALL_HEALTHY=true
  for node in rabbit1 rabbit2 rabbit3; do
    STATUS=$(docker inspect --format='{{.State.Health.Status}}' "$node" 2>/dev/null || echo "missing")
    if [ "$STATUS" != "healthy" ]; then
      ALL_HEALTHY=false
      break
    fi
  done
  if $ALL_HEALTHY; then
    echo "[$(ts)] Все ноды healthy (прошло ${ELAPSED}с)"
    break
  fi
  if [ $ELAPSED -ge $TIMEOUT ]; then
    fail "Ноды не стали healthy за ${TIMEOUT}с. Статус: $(docker inspect --format='{{.Name}} {{.State.Health.Status}}' rabbit1 rabbit2 rabbit3)"
  fi
  sleep 5
  ELAPSED=$((ELAPSED + 5))
  echo "[$(ts)]   ... ждём (${ELAPSED}/${TIMEOUT}с)"
done
echo ""

# ── 3. Проверяем cluster_status ──
echo "[$(ts)] Шаг 3 — cluster_status (ожидаем 3 ноды)..."
CLUSTER_OUT=$(docker exec rabbit1 rabbitmqctl cluster_status 2>&1)
echo "$CLUSTER_OUT"
echo ""

for node in rabbit@rabbit1 rabbit@rabbit2 rabbit@rabbit3; do
  if ! echo "$CLUSTER_OUT" | grep -q "$node"; then
    fail "Нода $node не найдена в cluster_status"
  fi
done
echo "[$(ts)] Кластер OK: все 3 ноды в строю."
echo ""

# ── 4. Быстрый прогон producer + consumer ──
echo "[$(ts)] Шаг 4 — Go build + producer/consumer через Linux-бинарники внутри контейнера..."
cd "$ROOT_DIR/go"
go build ./... || fail "go build (Windows) завершился с ошибкой"
echo "[$(ts)] go build OK"

# Компилируем linux-бинарники и запускаем внутри rabbit1
# (AMQP соединение localhost:5672 работает изнутри контейнера)
echo "[$(ts)]   Компилируем producer и consumer для Linux..."
GOOS=linux GOARCH=amd64 go build -o producer-linux ./cmd/producer \
  && GOOS=linux GOARCH=amd64 go build -o consumer-linux ./cmd/consumer \
  || fail "Linux build завершился с ошибкой"

# Копируем в контейнер через stdin pipe (обходит проблему с путями в Git Bash)
cat "$ROOT_DIR/go/producer-linux" | docker exec -i rabbit1 bash -c 'cat > /tmp/producer && chmod +x /tmp/producer'
cat "$ROOT_DIR/go/consumer-linux" | docker exec -i rabbit1 bash -c 'cat > /tmp/consumer && chmod +x /tmp/consumer'
echo "[$(ts)] Бинарники скопированы в rabbit1."

# Запускаем consumer в фоне внутри rabbit1; сохраняем PID через pid-файл
echo "[$(ts)]   Запускаем consumer внутри rabbit1 (autoack=true)..."
docker exec rabbit1 bash -c '//tmp//consumer -autoack=true -urls "amqp://demo:demo@localhost:5672/" > /tmp/consumer.log 2>&1 & echo $! > /tmp/consumer.pid'
sleep 2

# Запускаем producer внутри rabbit1
echo "[$(ts)]   Запускаем producer -n 20 внутри rabbit1..."
docker exec rabbit1 bash -c '//tmp//producer -n 20 -urls "amqp://demo:demo@localhost:5672/"' 2>&1
echo "[$(ts)]   Producer завершён."

# Убиваем consumer по сохранённому PID
sleep 3
docker exec rabbit1 bash -c 'if [ -f /tmp/consumer.pid ]; then kill $(cat /tmp/consumer.pid) 2>/dev/null || true; rm -f /tmp/consumer.pid; fi'
echo "[$(ts)] Consumer logs:"
docker exec rabbit1 bash -c 'tail -5 /tmp/consumer.log 2>/dev/null || true'
echo "[$(ts)] Producer + Consumer OK"
echo ""
cd "$ROOT_DIR"

# ── 5. retry-demo.sh ──
echo "[$(ts)] Шаг 5 — AUTO=1 bash scripts/retry-demo.sh..."
AUTO=1 bash "$SCRIPT_DIR/retry-demo.sh"
echo ""
echo "[$(ts)] retry-demo.sh OK"
echo ""

# ── 6. delayed-retry-demo.sh ──
echo "[$(ts)] Шаг 6 — AUTO=1 bash scripts/delayed-retry-demo.sh..."
AUTO=1 bash "$SCRIPT_DIR/delayed-retry-demo.sh"
echo ""
echo "[$(ts)] delayed-retry-demo.sh OK"
echo ""

# ── 7. Останавливаем и чистим ──
echo "[$(ts)] Шаг 7 — docker compose down -v..."
docker compose -f "$ROOT_DIR/docker-compose.yml" down -v
echo ""

echo "============================================================"
echo " Smoke test ПРОЙДЕН"
echo " Время финиша: $(ts)"
echo "============================================================"
