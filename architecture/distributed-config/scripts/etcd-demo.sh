#!/usr/bin/env bash
set -euo pipefail
# Демо etcd: запись, чтение и watch (реакция на изменение в реальном времени).
# Важно: в официальном образе etcd НЕТ shell — все команды идут напрямую через
# etcdctl, а таймаут на watch ставится на host-стороне.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DC="docker compose -f $ROOT_DIR/docker-compose.yml"
E="$DC exec -T etcd etcdctl"

echo "── etcd: put / get ──────────────────────────────────────────"
$E put /config/feature/new-ui true
$E get /config/feature/new-ui

echo ""
echo "── etcd: watch (реакция на изменение) ───────────────────────"
WATCH_OUT="$(mktemp)"
# watch в фоне с таймаутом на host-стороне (в etcd-образе нет shell для timeout)
timeout 6 $DC exec -T etcd etcdctl watch --prefix /config/ >"$WATCH_OUT" 2>&1 &
WPID=$!
sleep 1
$E put /config/feature/new-ui false
wait "$WPID" 2>/dev/null || true   # timeout штатно завершит watch (код 124)

echo "Получено watch:"
cat "$WATCH_OUT"
grep -q "/config/feature/new-ui" "$WATCH_OUT" \
  || { rm -f "$WATCH_OUT"; echo "etcd-demo: ОШИБКА — watch не увидел изменение" >&2; exit 1; }
rm -f "$WATCH_OUT"
echo "watch реально увидел изменение ключа."

echo ""
echo "── etcd: транзакция (CAS / leader election) ─────────────────"
# Атомарно занять /election/leader, только если ключа ещё нет (version == 0).
# При конфликте сработает ветка failure — и мы увидим текущего лидера.
$E del /election/leader >/dev/null 2>&1 || true   # сброс от прошлых прогонов
echo "nodeA пытается стать лидером:"
printf 'version("/election/leader") = "0"\n\nput /election/leader "nodeA"\n\nget /election/leader\n\n' | $E txn
echo "nodeB пытается стать лидером (ключ уже занят → ветка failure):"
printf 'version("/election/leader") = "0"\n\nput /election/leader "nodeB"\n\nget /election/leader\n\n' | $E txn
LEADER="$($E get /election/leader --print-value-only)"
[ "$LEADER" = "nodeA" ] \
  || { echo "etcd-demo: ОШИБКА — CAS не защитил лидера (получили: $LEADER)" >&2; exit 1; }

echo ""
echo "etcd-demo: ок (watch увидел изменение; CAS-транзакция защитила лидера = $LEADER)."
