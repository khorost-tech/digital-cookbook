#!/usr/bin/env bash
set -euo pipefail

# Federation-демо: использует ИЗОЛИРОВАННЫЙ exchange demo.fed (не demo.orders),
# чтобы self-loop upstream не дублировал рабочий трафик других демо.
# В продакшне upstream — ДРУГОЙ кластер (второй compose в отдельной сети).
#
# Использование:
#   bash scripts/federation-demo.sh          — развернуть демо
#   bash scripts/federation-demo.sh clean    — удалить все артефакты
#   AUTO=1 bash scripts/federation-demo.sh   — неинтерактивный режим (паузы пропускаются)
#   bash scripts/federation-demo.sh --auto   — то же, что AUTO=1

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
  shift
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

RABBIT="docker exec rabbit1"
ADMIN="$RABBIT rabbitmqadmin --username demo --password demo"
CTL="$RABBIT rabbitmqctl"

# ──────────────────────────────────────────────────────────────
# Режим очистки
# ──────────────────────────────────────────────────────────────
if [[ "${1:-}" == "clean" ]]; then
  echo "=== [federation-демо] Очистка артефактов ==="
  $CTL clear_policy federate-fed || true
  $CTL clear_parameter federation-upstream demo-upstream || true
  $ADMIN delete exchange --name demo.fed || true
  $ADMIN delete queue    --name demo.fed.q || true
  echo "=== [federation-демо] Готово: policy federate-fed и upstream demo-upstream удалены ==="
  exit 0
fi

# ──────────────────────────────────────────────────────────────
# Шаг 1. Создать изолированные ресурсы (exchange + очередь)
# ──────────────────────────────────────────────────────────────
echo "=== [federation-демо] Объявляем exchange demo.fed и очередь demo.fed.q ==="

$ADMIN declare exchange \
  --name demo.fed \
  --type fanout \
  --durable true

$ADMIN declare queue \
  --name demo.fed.q \
  --type quorum \
  --durable true

$ADMIN declare binding \
  --source demo.fed \
  --destination-type queue \
  --destination demo.fed.q \
  --routing-key ""

echo "Exchange demo.fed и очередь demo.fed.q созданы и привязаны."

# ──────────────────────────────────────────────────────────────
# Шаг 2. Federation upstream
# ──────────────────────────────────────────────────────────────
echo "=== [federation-демо] Задаём upstream demo-upstream (self-loop для демонстрации механизма) ==="

$CTL set_parameter federation-upstream demo-upstream \
  '{"uri":"amqp://demo:demo@rabbit1:5672","expires":3600000}'

echo "Upstream demo-upstream установлен."

# ──────────────────────────────────────────────────────────────
# Шаг 3. Policy ТОЛЬКО на demo.fed — demo.orders НЕ затрагивается
# ──────────────────────────────────────────────────────────────
echo "=== [federation-демо] Применяем policy federate-fed на pattern ^demo\\.fed$ ==="

$CTL set_policy \
  --apply-to exchanges \
  federate-fed \
  "^demo\\.fed$" \
  '{"federation-upstream-set":"all"}'

echo "Policy federate-fed применена только к exchange demo.fed (demo.orders не затронут)."

# ──────────────────────────────────────────────────────────────
# Шаг 4. Проверка
# ──────────────────────────────────────────────────────────────
echo ""
echo "=== [federation-демо] Текущие parameters (federation-upstream): ==="
$CTL list_parameters

echo ""
echo "=== [federation-демо] Federation status ([] или self-link — оба варианта нормальны для self-upstream): ==="
$CTL eval 'rabbit_federation_status:status().'

echo ""
echo "=== [federation-демо] ПРИМЕЧАНИЕ ==="
echo "  - Upstream и downstream — ОДИН кластер (self-loop)."
echo "  - Это показывает МЕХАНИЗМ federation-link, а не реальную cross-cluster репликацию."
echo "  - В продакшне upstream — URI другого кластера (второй compose в отдельной сети)."
echo "  - policy federate-fed матчит ТОЛЬКО ^demo\\.fed$; demo.orders НЕ затронут."
echo ""
echo "=== [federation-демо] Для удаления артефактов: ==="
echo "  bash scripts/federation-demo.sh clean"
