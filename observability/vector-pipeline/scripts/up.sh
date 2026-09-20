#!/usr/bin/env bash
set -euo pipefail

TRANSPORT="${1:-${TRANSPORT:-nats}}"
if [[ "$TRANSPORT" != "nats" && "$TRANSPORT" != "kafka" ]]; then
  echo "транспорт должен быть nats или kafka, получено: $TRANSPORT" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

# Страховка для Windows/Git Bash: MSYS иначе переписывает похожие на POSIX-
# абсолютные пути в переменных окружения (например /etc/vector/...) в Windows-путь
# (C:/Program Files/Git/etc/vector/...) при вызове docker.exe, и AGENT_CONFIG /
# AGG_CONFIG ниже долетали бы до контейнера испорченными. На Linux/macOS переменная
# ничего не значит — там MSYS нет, поведение скрипта не меняется.
export MSYS_NO_PATHCONV=1

# Поднимаем ТОЛЬКО инфраструктуру и строго по списку. Без списка compose поднял бы
# все непрофилированные сервисы, включая генератор с дефолтной командой: он молча
# писал бы 5000 событий/с параллельно любому ручному прогону и загрязнял замеры
# (проверено живьём — 126355 событий вместо ожидаемых 2067).
docker compose --profile "$TRANSPORT" up -d opensearch prometheus "$TRANSPORT"

# Выбор конфигов агента и агрегатора по транспорту. AGENT_CONFIG/AGG_CONFIG —
# переменные compose (не Vector): их подставляет docker compose при разворачивании
# command ДО старта контейнера. Это НЕ интерполяция ${VAR} внутри YAML-конфигов
# Vector — та Vector 0.57 не делает, и в vector/*.yaml переменных нет и не должно быть.
# ${VAR:-default} уважает внешне заданное значение: скрипты замеров (Task 10,
# fail-sink.sh) подменяют AGG_CONFIG на вариант буфера ДО вызова up.sh, и эта логика
# не должна их перетирать.
if [[ "$TRANSPORT" == "kafka" ]]; then
  AGENT_CONFIG="${AGENT_CONFIG:-/etc/vector/agent-kafka.yaml}"
  AGG_CONFIG="${AGG_CONFIG:-/etc/vector/aggregator-kafka.yaml}"
else
  AGENT_CONFIG="${AGENT_CONFIG:-/etc/vector/agent.yaml}"
  AGG_CONFIG="${AGG_CONFIG:-/etc/vector/aggregator.yaml}"
fi
export AGENT_CONFIG AGG_CONFIG

echo "ждём OpenSearch..."
os_ready=""
for i in $(seq 1 60); do
  if curl -sk -u "admin:VectorDemo#2026" https://localhost:9224/_cluster/health \
     | grep -q '"status":"\(green\|yellow\)"'; then
    os_ready=1
    break
  fi
  sleep 2
done
if [[ -z "$os_ready" ]]; then
  echo "OpenSearch не поднялся за 120 секунд" >&2
  exit 1
fi
echo "OpenSearch готов"

# Порядок «агрегатор раньше агента» — требование транспорта, а не соглашение между
# людьми: у NATS core подписка «здесь и сейчас», события, отправленные до появления
# подписчика, теряются безвозвратно (Task 6). Поднимаем и ждём готовности агрегатора
# здесь, в скрипте, а не полагаемся на то, что вызывающий не перепутает порядок.
echo "поднимаем агрегатор ($AGG_CONFIG)..."
docker compose --profile "$TRANSPORT" up -d aggregator

echo "ждём старта агрегатора..."
agg_ready=""
for i in $(seq 1 30); do
  if docker compose logs aggregator 2>&1 | grep -q "Vector has started"; then
    agg_ready=1
    break
  fi
  if [[ "$(docker inspect --format '{{.State.Status}}' vp-agg 2>/dev/null)" == "exited" ]]; then
    echo "агрегатор упал при старте — см. docker compose logs aggregator" >&2
    exit 1
  fi
  sleep 2
done
if [[ -z "$agg_ready" ]]; then
  echo "агрегатор не стартовал за 60 секунд" >&2
  exit 1
fi
echo "агрегатор готов"

echo "поднимаем агент ($AGENT_CONFIG)..."
docker compose --profile "$TRANSPORT" up -d agent
