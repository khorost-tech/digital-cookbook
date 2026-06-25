#!/usr/bin/env bash
set -euo pipefail
# Демонстрация НАТИВНОГО отложенного redelivery (RabbitMQ 4.3).
#
# Механизм: quorum-очередь с политикой delayed-retry-type=all, delayed-retry-min=2000ms,
# delayed-retry-max=10000ms. При implicit nack (закрытие AMQP-соединения без ack)
# брокер откладывает повторную доставку с нарастающей задержкой:
#   delay = min(min_delay × delivery_count, max_delay)
#   dc=0 → ~2s, dc=1 → ~4s, dc=2 → ~6s, ...
#
# ВАЖНО: нативный delayed retry срабатывает при IMPLICIT nack (закрытие соединения/канала
# без ack). Явный AMQP Basic.Nack(requeue=false) — это dead-lettering, а НЕ delayed retry.
#
# В отличие от TTL-retry-queue (retry-demo.sh) — дополнительные очереди не нужны;
# retry-логика полностью внутри одной quorum-очереди.
#
# Реализация: специальный Go-бинарник delayed-retry-demo-linux запускается внутри
# контейнера rabbit1 (localhost:5672 доступен изнутри). Компилируется автоматически.
#
# Использование:
#   bash scripts/delayed-retry-demo.sh          — интерактивный режим
#   AUTO=1 bash scripts/delayed-retry-demo.sh   — неинтерактивный (паузы пропускаются)
#   bash scripts/delayed-retry-demo.sh --auto   — то же, что AUTO=1

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

ts() {
  date '+%H:%M:%S'
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$ROOT_DIR/go"

echo "=== Демонстрация Native Delayed Retry (RabbitMQ 4.3) ==="
echo ""
echo "Механизм: политика delayed-retry-type=all, min=2000ms, max=10000ms"
echo "  delay = min(min_delay × delivery_count, max_delay)"
echo "  dc=0 → ~2s, dc=1 → ~4s, dc=2 → ~6s ..."
echo ""
echo "КЛЮЧЕВОЕ: implicit nack (закрытие соединения без ack) → delayed retry"
echo "          явный Nack(requeue=false) → dead-lettering (НЕ delayed retry)"
echo ""

# ── Шаг 1: компиляция Go delayed-retry-demo-linux ──
DEMO_BIN="$GO_DIR/delayed-retry-demo-linux"
if [ ! -f "$DEMO_BIN" ]; then
  echo "[$(ts)] Шаг 1 — компилируем delayed-retry-demo для Linux..."
  ( cd "$GO_DIR" && GOOS=linux GOARCH=amd64 go build -o delayed-retry-demo-linux ./cmd/delayed-retry-demo )
  echo "[$(ts)] OK."
else
  echo "[$(ts)] Шаг 1 — delayed-retry-demo-linux уже есть."
fi
echo ""

# ── Шаг 2: копируем бинарник в контейнер ──
echo "[$(ts)] Шаг 2 — копируем бинарник в rabbit1..."
# docker cp через stdin (cat + pipe) обходит проблему с путями в Git Bash на Windows.
# Это надёжно работает в любой среде: Linux, macOS, Windows (Git Bash, WSL).
cat "$DEMO_BIN" | docker exec -i rabbit1 bash -c 'cat > /tmp/delayed-retry-demo && chmod +x /tmp/delayed-retry-demo'
echo "[$(ts)] OK."
echo ""

# ── Шаг 3: применяем политику native delayed retry ──
echo "[$(ts)] Шаг 3 — применяем политику native-delayed-retry на demo.orders..."
docker exec rabbit1 rabbitmqctl set_policy native-delayed-retry '^demo\.orders$' \
  '{"delayed-retry-type":"all","delayed-retry-min":2000,"delayed-retry-max":10000}' \
  --apply-to queues --priority 15 2>&1
echo "[$(ts)] Политика применена."
docker exec rabbit1 rabbitmqctl list_policies --quiet 2>/dev/null | grep native-delayed-retry || true
echo ""

# ── Шаг 4: публикуем тестовое сообщение ──
echo "[$(ts)] Шаг 4 — очищаем demo.orders и публикуем одно сообщение..."
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  purge queue --name demo.orders 2>&1 || true

MSG="native-dr-$(date +%s)"
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  publish message \
    -k demo.orders \
    -m "$MSG" 2>&1
echo "[$(ts)] Опубликовано: $MSG"
docker exec rabbit1 rabbitmqctl list_queues name messages --quiet | grep demo.orders || true
echo ""

pause ">> Нажмите Enter, чтобы запустить демонстрацию нарастающей задержки (3 nack → 1 ack)"

# ── Шаг 5: запускаем демо-бинарник внутри контейнера ──
echo "[$(ts)] Шаг 5 — запускаем delayed-retry-demo (3 implicit nack, затем ack)..."
echo ""
echo "  Ожидаемые задержки между доставками:"
echo "    delivery #1 (dc=0) → implicit nack → ~2s → delivery #2"
echo "    delivery #2 (dc=1) → implicit nack → ~4s → delivery #3"
echo "    delivery #3 (dc=2) → implicit nack → ~6s → delivery #4"
echo "    delivery #4 (dc=3) → ack (успех)"
echo ""

# Тайм-аут: 3×nack с задержками + буфер = (2+4+6+8)*2 = 40s
# Используем //tmp// вместо /tmp/ для обхода преобразования путей в Git Bash на Windows.
DEMO_OUTPUT=$(docker exec rabbit1 bash -c \
  '//tmp//delayed-retry-demo -urls "amqp://demo:demo@localhost:5672/" -queue demo.orders -n 3' \
  2>&1) || DEMO_EXIT=$?

echo "$DEMO_OUTPUT"
echo ""

if echo "$DEMO_OUTPUT" | grep -q "SUCCESS"; then
  echo "[$(ts)] УСПЕХ: нарастающая задержка redelivery продемонстрирована."

  # Извлекаем задержки из вывода
  echo ""
  echo "Фактические задержки (из вывода выше):"
  echo "$DEMO_OUTPUT" | grep "redelivery delay=" || true
else
  echo "[$(ts)] ВНИМАНИЕ: демо не завершилось успехом. Проверьте вывод выше."
  echo "  Возможные причины:"
  echo "  - Сообщение не попало в очередь (routing issue)"
  echo "  - Политика не применена вовремя"
  echo "  - Задержка больше ожидаемой"
fi

echo ""
echo "=== Итог ==="
docker exec rabbit1 rabbitmqctl list_queues name messages --quiet | grep demo.orders || true
echo ""
echo "Паттерн продемонстрирован:"
echo "  1. quorum-очередь demo.orders с политикой native-delayed-retry"
echo "  2. Implicit nack (закрытие соединения без ack) → задержанный redelivery"
echo "  3. Задержка нарастает: delay = min(2000ms × dc, 10000ms)"
echo "  4. Дополнительные очереди (demo.dlx, demo.retry) — не нужны"
echo ""
echo "Для сравнения — TTL-подход (переносимый паттерн): scripts/retry-demo.sh"

# ── Очистка политики ──
echo ""
echo "[$(ts)] Очистка: удаляем политику native-delayed-retry..."
docker exec rabbit1 rabbitmqctl clear_policy native-delayed-retry 2>/dev/null || true
echo "[$(ts)] Готово."
