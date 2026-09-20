#!/usr/bin/env bash
# Самопроверка стенда: сверяет число доехавших событий с ТОЧНЫМ ожиданием,
# которое генератор посчитал по построению (поля nginx_expected и app_expected).
#
# Почему точное ожидание, а не порог «доехало не меньше N процентов»: доля
# доехавших не константа. nginx-ветка теряет ровно запросы к /health, а app-ветка
# после дедупликации насыщается числом различимых комбинаций полей, поэтому с
# ростом партии доля падает и асимптотически идёт к 41.7%. Порог рядом с этой
# асимптотой врал бы ровно на больших прогонах — там, где проверка нужнее всего.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! manifest="$(docker compose run --rm --entrypoint sh loadgen -c 'cat /logs/manifest.json')"; then
  echo "не прочитать манифест генератора: том logs пуст или стенд не поднят" >&2
  exit 1
fi

field() {
  printf '%s' "$manifest" | tr -d '
' | sed "s/.*\"$1\":\([0-9]*\).*/\1/"
}

nginx_expected="$(field nginx_expected)"
app_expected="$(field app_expected)"
for v in "$nginx_expected" "$app_expected"; do
  if ! [[ "$v" =~ ^[0-9]+$ ]]; then
    echo "в манифесте нет полей nginx_expected/app_expected — генератор устарел" >&2
    exit 1
  fi
done
expected=$(( nginx_expected + app_expected ))

# страховка от вырожденного случая: ожидание и факт одновременно равны нулю
# дали бы (( 0 != 0 )) == false и прошли бы как "OK" без единого реального
# события. Естественным путём недостижимо (пустой/битый манифест ловится
# проверкой полей выше), но самопроверяющемуся стенду такая дыра не к лицу.
if (( expected == 0 )); then
  echo "ожидание нулевое: генератор не записал ни одного события — сравнивать нечего" >&2
  exit 1
fi

# кэш dedupe в агенте держит 5000 событий: если различимых ключей больше, часть
# повторов проскочит и точное ожидание перестанет быть точным
if (( app_expected >= 5000 )); then
  echo "app_expected=$app_expected >= 5000: кэш dedupe переполнен, ожидание неточное" >&2
  exit 1
fi

# ждём, пока архив перестанет расти: события могут быть ещё в пути.
# pipefail обязателен — без него падение docker было бы замаскировано кодом tr
prev=-1
stable=0
cur=0
for _ in $(seq 1 30); do
  if ! cur="$(docker compose exec -T aggregator sh -c 'wc -l < /archive/events.log 2>/dev/null || echo 0' | tr -d ' ')"; then
    echo "не прочитать архив: контейнер агрегатора не запущен?" >&2
    exit 1
  fi
  if [[ "$cur" == "$prev" ]]; then
    stable=$((stable + 1))
    if (( stable >= 2 )); then break; fi
  else
    stable=0
  fi
  prev="$cur"
  sleep 2
done
archived="$cur"

echo "ожидалось: $expected (nginx $nginx_expected + app $app_expected)"
echo "в архиве:  $archived"

if (( archived != expected )); then
  echo "ПРОВАЛ: расхождение $(( archived - expected )) событий" >&2
  exit 1
fi

# индексация в OpenSearch асинхронна: ждём, пока счётчик перестанет расти.
# Отдельно от стабилизации значения — само чтение _count по HTTPS уязвимо к
# единичному транспортному сбою (обрыв соединения, сброс при TLS-рукопожатии
# и т.п.); конкретно на этом стенде такой сбой воспроизведён живьём как
# таймаут TLS-рукопожатия curl (rc=28) через self-signed HTTPS и проброшенный
# Docker-порт — но защита ниже не завязана на эту конкретную причину, она
# просто не даёт одиночному сетевому сбою прервать проверку раньше времени.
# Поэтому такой сбой — повод повторить попытку в пределах того же окна
# ожидания, а не сразу считать OpenSearch недоступным; недоступность
# фиксируем, только если НИ ОДНА из 30 попыток не дала ответа. У самого curl
# добавлены явные таймауты (--connect-timeout/--max-time) — без них одна
# зависшая попытка могла бы висеть неопределённо долго, и окно из 30 итераций
# перестало бы быть предсказуемым по времени.
prev_idx=-1
idx_stable=0
indexed=0
idx_read_ok=0
for _ in $(seq 1 30); do
  if ! cur_idx="$(curl -sk --connect-timeout 3 --max-time 5 -u "admin:VectorDemo#2026" \
      "https://localhost:9224/logs-app-*/_count" | sed 's/.*"count":\([0-9]*\).*/\1/')"; then
    sleep 2
    continue
  fi
  if ! [[ "$cur_idx" =~ ^[0-9]+$ ]]; then
    echo "OpenSearch вернул не число: $cur_idx" >&2
    exit 1
  fi
  idx_read_ok=1
  indexed="$cur_idx"
  if [[ "$indexed" == "$prev_idx" ]]; then
    idx_stable=$((idx_stable + 1))
    if (( idx_stable >= 2 )); then break; fi
  else
    idx_stable=0
  fi
  prev_idx="$indexed"
  sleep 2
done

if (( idx_read_ok == 0 )); then
  echo "не прочитать _count индекса: OpenSearch недоступен?" >&2
  exit 1
fi

echo "в индексе: $indexed"
if (( indexed != expected )); then
  echo "ПРОВАЛ: в индексе $indexed вместо $expected (расхождение $(( indexed - expected )))" >&2
  exit 1
fi

echo "OK: доехало ровно столько, сколько должно"
