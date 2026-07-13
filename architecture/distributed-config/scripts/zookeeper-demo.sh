#!/usr/bin/env bash
set -euo pipefail
# Демо ZooKeeper: координация через znodes.
#  - persistent znode — распределённая конфигурация;
#  - ephemeral znode — liveness: узел существует, ПОКА жива сессия клиента.
#    Это основа service discovery и leader election в ZooKeeper.
# Команды подаются через stdin (одна сессия zkCli), а служебные логи (zkCli
# пишет их в stdout) отфильтровываются.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DC="docker compose -f $ROOT_DIR/docker-compose.yml"

# zkCli флакует, пока сервер не готов — ждём по zkServer.sh status
for i in $(seq 1 30); do
  $DC exec -T zookeeper zkServer.sh status >/dev/null 2>&1 && break
  [ "$i" -eq 30 ] && { echo "ОШИБКА: zookeeper не готов" >&2; exit 1; }
  sleep 1
done

# выполнить команды zkCli из stdin и вернуть только содержательные строки
zk() { $DC exec -T zookeeper zkCli.sh -server localhost:2181 2>&1; }

echo "── ZooKeeper: persistent znode (config) ─────────────────────"
printf 'create /config\ncreate /config/feature-new-ui true\nget /config/feature-new-ui\n' \
  | zk | grep -oE 'Created /config[A-Za-z/-]*|^true$' || true
VAL="$(printf 'get /config/feature-new-ui\n' | zk | grep -E '^true$' || true)"
[ "$VAL" = "true" ] || { echo "ОШИБКА: persistent znode не прочитан" >&2; exit 1; }

echo ""
echo "── ZooKeeper: ephemeral znode (liveness) ────────────────────"
# Ephemeral-узел живёт, только пока открыта сессия zkCli. Создаём его и закрываем
# сессию gracefully (close) — сервер удалит узел. Берём строку ls РОВНО вида
# []/[api], а не prompt zkCli ([zk: localhost:2181(CONNECTED) N]).
echo -n "в живой сессии: ls /services → "
printf 'create /services\ncreate -e /services/api host:8080\nls /services\nclose\n' \
  | zk | grep -E '^\[(api)?\]$' | tail -1
# ждём исчезновения узла (graceful close — быстро; иначе до session timeout ~30s)
AFTER="[api]"
for i in $(seq 1 30); do
  AFTER="$(printf 'ls /services\n' | zk | grep -E '^\[(api)?\]$' | tail -1 || true)"
  [ "$AFTER" = "[]" ] && break
  sleep 1
done
echo "после закрытия сессии: ls /services → $AFTER"
[ "$AFTER" = "[]" ] || { echo "ОШИБКА: ephemeral-узел не исчез (получили: $AFTER)" >&2; exit 1; }

echo ""
echo "zookeeper-demo: ок (persistent config остался, ephemeral-узел исчез вместе с сессией)."
