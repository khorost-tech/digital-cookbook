#!/usr/bin/env bash
set -euo pipefail

# Cross-cluster Federation Demo
# =====================================================================
# Топология:
#   UPSTREAM:   основной кластер (rabbit1/rabbit2/rabbit3) — порты 5672/15672
#   DOWNSTREAM: rabbit-b (отдельный одиночный кластер)    — порты 5681/15681
#
# rabbit-b подключён к сети rabbitnet (общей с rabbit1), поэтому может
# достучаться до rabbit1 по AMQP по имени контейнера.
# URI: amqp://demo:demo@rabbit1:5672/%2F  (%2F = url-encoded vhost "/")
#
# Что демонстрирует:
#   1. На rabbit-b объявляем exchange demo.fed + очередь demo.fed.q
#   2. Задаём federation-upstream, указывающий на rabbit1 (другой кластер!)
#   3. Применяем policy federation на rabbit-b
#   4. Публикуем сообщение в UPSTREAM (rabbit1) в exchange demo.fed
#   5. Показываем, что сообщение РЕАЛЬНО появилось на DOWNSTREAM (rabbit-b)
#   6. Статус federation-link на rabbit-b — running
#
# Использование:
#   bash scripts/federation-demo.sh          — развернуть демо
#   bash scripts/federation-demo.sh clean    — удалить все артефакты
#   AUTO=1 bash scripts/federation-demo.sh   — неинтерактивный режим
#   bash scripts/federation-demo.sh --auto   — то же, что AUTO=1
# =====================================================================

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
  shift
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

# Команды для UPSTREAM (rabbit1) и DOWNSTREAM (rabbit-b)
RABBIT_UP="docker exec rabbit1"
RABBIT_DOWN="docker exec rabbit-b"

# rabbitmqadmin v2: --username/--password флаги (не позиционные аргументы)
ADMIN_UP="$RABBIT_UP rabbitmqadmin --username demo --password demo"
ADMIN_DOWN="$RABBIT_DOWN rabbitmqadmin --username demo --password demo"

CTL_UP="$RABBIT_UP rabbitmqctl"
CTL_DOWN="$RABBIT_DOWN rabbitmqctl"

# vhost "/" url-encoded для AMQP URI (обязательно для RabbitMQ 4.x federation)
UPSTREAM_URI='amqp://demo:demo@rabbit1:5672/%2F'

# ──────────────────────────────────────────────────────────────
# Режим очистки
# ──────────────────────────────────────────────────────────────
if [[ "${1:-}" == "clean" ]]; then
  echo "=== [federation-демо] Очистка артефактов ==="

  echo "--- Удаляем на DOWNSTREAM (rabbit-b)..."
  $CTL_DOWN clear_policy federate-fed-b || true
  $CTL_DOWN clear_parameter federation-upstream upstream-rabbit1 || true
  $ADMIN_DOWN delete exchange --name demo.fed || true
  $ADMIN_DOWN delete queue    --name demo.fed.q || true

  echo "--- Удаляем на UPSTREAM (rabbit1)..."
  $ADMIN_UP delete exchange --name demo.fed || true

  echo "=== [federation-демо] Очистка завершена ==="
  exit 0
fi

echo ""
echo "=== [federation-демо] Cross-cluster Federation Demo ==="
echo "  UPSTREAM:   rabbit1 (кластер rabbit1/2/3, AMQP 5672 / Management 15672)"
echo "  DOWNSTREAM: rabbit-b (отдельный кластер,  AMQP 5681 / Management 15681)"
echo "  Federation URI: ${UPSTREAM_URI}"
echo ""

pause "Нажмите Enter, чтобы начать шаг 1 — создать exchange на UPSTREAM..."

# ──────────────────────────────────────────────────────────────
# Шаг 1. Создаём exchange demo.fed на UPSTREAM (rabbit1)
# ──────────────────────────────────────────────────────────────
echo "=== Шаг 1. Объявляем exchange demo.fed на UPSTREAM (rabbit1) ==="

$ADMIN_UP declare exchange \
  --name demo.fed \
  --type fanout \
  --durable true

echo "Exchange demo.fed создан на rabbit1 (upstream)."

pause "Нажмите Enter для шага 2 — создать ресурсы на DOWNSTREAM (rabbit-b)..."

# ──────────────────────────────────────────────────────────────
# Шаг 2. Создаём exchange + очередь на DOWNSTREAM (rabbit-b)
# ──────────────────────────────────────────────────────────────
echo "=== Шаг 2. Объявляем exchange demo.fed и очередь demo.fed.q на DOWNSTREAM (rabbit-b) ==="

$ADMIN_DOWN declare exchange \
  --name demo.fed \
  --type fanout \
  --durable true

$ADMIN_DOWN declare queue \
  --name demo.fed.q \
  --type quorum \
  --durable true

$ADMIN_DOWN declare binding \
  --source demo.fed \
  --destination-type queue \
  --destination demo.fed.q \
  --routing-key ""

echo "Exchange demo.fed и очередь demo.fed.q созданы на rabbit-b (downstream)."

pause "Нажмите Enter для шага 3 — настроить federation upstream на rabbit-b..."

# ──────────────────────────────────────────────────────────────
# Шаг 3. Federation upstream на DOWNSTREAM (rabbit-b → rabbit1)
# Важно: vhost "/" должен быть url-encoded как %2F в AMQP URI.
# Без %2F RabbitMQ 4.x интерпретирует trailing "/" как пустой vhost → not_allowed.
# ──────────────────────────────────────────────────────────────
echo "=== Шаг 3. Задаём federation-upstream на rabbit-b (указывает на rabbit1) ==="

$CTL_DOWN set_parameter federation-upstream upstream-rabbit1 \
  "{\"uri\":\"${UPSTREAM_URI}\",\"expires\":3600000}"

echo "Upstream upstream-rabbit1 установлен на rabbit-b."
echo "  URI: ${UPSTREAM_URI}"

pause "Нажмите Enter для шага 4 — применить policy federation на rabbit-b..."

# ──────────────────────────────────────────────────────────────
# Шаг 4. Policy federation на DOWNSTREAM (rabbit-b)
# ──────────────────────────────────────────────────────────────
echo "=== Шаг 4. Применяем policy federate-fed-b на exchange demo.fed на rabbit-b ==="

$CTL_DOWN set_policy \
  --apply-to exchanges \
  federate-fed-b \
  "^demo\\.fed$" \
  '{"federation-upstream-set":"all"}'

echo "Policy federate-fed-b применена на rabbit-b."

# ──────────────────────────────────────────────────────────────
# Шаг 5. Ожидаем установки federation-link
# ──────────────────────────────────────────────────────────────
echo ""
echo "=== Шаг 5. Ожидаем установки federation-link (до 45 сек)... ==="

LINK_UP=false
for i in $(seq 1 9); do
  LINK_STATUS=$($CTL_DOWN eval 'rabbit_federation_status:status().' 2>/dev/null || echo "[]")
  if echo "$LINK_STATUS" | grep -q "running"; then
    LINK_UP=true
    echo "[OK] Federation-link в статусе running после $((i*5)) сек."
    break
  fi
  echo "  ... ожидаем (${i}/9, прошло $((i*5)) сек)"
  sleep 5
done

echo ""
echo "=== Статус federation parameters на rabbit-b: ==="
$CTL_DOWN list_parameters

echo ""
echo "=== Federation link status на rabbit-b: ==="
$CTL_DOWN eval 'rabbit_federation_status:status().'

if ! $LINK_UP; then
  echo ""
  echo "[ПРЕДУПРЕЖДЕНИЕ] federation-link не перешёл в running за 45 сек."
  echo "  Проверьте: docker logs rabbit-b | grep federation"
  echo "  Убедитесь что rabbit-b подключён к сети rabbitnet: docker inspect rabbit-b | grep Networks"
fi

pause "Нажмите Enter для шага 6 — опубликовать тестовое сообщение в UPSTREAM (rabbit1)..."

# ──────────────────────────────────────────────────────────────
# Шаг 6. Публикуем сообщение в UPSTREAM (rabbit1)
# rabbitmqadmin v2 синтаксис: publish message --exchange --routing-key --payload
# ──────────────────────────────────────────────────────────────
echo ""
echo "=== Шаг 6. Публикуем тестовое сообщение в exchange demo.fed на UPSTREAM (rabbit1) ==="

MSG_PAYLOAD="cross-cluster-federation-test-$(date +%s)"
$ADMIN_UP publish message \
  --exchange demo.fed \
  --routing-key "" \
  --payload "${MSG_PAYLOAD}"

echo "Сообщение опубликовано в rabbit1 (upstream): ${MSG_PAYLOAD}"

# ──────────────────────────────────────────────────────────────
# Шаг 7. Проверяем доставку на DOWNSTREAM (rabbit-b)
# ──────────────────────────────────────────────────────────────
echo ""
echo "=== Шаг 7. Ожидаем доставки сообщения в demo.fed.q на DOWNSTREAM (rabbit-b)... ==="

DELIVERED=false
for i in $(seq 1 6); do
  MSG_COUNT=$($CTL_DOWN list_queues name messages 2>/dev/null | awk '/demo\.fed\.q/ {print $2}' || echo "0")
  MSG_COUNT="${MSG_COUNT:-0}"
  if [ "${MSG_COUNT}" -gt 0 ] 2>/dev/null; then
    DELIVERED=true
    echo "[OK] demo.fed.q на rabbit-b содержит ${MSG_COUNT} сообщение(й) — cross-cluster federation работает!"
    break
  fi
  echo "  ... ожидаем (${i}/6): demo.fed.q на rabbit-b = ${MSG_COUNT} сообщений"
  sleep 5
done

echo ""
echo "=== Очереди на DOWNSTREAM (rabbit-b): ==="
$CTL_DOWN list_queues name messages

if $DELIVERED; then
  echo ""
  echo "╔══════════════════════════════════════════════════════════════╗"
  echo "║  УСПЕХ: Cross-cluster federation доставила сообщение!       ║"
  echo "║  Опубликовано в: rabbit1 (upstream) → exchange demo.fed     ║"
  echo "║  Получено в:     rabbit-b (downstream) → queue demo.fed.q   ║"
  echo "╚══════════════════════════════════════════════════════════════╝"
else
  echo ""
  echo "=== [ПРЕДУПРЕЖДЕНИЕ] Сообщение не найдено в demo.fed.q на rabbit-b ==="
  echo "  Возможные причины: federation-link ещё устанавливается, сетевая изоляция."
  echo "  Статус линка:"
  $CTL_DOWN eval 'rabbit_federation_status:status().' || true
fi

echo ""
echo "=== Для удаления артефактов: ==="
echo "  bash scripts/federation-demo.sh clean"
echo ""
echo "=== Management UI: ==="
echo "  UPSTREAM  rabbit1:  http://localhost:15672  (demo/demo)"
echo "  DOWNSTREAM rabbit-b: http://localhost:15681  (demo/demo)"
