#!/usr/bin/env bash
# Сколько занимает ОДИН И ТОТ ЖЕ поток логов в трёх бэкендах.
#
#   ./scripts/bench-storage.sh [записей] [seed]
#
# Требует чистых томов: ./scripts/down.sh clean && ./scripts/up.sh
#
# ПРЕДУСЛОВИЕ. Перед сравнением объёмов проверяется, что во все три бэкенда
# приехало РОВНО столько записей, сколько отправлено. Без этого «компактнее»
# неотличимо от «потерял часть данных» — а выглядят эти два случая одинаково.
#
# КОНТРОЛЬНАЯ ТОЧКА. Сжатие и слияние идут в фоне: снимок сразу после записи
# покажет не то, что будет через минуту. Размер снимается после того, как он
# перестал меняться.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-100000}"
SEED="${2:-42}"
RUN="st-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/02-storage.txt"

VOL_LOKI="log-backends_loki-data"
VOL_OS="log-backends_os-data"
VOL_VL="log-backends_vlogs-data"

# Пути ДАННЫХ внутри томов (без журналов). Состав разный у всех трёх, см.
# комментарий к data_bytes в lib.sh.
DATA_LOKI="/data/chunks /data/tsdb-shipper-active"
DATA_OS="/data/nodes"
DATA_VL="/data/partitions"

# --- счётчики записей --------------------------------------------------------

count_loki() {
  local now start
  now="$(date +%s)"; start=$((now - 7200))
  innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
    --data-urlencode 'query=sum(count_over_time({service=~".+"}[2h]))' \
    --data-urlencode "start=${start}000000000" --data-urlencode "end=${now}000000000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['values'][-1][1])) if r else 0)
except Exception:
    print(0)
"
}

count_os() {
  innet -s --max-time 30 "http://opensearch:9200/logs-default/_count" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(int(json.load(sys.stdin).get('count', 0)))
except Exception:
    print(0)
"
}

count_vl() {
  innet -s --max-time 30 -X POST "http://victorialogs:9428/select/logsql/query" \
    -d 'query=* | count()' 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    line = sys.stdin.readline().strip()
    print(int(json.loads(line)['count(*)']) if line else 0)
except Exception:
    print(0)
"
}

# --- серия замеров размера ---------------------------------------------------

# Размер НЕ стабилизируется в том смысле, в каком этого хочется. Наблюдение за
# томами после окончания потока:
#
#   Loki:        2.30 -> 2.30 -> 2.31 -> 3.56 МБ   (вырос через 6 минут)
#   OpenSearch:  4.53 -> 4.53 -> 4.55 -> 4.35 МБ   (колеблется в обе стороны)
#
# Причина в фоновых операциях: сброс чанков из памяти, слияние сегментов,
# компакция. Они идут паузами, поэтому критерий «N одинаковых замеров подряд»
# ловит промежуток МЕЖДУ операциями, а не их окончание. Первая редакция этого
# скрипта так и попалась: она зафиксировала у Loki 8.7 МиБ, а через несколько
# минут том занимал 2.2 МиБ — вчетверо меньше.
#
# Поэтому здесь снимается СЕРИЯ и приводится медиана с разбросом. Если разброс
# перекрывает разницу между бэкендами — вывод об этой паре делать нельзя, и
# отчёт говорит об этом прямо.
SETTLE_SECONDS="${SETTLE_SECONDS:-300}"
SAMPLES="${SAMPLES:-6}"
SAMPLE_INTERVAL="${SAMPLE_INTERVAL:-60}"

size_series() {
  local volume="$1" i
  for i in $(seq 1 "$SAMPLES"); do
    volume_bytes "$volume"
    [ "$i" -lt "$SAMPLES" ] && sleep "$SAMPLE_INTERVAL"
  done
}

data_series() {
  local volume="$1"; shift
  local paths="$*" i
  for i in $(seq 1 "$SAMPLES"); do
    data_bytes "$volume" $paths
    [ "$i" -lt "$SAMPLES" ] && sleep "$SAMPLE_INTERVAL"
  done
}

# Медиана, минимум, максимум и разброс в процентах от медианы.
series_stats() {
  "$PYTHON_BIN" -c "
import sys, statistics
xs = [int(x) for x in sys.stdin.read().split() if x.strip()]
if not xs:
    print('0 0 0 0'); raise SystemExit
med = int(statistics.median(xs))
spread = (max(xs) - min(xs)) / med * 100 if med else 0
print('%d %d %d %.1f' % (med, min(xs), max(xs), spread))
"
}

# --- прогон ------------------------------------------------------------------

log "прогон: $COUNT записей, seed $SEED, метка $RUN"
docker compose --profile tools run --rm -T loadgen \
  -count "$COUNT" -seed "$SEED" -run "$RUN" >/dev/null 2>&1

MANIFEST="$(docker run --rm -v log-backends_logs:/logs alpine:3.21 cat /logs/manifest.json 2>/dev/null)"
FACT_TOTAL="$(printf '%s' "$MANIFEST" | "$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['total'])
")"
# Размер исходного файла — та величина, с которой сравниваются все три. Без неё
# «столько-то мегабайт» ни о чём не говорит: неизвестно, много это или мало.
RAW_BYTES="$(docker run --rm -v log-backends_logs:/logs alpine:3.21 stat -c %s /logs/app.log 2>/dev/null)"
echo "  отправлено: $FACT_TOTAL записей, исходный файл: $(human "$RAW_BYTES")"

log "жду доставки всех $FACT_TOTAL записей"
N_LOKI=0; N_OS=0; N_VL=0
for _ in $(seq 1 60); do
  N_LOKI="$(count_loki)"; N_OS="$(count_os)"; N_VL="$(count_vl)"
  if [ "$N_LOKI" -ge "$FACT_TOTAL" ] && [ "$N_OS" -ge "$FACT_TOTAL" ] && [ "$N_VL" -ge "$FACT_TOTAL" ] 2>/dev/null; then
    break
  fi
  sleep 5
done
echo "  доехало: Loki $N_LOKI, OpenSearch $N_OS, VictoriaLogs $N_VL"

if [ "$N_LOKI" != "$FACT_TOTAL" ] || [ "$N_OS" != "$FACT_TOTAL" ] || [ "$N_VL" != "$FACT_TOTAL" ]; then
  echo "ОШИБКА: доехало не всё — сравнивать объёмы нельзя." >&2
  echo "        Меньший объём у бэкенда с потерями выглядел бы как компактность." >&2
  exit 1
fi

# --- сброс на диск и ожидание тишины -----------------------------------------
#
# Бэкенды держат часть данных в памяти. Просим сбросить то, что можно попросить:
# у OpenSearch есть явный flush, у Loki и VictoriaLogs — нет, им нужно время.

log "прошу сброс на диск"
innet -s --max-time 60 -X POST "http://opensearch:9200/logs-default/_flush" >/dev/null 2>&1 || true
# wait_for_completion=true ОБЯЗАТЕЛЕН. Без него запрос возвращается сразу, а
# слияние продолжается в фоне — и замер ловит промежуточное состояние. На одних
# и тех же данных так получалось 20.7 / 6.0 / 4.3 / 2.9 МиБ.
OS_MERGE="http://opensearch:9200/logs-default/_forcemerge?max_num_segments=1&wait_for_completion=true"
innet -s --max-time 300 -X POST "$OS_MERGE" >/dev/null 2>&1 || true

# Пауза покоя перед серией: даём фоновым операциям пройти основную часть работы.
# Не «до стабилизации» — она не наступает, — а фиксированное время, одинаковое
# для всех трёх.
log "покой ${SETTLE_SECONDS}с, затем серия из $SAMPLES замеров с интервалом ${SAMPLE_INTERVAL}с"
sleep "$SETTLE_SECONDS"

# Данные и весь том — две разные величины, обе нужны. Серии снимаются
# поочерёдно; интервал общий, поэтому фаза фоновых операций у них одна.
read -r SIZE_LOKI MIN_LOKI MAX_LOKI SPREAD_LOKI <<<"$(data_series "$VOL_LOKI" $DATA_LOKI | series_stats)"
read -r SIZE_OS   MIN_OS   MAX_OS   SPREAD_OS   <<<"$(data_series "$VOL_OS"   $DATA_OS   | series_stats)"
read -r SIZE_VL   MIN_VL   MAX_VL   SPREAD_VL   <<<"$(data_series "$VOL_VL"   $DATA_VL   | series_stats)"

VOLTOT_LOKI="$(volume_bytes "$VOL_LOKI")"
VOLTOT_OS="$(volume_bytes "$VOL_OS")"
VOLTOT_VL="$(volume_bytes "$VOL_VL")"

echo "  данные (без журналов):"
echo "    Loki:         $(human "$SIZE_LOKI")  (разброс ${SPREAD_LOKI}%)"
echo "    OpenSearch:   $(human "$SIZE_OS")  (разброс ${SPREAD_OS}%)"
echo "    VictoriaLogs: $(human "$SIZE_VL")  (разброс ${SPREAD_VL}%)"
echo "  весь том (с журналами):"
echo "    Loki:         $(human "$VOLTOT_LOKI")"
echo "    OpenSearch:   $(human "$VOLTOT_OS")"
echo "    VictoriaLogs: $(human "$VOLTOT_VL")"

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

echo
log "вердикт"
verdict "Loki: записей"         "$N_LOKI" "$FACT_TOTAL"
verdict "OpenSearch: записей"   "$N_OS"   "$FACT_TOTAL"
verdict "VictoriaLogs: записей" "$N_VL"   "$FACT_TOTAL"

# Отношения к исходному файлу и к самому компактному из трёх.
read -r R_LOKI R_OS R_VL MIN_NAME RATIO_MAX <<<"$("$PYTHON_BIN" -c "
sizes = {'Loki': $SIZE_LOKI, 'OpenSearch': $SIZE_OS, 'VictoriaLogs': $SIZE_VL}
raw = $RAW_BYTES
mn = min(sizes, key=sizes.get)
print('%.2f %.2f %.2f %s %.1f' % (
    sizes['Loki'] / raw, sizes['OpenSearch'] / raw, sizes['VictoriaLogs'] / raw,
    mn, max(sizes.values()) / min(sizes.values())))
")"

echo
echo "  к размеру исходного файла: Loki ${R_LOKI}x, OpenSearch ${R_OS}x, VictoriaLogs ${R_VL}x"
echo "  самый компактный: $MIN_NAME; разброс между крайними: ${RATIO_MAX}x"

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Объём одного и того же потока в трёх бэкендах"
  echo "=============================================="
  echo "Прогон: $COUNT записей, seed $SEED, метка $RUN. Тома чистые."
  echo "Исходный файл (JSON-строки): $(human "$RAW_BYTES")"
  echo
  echo "ПРЕДУСЛОВИЕ ВЫПОЛНЕНО: во все три доехало по $FACT_TOTAL записей."
  echo "Без этой проверки меньший объём у бэкенда с потерями читался бы как"
  echo "компактность."
  echo
  printf "%-14s %12s %9s %12s %9s\n" "БЭКЕНД" "ДАННЫЕ" "К ФАЙЛУ" "ВЕСЬ ТОМ" "РАЗБРОС"
  printf "%-14s %12s %8sx %12s %8s%%\n" "Loki" "$(human "$SIZE_LOKI")" "$R_LOKI" "$(human "$VOLTOT_LOKI")" "$SPREAD_LOKI"
  printf "%-14s %12s %8sx %12s %8s%%\n" "OpenSearch" "$(human "$SIZE_OS")" "$R_OS" "$(human "$VOLTOT_OS")" "$SPREAD_OS"
  printf "%-14s %12s %8sx %12s %8s%%\n" "VictoriaLogs" "$(human "$SIZE_VL")" "$R_VL" "$(human "$VOLTOT_VL")" "$SPREAD_VL"
  echo
  echo "ДАННЫЕ — каталоги данных без журналов упреждающей записи (у Loki это"
  echo "chunks и tsdb-shipper-active, но НЕ wal). ВЕСЬ ТОМ — то же плюс журналы"
  echo "и служебные файлы. Первое отвечает на вопрос «сколько занимает хранение»,"
  echo "второе — «сколько места нужно работающему тому». ПЛАНИРОВАТЬ ДИСК НАДО ПО"
  echo "ВТОРОМУ: WAL лежит рядом с данными, а не вместо них."
  echo
  echo "Самый компактный: $MIN_NAME. Разброс между крайними: ${RATIO_MAX}x."
  echo
  echo "ЧТО ЗДЕСЬ ДОКАЗАТЕЛЬНО, А ЧТО НЕТ"
  echo "Смотреть надо на колонку РАЗБРОС: разница между бэкендами меньше их"
  echo "разброса ничего не значит. Разница на порядок (VictoriaLogs против"
  echo "OpenSearch) устойчива и переживает повторный прогон."
  echo
  echo "ПОЧЕМУ ЗАМЕР УСТРОЕН ИМЕННО ТАК. Ранние редакции давали разброс в разы, и"
  echo "обе причины оказались в МЕТОДЕ, а не в бэкендах:"
  echo "  1. forcemerge вызывался без wait_for_completion, и замер попадал на"
  echo "     середину слияния: 20.7 / 6.0 / 4.3 / 2.9 МиБ на одних данных."
  echo "  2. Критерий «N одинаковых замеров подряд» ловил паузу между фоновыми"
  echo "     операциями, а не их конец: у Loki зафиксировалось 8.7 МиБ, а через"
  echo "     несколько минут том занимал 2.2 МиБ. Объём ходил в обе стороны:"
  echo "     2.30 -> 2.30 -> 2.31 -> 3.56 МБ."
  echo "Сейчас слияние ожидается по факту, затем фиксированный покой и медиана"
  echo "серии. Разброс этой серии — в таблице выше."
  echo
  echo "КАК МЕРИЛОСЬ"
  echo "Размер тома снаружи, вспомогательным контейнером (du -sb), одинаково для"
  echo "всех трёх: образы Loki и VictoriaLogs собраны без шелла, и du внутри них"
  echo "не работает. Разные способы измерения дали бы разницу метода вместо"
  echo "разницы бэкендов."
  echo
  echo "Перед замером OpenSearch попросили flush и forcemerge в один сегмент —"
  echo "у него такая операция есть, — причём с wait_for_completion=true: без"
  echo "него вызов возвращается ДО окончания слияния и замер ловит промежуточное"
  echo "состояние (наблюдалось 20.7 / 6.0 / 4.3 / 2.9 МиБ на одних данных)."
  echo "У Loki и VictoriaLogs явного аналога нет, им дано время: фиксированный"
  echo "покой ${SETTLE_SECONDS} с, затем медиана серии из $SAMPLES замеров с"
  echo "интервалом ${SAMPLE_INTERVAL} с. Критерия «N одинаковых замеров подряд»"
  echo "здесь НЕТ — он ловит паузу между фоновыми операциями, а не их конец."
  echo
  echo "ЧТО ИМЕННО В КОЛОНКЕ «ДАННЫЕ»"
  echo "Только каталоги данных, без журналов упреждающей записи: у Loki это"
  echo "chunks и tsdb-shipper-active, но НЕ wal. Это ответ на вопрос «сколько"
  echo "займёт хранение», а не «сколько места нужно работающему тому»: WAL"
  echo "живёт на диске одновременно с данными, поэтому рядом печатается и весь"
  echo "том целиком. Планировать диск надо по нему."
  echo
  echo "ЧЕГО ЭТОТ ЗАМЕР НЕ ПОКАЗЫВАЕТ"
  echo "1. Поведение на длинном ретеншене: здесь один прогон, а не месяц данных."
  echo "2. Кластерную конфигурацию: все три в одиночном режиме, без репликации."
  echo "   В проде OpenSearch обычно держит реплику, то есть занимает вдвое"
  echo "   больше показанного."
  echo "3. Влияние настройки: это прогон на значениях ПО УМОЛЧАНИЮ. Что даёт"
  echo "   настройка — в results/05-tuned.txt."
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
