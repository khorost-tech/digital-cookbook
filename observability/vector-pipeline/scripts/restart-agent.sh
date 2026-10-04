#!/usr/bin/env bash
# Замер 6: потери и дубли при перезапуске агента.
# Использование: ./scripts/restart-agent.sh <persistent|ephemeral>
#
# Смысл: data_dir агента (том vector-agent-data) хранит checkpoints — позицию
# чтения файлов source file. persistent — обычный docker compose restart, том
# цел, агент продолжает с той же позиции. ephemeral имитирует потерю тома
# вместе с контейнером: source читает файлы заново (read_from: beginning
# действует, когда чекпойнта нет), dedupe_app в свежем процессе стартует с
# пустым in-memory кэшем и не узнаёт эти события как повторные — они снова
# доезжают до OpenSearch как НОВЫЕ документы (свой auto-generated _id).
#
# Имя тома проверяется живьём через `docker volume ls`, а не берётся на веру:
# директива `name:` в docker-compose.yml задаёт префикс проекта, и если он не
# совпадает с ожидаемым, `docker volume rm` на угаданное имя молча ничего не
# удалит — оба режима дадут одинаковый (нулевой) прирост, и весь замер будет
# ложным. Поэтому ниже: точный поиск тома по фильтру и подтверждение, что он
# реально исчез из `docker volume ls`, а не просто отвязан от контейнера.
set -euo pipefail

MODE="${1:-persistent}"
if [[ "$MODE" != "persistent" && "$MODE" != "ephemeral" ]]; then
  echo "режим должен быть persistent или ephemeral, получено: $MODE" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
OUT="results/06-restart-agent.txt"

# Страховка для Windows/Git Bash — та же, что в up.sh: MSYS иначе переписывает
# похожие на POSIX-пути аргументы/переменные при вызове docker.exe.
export MSYS_NO_PATHCONV=1

# Транспорт фиксирован на kafka: не принципиально для этого замера (речь про
# checkpoints агента, а не про транспорт), но kafka не роняет агрегатор при
# кратковременной недоступности брокера во время down/up между прогонами —
# смотри асимметрию nats/kafka в vector/aggregator.yaml.
export AGENT_CONFIG="/etc/vector/agent-kafka.yaml"

OS_AUTH="admin:VectorDemo#2026"
OS_COUNT_URL="https://localhost:9224/logs-app-*/_count"

# Ждём стабилизации счётчика _count (refresh в OpenSearch раз в секунду) с
# верхней границей и явным отказом — та же логика, что в verify.sh/bench.sh,
# вынесенная в функцию, т.к. здесь она нужна дважды (до и после перезапуска).
count_stable() {
  local prev=-1 stable=0 cur read_ok=0
  for _ in $(seq 1 30); do
    if cur="$(curl -sk --connect-timeout 3 --max-time 5 -u "$OS_AUTH" "$OS_COUNT_URL" \
        | sed 's/.*"count":\([0-9]*\).*/\1/')" && [[ "$cur" =~ ^[0-9]+$ ]]; then
      read_ok=1
      if [[ "$cur" == "$prev" ]]; then
        stable=$((stable + 1))
        if (( stable >= 2 )); then break; fi
      else
        stable=0
      fi
      prev="$cur"
    fi
    sleep 2
  done
  if (( read_ok == 0 )); then
    echo "не прочитать _count индекса: OpenSearch недоступен?" >&2
    return 1
  fi
  if (( stable < 2 )); then
    echo "счётчик _count не стабилизировался за отведённое окно (последнее значение: $prev)" >&2
    return 1
  fi
  printf '%s\n' "$prev"
}

if ! ./scripts/down.sh; then
  echo "не удалось погасить стенд перед прогоном — замер не начат" >&2
  exit 1
fi
./scripts/up.sh kafka

docker compose run --rm loadgen -rate 5000 -duration 20s

echo "ждём стабилизации индекса до перезапуска..." >&2
before="$(count_stable)"

# Момент "до" действия — отсекает старые строки лога при ожидании нового
# старта агента ниже. Unix-время: не зависит от часового пояса и формата,
# которые ждёт --since у `docker compose logs`.
restart_ts="$(date +%s)"

if [[ "$MODE" == "ephemeral" ]]; then
  VOLUME_NAME="$(docker volume ls -q --filter "name=vector-agent-data")"
  if [[ -z "$VOLUME_NAME" ]]; then
    echo "том vector-agent-data не найден в docker volume ls — стенд поднят без него?" >&2
    exit 1
  fi
  if [[ "$(printf '%s\n' "$VOLUME_NAME" | wc -l)" -ne 1 ]]; then
    echo "фильтр по имени тома вернул больше одного совпадения:"$'\n'"$VOLUME_NAME" >&2
    exit 1
  fi
  echo "том data_dir агента: $VOLUME_NAME" >&2

  docker compose rm -sf agent
  docker volume rm -f "$VOLUME_NAME"

  # Доказательство, что том реально удалён (а не просто отвязан от
  # контейнера): повторный список по тому же фильтру обязан быть пуст.
  still="$(docker volume ls -q --filter "name=vector-agent-data")"
  if [[ -n "$still" ]]; then
    echo "том $VOLUME_NAME всё ещё существует после rm -f — удаление не сработало" >&2
    exit 1
  fi
  echo "подтверждено: том $VOLUME_NAME отсутствует в docker volume ls" >&2

  docker compose --profile kafka up -d agent
else
  docker compose restart agent
fi

echo "ждём старта агента после перезапуска..." >&2
agent_ready=""
for i in $(seq 1 30); do
  if docker compose logs --since "$restart_ts" agent 2>&1 | grep -q "Vector has started"; then
    agent_ready=1
    break
  fi
  if [[ "$(docker inspect --format '{{.State.Status}}' vp-agent 2>/dev/null)" == "exited" ]]; then
    echo "агент упал после перезапуска — см. docker compose logs agent" >&2
    exit 1
  fi
  sleep 2
done
if [[ -z "$agent_ready" ]]; then
  echo "агент не стартовал за 60 секунд после перезапуска" >&2
  exit 1
fi

echo "ждём стабилизации индекса после перезапуска..." >&2
after="$(count_stable)"

# Файл результата держит ОБА режима одним файлом (persistent и ephemeral —
# два разных прогона, сравнение имеет смысл только вместе). Но повторный
# прогон ОДНОГО И ТОГО ЖЕ режима не должен просто дописываться в хвост —
# тогда накопились бы несколько блоков "=== режим: persistent" подряд, и
# без чтения списка коммитов было бы не понять, какой из них актуален.
# Поэтому перед записью нового блока вычищаем предыдущий блок ЭТОГО ЖЕ
# режима (если он есть), а блок другого режима не трогаем.
if [[ -f "$OUT" ]]; then
  awk -v mode="=== режим: $MODE" '
    $0 == mode { skip=1; next }
    /^=== режим: / { skip=0 }
    !skip { print }
  ' "$OUT" > "${OUT}.tmp"
  mv "${OUT}.tmp" "$OUT"
fi

{
  echo "=== режим: $MODE"
  if [[ "$MODE" == "ephemeral" ]]; then
    echo "том data_dir агента ($VOLUME_NAME) удалён и подтверждён отсутствующим до пересоздания"
  fi
  echo "в индексе до перезапуска:    $before"
  echo "в индексе после перезапуска: $after"
  echo "прирост:                     $(( after - before ))"
} | tee -a "$OUT"
