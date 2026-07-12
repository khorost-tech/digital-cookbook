#!/usr/bin/env bash
set -euo pipefail
# Сквозная демонстрация различий rootful/rootless Docker (неинтерактивно):
#   1. docker compose up -d --build
#   2. Ожидание готовности probe (retry curl /info)
#   3. uid ВНУТРИ контейнера (из /info)
#   4. Владелец файла, записанного контейнером в volume, на ХОСТЕ
#   5. Определение режима демона (rootless: true/false) и интерпретация
#   6. docker compose down -v + очистка data/ (trap EXIT — всегда)
#
# Использование:
#   bash scripts/smoke.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DATA_DIR="$ROOT_DIR/data"
PROBE_FILE="$DATA_DIR/written-by-container.txt"

ts() { date '+%H:%M:%S'; }

cleanup() {
  echo "" >&2
  echo "[$(ts)] Trap EXIT — docker compose down -v + очистка data/..." >&2
  docker compose -f "$ROOT_DIR/docker-compose.yml" down -v 2>/dev/null || true
  [ -d "$DATA_DIR" ] || return 0
  rm -rf "$DATA_DIR" 2>/dev/null || true
  # На rootful + ext4 файл может быть root-owned — обычным rm не удалить.
  # Дочищаем через контейнер (он пишет от root в bind mount).
  if [ -d "$DATA_DIR" ]; then
    docker run --rm -v "$DATA_DIR":/d alpine sh -c 'rm -rf /d/* /d/.[!.]* 2>/dev/null' 2>/dev/null || true
    rmdir "$DATA_DIR" 2>/dev/null || true
  fi
  if [ -d "$DATA_DIR" ]; then
    echo "[$(ts)] ВНИМАНИЕ: $DATA_DIR не удалён (возможно root-owned). Удалите вручную: sudo rm -rf $DATA_DIR" >&2
  fi
}
trap cleanup EXIT

fail() { echo "[$(ts)] ОШИБКА: $1" >&2; exit 1; }

echo "============================================================"
echo " Demo: rootful vs rootless Docker"
echo " Время старта: $(ts)"
echo "============================================================"

mkdir -p "$DATA_DIR"

echo "[$(ts)] docker compose up -d --build..."
docker compose -f "$ROOT_DIR/docker-compose.yml" up -d --build || fail "compose up не удался"

echo "[$(ts)] Ожидание готовности probe на :8080..."
for i in $(seq 1 30); do
  if curl -fsS http://localhost:8080/info >/dev/null 2>&1; then break; fi
  [ "$i" -eq 30 ] && fail "probe не ответил за 30 попыток"
  sleep 1
done

echo ""
echo "── Ответ probe (/info) — uid ВНУТРИ контейнера ──────────────"
curl -fsS http://localhost:8080/info || fail "не удалось получить /info"
echo ""

[ -f "$PROBE_FILE" ] || fail "контейнер не создал $PROBE_FILE в volume"

echo ""
echo "── Владелец файла на ХОСТЕ (ls -ln) ─────────────────────────"
ls -ln "$PROBE_FILE"
HOST_UID="$(stat -c '%u' "$PROBE_FILE")"

echo ""
echo "── Режим демона ─────────────────────────────────────────────"
ROOTLESS="$(docker info -f '{{.SecurityOptions}}' 2>/dev/null | grep -o 'rootless' || true)"
MY_UID="$(id -u)"
if [ -n "$ROOTLESS" ]; then
  echo "Демон: ROOTLESS"
  echo "Файл в volume на хосте принадлежит uid=$HOST_UID (ваш пользователь / subuid),"
  echo "а НЕ root — удаляется без sudo. В этом и ценность rootless."
else
  echo "Демон: ROOTFUL"
  if [ "$HOST_UID" = "0" ]; then
    echo "Файл в volume на хосте принадлежит uid=0 (root)."
    echo "Чтобы удалить такой файл, обычно нужен sudo. Запустите тот же стенд"
    echo "на rootless-демоне — владельцем станет ваш пользователь."
  else
    echo "ВНИМАНИЕ: демон rootful, но владелец файла uid=$HOST_UID (не root)."
    echo "Скорее всего стенд лежит на ФС, которая маскирует владельца"
    echo "(WSL /mnt/*, DrvFs, NFS с root squash). Демонстрация ownership"
    echo "НЕДОСТОВЕРНА — перенесите стенд на нативную Linux-ФС (ext4 в \$HOME)"
    echo "и запустите снова."
  fi
fi

echo ""
echo "[$(ts)] Demo завершено успешно."
