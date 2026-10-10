#!/usr/bin/env bash
# Самопроверка профилирования: доехали ли профили ОБОИХ сервисов, есть ли в них
# ожидаемые функции и работает ли разрез по спану.
#
#   ./scripts/verify-profiles.sh [запросов]          # режим PIPELINE (по умолчанию)
#   ./scripts/verify-profiles.sh fresh [запросов]    # режим FRESH
#
# ДВА РЕЖИМА, и путать их нельзя — они доказывают разное:
#
#   PIPELINE — «конвейер профилирования работает и отдаёт ожидаемые функции».
#   Ищет в окне последнего часа, поэтому на тёплом стенде находит функции из
#   ПРОШЛЫХ прогонов за секунды. Это честный ответ на вопрос «всё ли подключено»,
#   но НЕ доказательство того, что профиль собран текущим прогоном.
#
#   FRESH — «профиль ЭТОГО прогона доехал». Пересоздаёт контейнер Pyroscope
#   (хранилище у него внутри контейнера, тома нет), поэтому старых данных не
#   остаётся физически, и найденное принадлежит только текущей нагрузке. Дороже:
#   ждать приходится минуты, потому что свежие данные Go созревают долго.
#
# Раньше режим был один, с окном в час, и отчёт при этом называл результат
# проверкой прогона. Симптом был виден прямо в выводе: «профиль покоя» содержал
# 650 фреймов и CreateOrder, а итог всё равно оставался ГОДЕН.
#
# Критерий здесь — наличие КОНКРЕТНЫХ фреймов, а не «сервис ответил 200» и не
# «профиль непустой». Причина: Pyroscope на запрос по НЕ ТОМУ типу профиля
# отвечает HTTP 200 с пустым флеймграфом, без всякой ошибки. Проверка «ответ
# получен» такое пропускает, и полчаса уходит на поиск разрыва между приёмом и
# чтением, которого нет.
#
# Типы профиля у Go и Java РАЗНЫЕ, и это не догадка:
#   Go   отдаёт и process_cpu:samples:count:cpu:nanoseconds, и
#        process_cpu:cpu:nanoseconds:cpu:nanoseconds
#   Java отдаёт ТОЛЬКО process_cpu:cpu:nanoseconds:cpu:nanoseconds
# Запрос Java по типу samples возвращает 200, один фрейм и numTicks 0.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

MODE="pipeline"
if [ "${1:-}" = "fresh" ]; then
  MODE="fresh"
  shift
fi
# В fresh-режиме нагрузка по умолчанию БОЛЬШЕ, и это не запас «на всякий случай».
# go-frontend — прокси: почти всё время он ждёт ответа Java, своего процессорного
# времени у него мало, и CPU-сэмплов набирается немного. Замерено на чистом
# хранилище: при 4000 запросов функция CreateOrder в окне прогона так и не
# появилась (183 фрейма, но не та), при 6000 — появилась через 54 с. У Java
# обратная картина: 4200 фреймов почти сразу, она делает саму работу.
if [ "$MODE" = "fresh" ]; then
  REQUESTS="${1:-6000}"
else
  REQUESTS="${1:-3000}"
fi
# Сколько секунд держать нагрузку в FRESH-режиме. Профилировщик сэмплирует время,
# поэтому длительность важнее объёма — подробности у самого цикла нагрузки.
FRESH_LOAD_SECONDS="${FRESH_LOAD_SECONDS:-120}"
RESULT_FILE="$STAND_DIR/results/09-profiling.txt"

# Тип профиля, который есть у обоих языков. Единственный общий — на нём и строим
# сравнение, чтобы не сравнивать разное.
CPU_TYPE="process_cpu:cpu:nanoseconds:cpu:nanoseconds"

require_tools

now_ms() { "$PYTHON_BIN" -c "import time; print(int(time.time()*1000))"; }

# --- запросы к Pyroscope -----------------------------------------------------
#
# Тело запроса собирается ПИТОНОМ, а не склейкой строк в шелле, и это не вкус.
# Селектор Pyroscope сам содержит двойные кавычки: {service_name="go-frontend"}.
# При подстановке такого значения внутрь JSON-строки кавычки не экранируются, и
# уходит сломанное тело:
#     {"label_selector":"{service_name="go-frontend"}"}
# Pyroscope отвечает на него внятной ошибкой
#     {"code":"invalid_argument","message":"... proto: syntax error ..."}
# но разбор ответа, который просто берёт d['flamegraph']['names'], получает пустой
# список и сообщает «фреймов 0» — то есть ОШИБКА ЗАПРОСА превращается в вывод
# «профиля нет». На это ушло два прогона проверки. Поэтому:
#   1) тело формирует json.dumps, экранирование получается по построению;
#   2) разбор отличает ошибку сервера от честной пустоты и печатает ERR.

pyro_call() {
  local path="$1" payload="$2"
  innet -s --max-time 30 -X POST -H 'Content-Type: application/json' -d "$payload"     "http://pyroscope:4040/$path" 2>/dev/null
}

# Число фреймов и наличие искомой функции: «фреймов:найдено». При ошибке сервера
# возвращает «-1:-1», и это отличимо от «0:0» — пустого, но исправного ответа.
pyro_frames() {
  local service="$1" selector="$2" pattern="$3" start="$4" end="$5" payload
  payload="$("$PYTHON_BIN" -c "
import json, sys
print(json.dumps({
    'profile_typeID': sys.argv[1],
    'label_selector': sys.argv[2],
    'start': int(sys.argv[3]),
    'end': int(sys.argv[4]),
    'max_nodes': 8000,
}))
" "$CPU_TYPE" "$selector" "$start" "$end")"

  pyro_call "querier.v1.QuerierService/SelectMergeStacktraces" "$payload" |
    "$PYTHON_BIN" -c "
import sys, json
pattern = sys.argv[1]
try:
    d = json.load(sys.stdin)
except Exception:
    print('-1:-1'); raise SystemExit
if 'code' in d or 'message' in d:
    # Ошибка сервера, а не пустой профиль. Разница принципиальна: первое значит
    # «запрос неверен», второе — «данных за окно нет».
    sys.stderr.write('  ОШИБКА Pyroscope: %s\n' % d.get('message', d.get('code')))
    print('-1:-1'); raise SystemExit
names = d.get('flamegraph', {}).get('names', [])
print('%d:%d' % (len(names), 1 if any(pattern in n for n in names) else 0))
" "$pattern"
}

pyro_services() {
  pyro_call "querier.v1.QuerierService/Series" '{}' |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    out = set()
    for s in d.get('labelsSet', []):
        lab = {x['name']: x['value'] for x in s.get('labels', [])}
        if lab.get('service_name'):
            out.add(lab['service_name'])
    print(' '.join(sorted(out)))
except Exception:
    print('')
"
}

pyro_label_values() {
  local name="$1" service="$2" start="$3" end="$4" payload
  payload="$("$PYTHON_BIN" -c "
import json, sys
print(json.dumps({
    'name': sys.argv[1],
    'matchers': ['{service_name=\"%s\"}' % sys.argv[2]],
    'start': int(sys.argv[3]),
    'end': int(sys.argv[4]),
}))
" "$name" "$service" "$start" "$end")"
  pyro_call "querier.v1.QuerierService/LabelValues" "$payload" |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(' '.join(json.load(sys.stdin).get('names', [])) or 'НЕТ')
except Exception:
    print('ERR')
"
}

pyro_label_names() {
  local service="$1" start="$2" end="$3" payload
  payload="$("$PYTHON_BIN" -c "
import json, sys
print(json.dumps({
    'matchers': ['{service_name=\"%s\"}' % sys.argv[1]],
    'start': int(sys.argv[2]),
    'end': int(sys.argv[3]),
}))
" "$service" "$start" "$end")"
  pyro_call "querier.v1.QuerierService/LabelNames" "$payload" |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(' '.join(json.load(sys.stdin).get('names', [])))
except Exception:
    print('ERR')
"
}

# Ждать, пока профиль СТАНЕТ ДОСТУПЕН для запроса, и вернуть затраченное время.
#
# Задержка между приёмом профиля и его доступностью в запросах — МИНУТЫ, и это
# замерено, а не предположено. Прогон из 4000 запросов, затем опрос окна
# [начало нагрузки .. сейчас] каждые 15 с:
#
#   +21 с   фреймов   1
#   +75 с   фреймов   8
#   +129 с  фреймов  41
#   +238 с  фреймов  76
#   +292 с  фреймов 110
#   +438 с  фреймов 142
#
# При этом Pyroscope профили ПРИНЯЛ: pyroscope_distributor_received_compressed_
# bytes_sum{type="process_cpu"} растёт, отказов нет. Данные оседают в сегментах и
# становятся доступны после сброса и компакции.
#
# Практическое следствие для проверки: короткое ожидание даёт ложный вывод
# «профиля нет» при полностью исправном конвейере. Лимит поднят до 600 с, а окно
# запроса расширено назад — иначе проверка меряет не работу профилировщика, а
# скорость компактора.
# Ждём появления САМОЙ ФУНКЦИИ, а не числа фреймов. Разница существенная: число
# фреймов растёт постепенно по мере оседания данных, и порог по нему выполняется
# РАНЬШЕ, чем в профиле появляется искомый стек. Первая версия ждала 60 фреймов,
# дожидалась их за минуту и объявляла отказ по CreateOrder — который приезжал
# ещё через пару минут. Ждать надо ровно то, что проверяешь.
wait_frames() {
  local service="$1" pattern="$2" start="$3" least="$4" limit="${5:-600}"
  local began now_s res frames found
  began="$(date +%s)"
  while :; do
    res="$(pyro_frames "$service" "{service_name=\"$service\"}" "$pattern" "$start" "$(now_ms)")"
    frames="${res%%:*}"
    found="${res##*:}"
    now_s=$(( $(date +%s) - began ))
    if [ "$found" = "1" ] && [ "$frames" -ge "$least" ] 2>/dev/null; then
      echo "$now_s:$res"
      return 0
    fi
    if [ "$now_s" -ge "$limit" ]; then
      echo "$now_s:$res"
      return 1
    fi
    sleep 5
  done
}

# --- 0. включено ли профилирование вообще ------------------------------------

log "проверяю, что профилирование включено в обоих сервисах"
GO_PROF_LOG="$(docker compose logs go-frontend 2>/dev/null | grep -c 'профилирование включено' || true)"
JAVA_PROF_LOG="$(docker compose logs java-backend 2>/dev/null | grep -c 'Profiling started' || true)"
echo "  go-frontend: строк «профилирование включено» = $GO_PROF_LOG"
echo "  java-backend: строк «Profiling started» = $JAVA_PROF_LOG"

if [ "$GO_PROF_LOG" = "0" ] || [ "$JAVA_PROF_LOG" = "0" ]; then
  echo "ОШИБКА: профилирование не запущено. Подними стенд как" >&2
  echo "        ./scripts/up.sh all otel none both" >&2
  exit 1
fi

# Режим сэмплирования Java берём из его же лога: по нему видно, работает ITIMER
# или perf. Различить по строке «Profiling started» нельзя — она одинакова.
JAVA_EVENT="$(docker compose logs java-backend 2>/dev/null |
  grep -o 'profilingEvent=[A-Za-z_]*' | tail -1 | cut -d= -f2 || true)"
echo "  режим сэмплирования Java: ${JAVA_EVENT:-неизвестен}"

# В режиме FRESH хранилище Pyroscope стирается ДО ВСЕГО ОСТАЛЬНОГО: до замера
# покоя и до любой нагрузки. Контейнер пересоздаётся, тома у него нет, значит
# старые профили исчезают физически — и всё, что найдётся дальше, принадлежит
# только этому прогону.
#
# ПОРЯДОК ЗДЕСЬ — САМА СУТЬ ПРОВЕРКИ, и первая версия его нарушала: очистка
# стояла ПОСЛЕ нагрузки, то есть стирала ровно то, что прогон только что собрал.
# Проверка при этом заканчивалась успехом (данные успевали прийти уже после
# очистки), и «свежесть» снова оказывалась заявленной, а не доказанной — та же
# ошибка, ради исправления которой режим и заводился.
if [ "$MODE" = "fresh" ]; then
  log "режим FRESH: пересоздаю Pyroscope ДО замеров, чтобы старых профилей не осталось"
  docker compose --profile profiling rm -sf pyroscope >/dev/null 2>&1 || true
  docker compose --profile profiling up -d pyroscope >/dev/null 2>&1
  # Ждать приходится долго: готовность идёт тремя фазами (metastore 15 с,
  # ingester 15 с, segment-writer 30 с), плюс запуск модулей.
  wait_ready "pyroscope" "http://pyroscope:4040/ready" 240 || {
    echo "ОШИБКА: Pyroscope не поднялся после пересоздания — проверять нечего" >&2
    exit 1
  }
  # Приложения переподключаются к новому Pyroscope сами (push-модель, адрес тот
  # же), но профилировщику нужен новый цикл отправки — дадим ему стартовать.
  docker compose up -d --force-recreate go-frontend java-backend >/dev/null 2>&1
  wait_ready "go-frontend"  "http://go-frontend:8080/health" 60
  wait_ready "java-backend" "http://java-backend:8080/inventory/SKU-0001" 60
  # Контрольная точка: хранилище должно быть ПУСТЫМ. Если здесь что-то нашлось,
  # значит пересоздание не сработало, и дальше проверять свежесть бессмысленно.
  leftovers="$(pyro_frames go-frontend '{service_name="go-frontend"}' 'CreateOrder'     "$("$PYTHON_BIN" -c "import time; print(int(time.time()*1000) - 3600*1000)")" "$(now_ms)")"
  if [ "${leftovers##*:}" = "1" ]; then
    echo "ОШИБКА: после пересоздания Pyroscope в хранилище всё ещё есть CreateOrder" >&2
    echo "        ($leftovers) — очистка не сработала, свежесть недоказуема." >&2
    exit 1
  fi
  echo "  хранилище пусто: $leftovers (фреймов:найдено)"
fi

# --- 1. профиль в покое ------------------------------------------------------
#
# Опыт из двух частей: сначала снимаем профиль БЕЗ нагрузки, потом под нагрузкой.
# Без первой части нельзя утверждать, что широкий фрейм появился ИЗ-ЗА нагрузки,
# а не был там всегда.

log "снимаю профиль в покое (нагрузки нет)"
IDLE_START="$(now_ms)"
sleep 20
IDLE_END="$(now_ms)"
IDLE_GO="$(pyro_frames go-frontend '{service_name="go-frontend"}' 'CreateOrder' "$IDLE_START" "$IDLE_END")"
IDLE_GO_FRAMES="${IDLE_GO%%:*}"
IDLE_GO_FOUND="${IDLE_GO##*:}"
echo "  go-frontend в покое: фреймов $IDLE_GO_FRAMES, CreateOrder найден: $IDLE_GO_FOUND"
# Профиль покоя проверяется ПОСЛЕ прогона, когда данные того окна уже доступны:
# сразу после сна окно ещё пусто из-за задержки доступности, и вывод «в покое
# CreateOrder нет» был бы получен просто из-за отсутствия данных.

# --- 2. нагрузка ------------------------------------------------------------

log "прогон нагрузки: $REQUESTS запросов"
LOAD_START="$(now_ms)"
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$REQUESTS" -concurrency 8 \
  -run "profiles" -json 2>/dev/null | sed -n '/^{/,$p' > "$STAND_DIR/results/.last-load-profiles.json" || true

# В FRESH-режиме нагрузка держится ДОЛГО, а не одним залпом, и это ключевая
# деталь, а не запас на всякий случай.
#
# CPU-профилировщик сэмплирует ВРЕМЯ (100 Гц), а не запросы. 6000 запросов
# проходят за 15 секунд и дают около 1500 сэмплов на весь процесс; у сервиса,
# который почти всё время ждёт ответа соседа, прикладные функции в такую выборку
# не попадают вовсе. Замерено на чистом хранилище: после залпового прогона в
# профиле go-frontend оставалось 179 фреймов служебного (сборка мусора,
# периодический reader OTel SDK) и НИ ОДНОЙ своей функции — сколько ни ждать
# оседания данных, их там просто нет.
#
# Поэтому нагрузка повторяется циклом, пока не истечёт FRESH_LOAD_SECONDS. Число
# запросов при этом перестаёт быть целью и становится способом занять процессор.
if [ "$MODE" = "fresh" ]; then
  log "FRESH: держу нагрузку ${FRESH_LOAD_SECONDS}с — профилировщику нужно ВРЕМЯ, а не запросы"
  load_began="$(date +%s)"
  rounds=1
  while [ $(( $(date +%s) - load_began )) -lt "$FRESH_LOAD_SECONDS" ]; do
    docker compose run --rm --no-deps -T loadgen       -target http://go-frontend:8080 -requests "$REQUESTS" -concurrency 8       -run "profiles-$rounds" >/dev/null 2>&1 || true
    rounds=$((rounds + 1))
  done
  echo "  проходов нагрузки: $rounds за $(( $(date +%s) - load_began ))с"
fi

LOAD_OK="$("$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['actual_by_status'].get('201', 0))
" < "$STAND_DIR/results/.last-load-profiles.json")"
echo "  успешных запросов: $LOAD_OK"

# --- 3. фреймы обоих языков --------------------------------------------------
#
# Порог по числу фреймов заведомо ниже наблюдённого (Go около 600, Java около
# 5000) и служит защитой от «профиль формально есть, но пустой».

# Критерии проверяются на ШИРОКОМ окне — последний час, а не окно прогона.
#
# Причина фактическая. Свежие данные Go становятся полностью доступны примерно
# через десять минут: замер по ширине окна в один и тот же момент дал
#   окно  300 с ->  195 фреймов, CreateOrder нет
#   окно  900 с ->  723 фрейма,  CreateOrder ЕСТЬ
#   окно 3600 с -> 1558 фреймов, CreateOrder ЕСТЬ
# У Java задержка на порядок меньше (секунды), у Go — минуты.
#
# Вопрос, на который отвечает эта проверка, — «профилирование работает и даёт
# ожидаемые функции», а не «данные появляются мгновенно». Привязка критерия к
# узкому окну прогона превращала бы его в проверку скорости компактора, и первые
# три прогона именно это и показывали: ОТКАЗ при полностью исправном конвейере.
#
# Задержка при этом не замалчивается — она замеряется ниже и попадает в отчёт
# отдельной строкой, но вердикта по ней нет.
# В режиме FRESH окно начинается от старта проверки: раньше него данных не
# существует, потому что хранилище стёрто. В PIPELINE — час назад.
if [ "$MODE" = "fresh" ]; then
  WIDE_START="$LOAD_START"
else
  WIDE_START="$("$PYTHON_BIN" -c "import time; print(int(time.time()*1000) - 3600*1000)")"
fi

log "жду, пока профиль Go станет доступен для запроса"
# Порог 60, а не 100: число фреймов зависит от того, сколько успело осесть, и
# завышенный порог превращает проверку в проверку скорости компактора. Наличие
# КОНКРЕТНОЙ функции проверяется отдельным критерием — он и есть содержательный.
# Лимит ожидания в fresh-режиме щедрее: там окно узкое и данные должны ещё
# осесть. В pipeline-режиме окно широкое, и если функции нет за 300 с — её нет.
GO_WAIT_LIMIT=300
[ "$MODE" = "fresh" ] && GO_WAIT_LIMIT=600
GO_WAIT_RES="$(wait_frames go-frontend 'CreateOrder' "$WIDE_START" 60 "$GO_WAIT_LIMIT")" || true
GO_WAIT="${GO_WAIT_RES%%:*}"
GO_RES="${GO_WAIT_RES#*:}"
GO_FRAMES="${GO_RES%%:*}"
GO_FOUND="${GO_RES##*:}"
echo "  go-frontend: фреймов $GO_FRAMES, CreateOrder найден: $GO_FOUND, ждали ${GO_WAIT}с"

log "жду профиль Java"
JAVA_WAIT_RES="$(wait_frames java-backend 'InventoryController' "$WIDE_START" 300 "$GO_WAIT_LIMIT")" || true
JAVA_WAIT="${JAVA_WAIT_RES%%:*}"
JAVA_RES="${JAVA_WAIT_RES#*:}"
JAVA_FRAMES="${JAVA_RES%%:*}"
JAVA_FOUND="${JAVA_RES##*:}"
echo "  java-backend: фреймов $JAVA_FRAMES, InventoryController найден: $JAVA_FOUND, ждали ${JAVA_WAIT}с"

# Окно фиксируется ПОСЛЕ ожидания: все дальнейшие запросы должны использовать то
# же окно, в котором данные уже точно доступны.
LOAD_END="$(now_ms)"
# Дальше всё считается на том же широком окне, что и критерии выше: смешивать
# окна в одной проверке — верный способ получить несравнимые числа.
LOAD_START="$WIDE_START"

SERVICES="$(pyro_services)"
echo "  сервисы с профилями: $SERVICES"
GO_PRESENT=0
JAVA_PRESENT=0
case " $SERVICES " in *" go-frontend "*) GO_PRESENT=1 ;; esac
case " $SERVICES " in *" java-backend "*) JAVA_PRESENT=1 ;; esac

# --- 4. связка со спаном -----------------------------------------------------
#
# Здесь проверяется не «библиотека подключена», а что метки реально появились и
# по ним можно отфильтровать профиль.

log "проверяю разрез профиля по спану"
LABELS="$(pyro_label_names go-frontend "$LOAD_START" "$LOAD_END")"
echo "  метки профилей go-frontend: $LABELS"
HAS_SPAN_NAME=0
case " $LABELS " in *" span_name "*) HAS_SPAN_NAME=1 ;; esac

SPAN_VALUES="$(pyro_label_values span_name go-frontend "$LOAD_START" "$LOAD_END")"
echo "  значения span_name: $SPAN_VALUES"

# Первое значение метки и есть имя корневого спана. Берём его из данных, а не
# зашиваем: имя зависит от того, что успел записать otelhttp к моменту старта.
FIRST_SPAN="$(echo "$SPAN_VALUES" | awk '{print $1}')"
SPAN_RES="$(pyro_frames go-frontend "{service_name=\"go-frontend\", span_name=\"$FIRST_SPAN\"}" 'CreateOrder' "$LOAD_START" "$LOAD_END")"
SPAN_FRAMES="${SPAN_RES%%:*}"
SPAN_FOUND="${SPAN_RES##*:}"
echo "  профиль по span_name=\"$FIRST_SPAN\": фреймов $SPAN_FRAMES, CreateOrder найден: $SPAN_FOUND"

# Контрольная точка: у ВЛОЖЕННОГО спана метки быть не должно. Если она вдруг
# появится, значит поведение библиотеки изменилось, и вывод статьи устарел.
NESTED_RES="$(pyro_frames go-frontend '{service_name="go-frontend", span_name="create_order"}' 'CreateOrder' "$LOAD_START" "$LOAD_END")"
NESTED_FRAMES="${NESTED_RES%%:*}"
echo "  профиль по span_name=\"create_order\" (вложенный спан): фреймов $NESTED_FRAMES"

# Перепроверка покоя. ВНИМАНИЕ на интерпретацию: окно покоя длится 20 с и снято
# перед нагрузкой, но если стенд работал до запуска проверки, в этом окне уже
# есть осевшая активность прошлых прогонов. На чистом стенде критерий осмыслен,
# на «горячем» — нет, и вердикт по нему смотреть не следует.
log "перепроверяю профиль покоя (данные того окна теперь доступны)"
IDLE_RECHECK="$(pyro_frames go-frontend '{service_name="go-frontend"}' 'CreateOrder' "$IDLE_START" "$IDLE_END")"
IDLE_GO_FRAMES="${IDLE_RECHECK%%:*}"
IDLE_GO_FOUND="${IDLE_RECHECK##*:}"
echo "  покой: фреймов $IDLE_GO_FRAMES, CreateOrder найден: $IDLE_GO_FOUND"

# --- 5. ожидание не попадает в CPU-профиль -----------------------------------
#
# На стенде есть медленный путь: pg_sleep внутри запроса к БД. В трейсе он виден
# как длинный спан, а в CPU-профиле его быть НЕ должно — процессор в это время
# не занят. Это ровно то различие, из-за которого профиль не заменяет трейс.

log "проверяю, что ожидание в БД не выглядит как работа процессора"
SLEEP_RES="$(pyro_frames java-backend '{service_name="java-backend"}' 'pg_sleep' "$LOAD_START" "$LOAD_END")"
SLEEP_FRAMES="${SLEEP_RES%%:*}"
SLEEP_FOUND="${SLEEP_RES##*:}"
echo "  фреймов в профиле Java: $SLEEP_FRAMES, pg_sleep среди них: $SLEEP_FOUND"

# ОБЯЗАТЕЛЬНАЯ контрольная точка. Вывод «pg_sleep в профиле нет» имеет смысл
# только если профиль вообще есть: на пустом профиле не найдётся ничего, и
# критерий прошёл бы, ничего не проверив. Первая версия этой проверки именно так
# и отрапортовала ГОДЕН на пустых данных.
SLEEP_VERDICT_VALID=1
if [ "$SLEEP_FRAMES" -lt 300 ] 2>/dev/null; then
  SLEEP_VERDICT_VALID=0
  echo "  ВНИМАНИЕ: профиль Java слишком мал ($SLEEP_FRAMES фреймов) — вывод про pg_sleep недействителен"
fi

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-42s %s (ожидалось %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-42s %s (ожидалось %s)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

verdict_at_least() {
  local name="$1" got="$2" least="$3"
  if [ "$got" -ge "$least" ] 2>/dev/null; then
    printf "  ГОДЕН   %-42s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
  else
    printf "  ОТКАЗ   %-42s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
    FAILED=1
  fi
}

echo
log "вердикт"
verdict "профиль go-frontend присутствует"      "$GO_PRESENT"   "1"
verdict "профиль java-backend присутствует"     "$JAVA_PRESENT" "1"
verdict "CreateOrder в профиле Go"              "$GO_FOUND"     "1"
verdict "InventoryController в профиле Java"    "$JAVA_FOUND"   "1"
# Порог по числу фреймов заведомо ниже наблюдённого (Go около 600, Java около
# 5000) и служит только защитой от «профиль формально есть, но пустой».
verdict_at_least "фреймов в профиле Go"         "$GO_FRAMES"     60
verdict_at_least "фреймов в профиле Java"       "$JAVA_FRAMES"  300
verdict "метка span_name присутствует"          "$HAS_SPAN_NAME" "1"
verdict "CreateOrder в профиле по корневому спану" "$SPAN_FOUND" "1"
if [ "$SLEEP_VERDICT_VALID" = 1 ]; then
  verdict "ожидание pg_sleep НЕ в CPU-профиле"  "$SLEEP_FOUND"  "0"
else
  printf "  ОТКАЗ   %-42s профиль пуст, проверять нечего
" "ожидание pg_sleep НЕ в CPU-профиле"
  FAILED=1
fi
# Вердикт по покою выносится только если профиль покоя ПУСТ по числу фреймов:
# иначе это не покой, а хвост прошлой работы, и утверждение о нём бессмысленно.
if [ "$IDLE_GO_FRAMES" -le 5 ] 2>/dev/null; then
  verdict "CreateOrder отсутствует в профиле покоя" "$IDLE_GO_FOUND" "0"
else
  printf "  ПРОПУСК %-42s в окне покоя %s фреймов — стенд был горячим
"     "CreateOrder отсутствует в профиле покоя" "$IDLE_GO_FRAMES"
fi

echo
if [ "$FAILED" = 0 ]; then
  if [ "$MODE" = "fresh" ]; then
    echo "ИТОГ: профили ЭТОГО прогона доехали, разрез по спану работает."
  else
    echo "ИТОГ: конвейер профилирования работает и отдаёт ожидаемые функции."
    echo "      ВНИМАНИЕ: это НЕ доказательство свежести — окно час, найденное могло"
    echo "      прийти из прошлых прогонов. Для проверки свежести: verify-profiles.sh fresh"
  fi
else
  echo "ИТОГ: есть расхождения — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Самопроверка профилирования"
  echo "==========================="
  if [ "$MODE" = "fresh" ]; then
    echo "РЕЖИМ: FRESH — хранилище Pyroscope стёрто до прогона, окно начинается"
    echo "       от старта проверки. Найденное принадлежит ТОЛЬКО этому прогону."
  else
    echo "РЕЖИМ: PIPELINE — окно последнего часа. Проверяется, что конвейер"
    echo "       работает и отдаёт ожидаемые функции. НЕ доказывает свежесть:"
    echo "       на тёплом стенде функции находятся из прошлых прогонов."
  fi
  echo "Pyroscope 2.2.0, pyroscope-go 1.4.1, otel-profiling-go 0.6.0,"
  echo "агент Pyroscope для Java 2.9.0, режим сэмплирования Java: ${JAVA_EVENT:-неизвестен}."
  echo "Нагрузка: $REQUESTS запросов, успешных $LOAD_OK."
  echo
  echo "Тип профиля, общий для обоих языков: $CPU_TYPE"
  echo "  Go отдаёт также process_cpu:samples:count:cpu:nanoseconds;"
  echo "  Java по этому типу возвращает 200, один фрейм и numTicks 0."
  echo
  echo "Профили"
  echo "  сервисы с данными: $SERVICES"
  echo "  go-frontend:  фреймов $GO_FRAMES, CreateOrder $GO_FOUND"
  echo "  java-backend: фреймов $JAVA_FRAMES, InventoryController $JAVA_FOUND"
  echo "  в покое (20 с без нагрузки): фреймов $IDLE_GO_FRAMES, CreateOrder $IDLE_GO_FOUND"
  echo "  ожидание доступности профиля: Go ${GO_WAIT}с, Java ${JAVA_WAIT}с"
  echo "  ЗАДЕРЖКА ДОСТУПНОСТИ: замерена отдельно, составляет МИНУТЫ. Прогон 4000"
  echo "  запросов, опрос окна [начало нагрузки .. сейчас] каждые 15 с дал"
  echo "  1 фрейм на +21 с, 41 на +129 с, 110 на +292 с, 142 на +438 с — при том"
  echo "  что Pyroscope профили принял и отказов не было. Данные оседают в"
  echo "  сегментах и становятся доступны после сброса и компакции."
  echo
  echo "Связка со спаном"
  echo "  метки: $LABELS"
  echo "  значения span_name: $SPAN_VALUES"
  echo "  профиль по span_name=\"$FIRST_SPAN\": фреймов $SPAN_FRAMES, CreateOrder $SPAN_FOUND"
  echo "  профиль по вложенному спану create_order: фреймов $NESTED_FRAMES"
  echo
  echo "Ожидание против работы"
  echo "  pg_sleep в CPU-профиле Java: $SLEEP_FOUND (ожидается 0)"
  echo
  if [ "$FAILED" = 0 ]; then
    echo "ИТОГ: ГОДЕН"
  else
    echo "ИТОГ: ОТКАЗ"
  fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
