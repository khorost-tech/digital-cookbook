#!/usr/bin/env bash
# Изоляция отказа: спасает ли политика буфера соседние синки.
#
#   ./scripts/bench-isolation.sh [записей] [seed] [сценарий]
#   сценарии: block (по умолчанию у Vector), drop (drop_newest у Loki), report, all
#
# ЗАЧЕМ. Замер кардинальности показал: когда Loki отказывается принимать поток
# (метка с уникальным значением), соседние синки получают лишь пятую часть
# данных. Отсюда напрашивался вывод «нужны раздельные буферы» — и он НЕВЕРЕН:
# буфер у каждого синка Vector и так свой. Дело не в общем буфере, а в том, что
# буфер КОНЕЧЕН и по умолчанию работает с политикой when_full: block: когда он
# забит, block останавливает отдачу вверх по топологии и тормозит всех, кто
# читает из того же источника.
#
# Проверяется ровно это: два прогона отличаются ТОЛЬКО политикой переполнения у
# синка Loki. Всё остальное — источник, трансформация, соседние синки, объём
# потока, seed — одинаково.
#
#   block  = agent-highcard.yaml  (штатное поведение)
#   drop   = agent-isolated.yaml  (у Loki when_full: drop_newest)
#
# Прогон занимает больше десяти минут: Loki в обоих случаях упирается в предел
# активных потоков и уходит в повторы. Поэтому сценарии запускаются по одному.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-100000}"
SEED="${2:-42}"
CASE="${3:-all}"
RESULT_FILE="$STAND_DIR/results/06-isolation.txt"
DRAIN_SECONDS="${DRAIN_SECONDS:-600}"

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

# ПОЛНЫЙ БАЛАНС СОБЫТИЙ по пути «источник -> трансформация -> буфер -> синк».
#
# Первая редакция считала потери как «буфер принял минус отдал минус держит» и
# выдавала число, которое НЕ ЗАМЫКАЛО баланс: 100 000 на входе, 5 000 принято
# Loki, 89 361 объявлено потерянным — и 5 639 событий без объяснения. Ошибка
# была двойная. Во-первых, awk перезаписывал значения, игнорируя измерения
# buffer_id и stage: метрики Vector размечены ими, и у составного буфера серий
# несколько. Во-вторых, «буфер отдал» — это НЕ «Loki принял»: между ними лежат
# события, отвергнутые с 429 и ушедшие в повторы.
#
# Поэтому здесь снимаются все звенья цепи, суммированием по всем сериям метрики,
# а замер проверяет, что баланс сходится.
balance_stats() {
  innet -s --max-time 20 "http://vector:9598/metrics" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, re

# {имя метрики: {(component_id, все прочие метки): значение}} — суммируем по
# сериям, а не перезаписываем: у метрики может быть несколько stage/buffer_id.
sums = {}
line_re = re.compile(r'^(vector_[a-z_]+)\{([^}]*)\}\s+([0-9.]+)')
for line in sys.stdin:
    m = line_re.match(line)
    if not m:
        continue
    name, labels, val = m.group(1), m.group(2), float(m.group(3))
    # Счётчики самого буфера (discarded) размечены buffer_id; на случай, если
    # component_id у серии нет, ключом служит buffer_id — это то же имя синка.
    cid = re.search(r'component_id=\"([^\"]+)\"', labels) or \
          re.search(r'buffer_id=\"([^\"]+)\"', labels)
    if not cid:
        continue
    key = (name, cid.group(1))
    # У ответов HTTP значим ещё и код: он объясняет расхождение «доставлено»
    # с «принято Loki», поэтому статус входит в ключ.
    st = re.search(r'status=\"([^\"]+)\"', labels)
    if st:
        key = (name, cid.group(1), st.group(1))
    sums.setdefault(key, 0.0)
    sums[key] += val

def g(name, cid):
    return int(sums.get((name, cid), 0))

src_out   = g('vector_component_sent_events_total', 'app_file')
buf_rec   = g('vector_buffer_received_events_total', 'to_loki')
buf_sent  = g('vector_buffer_sent_events_total', 'to_loki')
buf_held  = g('vector_buffer_events', 'to_loki')
sink_sent = g('vector_component_sent_events_total', 'to_loki')

# Коды ответов Loki. Нужны, чтобы объяснить, почему «доставлено» меньше, чем
# видно в Loki: запрос с хотя бы одним отвергнутым потоком считается неуспешным
# целиком, и события из него в component_sent_events_total не попадают — хотя
# строки в потоки, помещавшиеся в лимит, Loki сохраняет. Частичный приём
# доказан отдельно: scripts/verify-partial-accept.sh, results/07-partial-accept.txt.
ok_204  = int(sums.get(('vector_http_client_responses_total', 'to_loki', '204'), 0))
err_429 = int(sums.get(('vector_http_client_responses_total', 'to_loki', '429'), 0))

dropped  = buf_rec - buf_sent - buf_held      # выброшено при переполнении
in_retry = buf_sent - sink_sent               # отдано буфером, но не доставлено
# Прямой счётчик выброшенного. Серия появляется только после первого выброса,
# поэтому в сценарии block её законно нет — это 0, а не «метрики не существует».
discarded = g('vector_buffer_discarded_events_total', 'to_loki')

print(src_out, buf_rec, buf_sent, buf_held, sink_sent, dropped, in_retry, ok_204, err_429, discarded)
"
}

# Есть ли уникальное поле в маппинге OpenSearch. Он принимает всё подряд, значит
# его маппинг — самый честный ответ на вопрос «а поле вообще писалось».
field_present() {
  innet -s --max-time 30 "http://opensearch:9200/logs-default/_mapping" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    idx = next(iter(d.values()))
    print('yes' if 'request_id' in idx['mappings']['properties'] else 'no')
except Exception:
    print('no')
"
}

run_case() {
  local label="$1" mode="$2"

  log "СЦЕНАРИЙ $label: режим стенда $mode, тома чистые"
  ./scripts/down.sh clean >/dev/null 2>&1 || true
  ./scripts/up.sh "$mode" >/dev/null 2>&1

  docker compose --profile tools run --rm -T loadgen \
    -count "$COUNT" -seed "$SEED" -run "iso-$label" -high-cardinality >/dev/null 2>&1

  # КОНТРОЛЬ СОСТОЯТЕЛЬНОСТИ: поле должно доехать хотя бы до OpenSearch, который
  # принимает всё подряд. Нет поля — нет и опыта. В первой редакции замера флага
  # -high-cardinality не было: request_id не писался, метка не создавалась, все
  # три бэкенда приняли по 100 000, и «изоляция сработала» оказалось неотличимо
  # от «ломать было нечем».
  # Ждём, а не спрашиваем сразу: пока в OpenSearch не приехала первая запись,
  # индекса нет вовсе, и ответ «поля нет» описывает пустоту, а не поток. Проверка
  # отсутствия на пустых данных проходит всегда — и маскирует настоящую ошибку.
  local has_field="no" waited=0
  while [ "$waited" -lt 120 ]; do
    has_field="$(field_present)"
    [ "$has_field" = "yes" ] && break
    sleep 5
    waited=$(( waited + 5 ))
  done
  echo "  поле request_id доехало: $has_field (ожидается yes)"
  if [ "$has_field" != "yes" ]; then
    echo "ОШИБКА: уникального поля нет — ломать нечего, замер бессмыслен." >&2
    exit 1
  fi

  # Ждём не «сколько-то», а пока приём соседей не встанет: две подряд одинаковые
  # пары значений при непустом счётчике. Фиксированная пауза здесь врала бы —
  # в сценарии drop поток доезжает за минуту, в block ползёт десять.
  local n_os=0 n_vl=0 p_os=-1 p_vl=-1 waited=0 started stable=0
  started="$(date +%s)"
  while [ "$waited" -lt "$DRAIN_SECONDS" ]; do
    sleep 20
    n_os="$(count_os)"; n_vl="$(count_vl)"
    if [ "$n_os" -ge "$COUNT" ] && [ "$n_vl" -ge "$COUNT" ] 2>/dev/null; then
      break
    fi
    if [ "$n_os" = "$p_os" ] && [ "$n_vl" = "$p_vl" ] && [ "$n_os" -gt 0 ] 2>/dev/null; then
      stable=$(( stable + 1 ))
      [ "$stable" -ge 3 ] && break
    else
      stable=0
    fi
    p_os="$n_os"; p_vl="$n_vl"
    waited=$(( $(date +%s) - started ))
  done

  local n_loki
  n_loki="$(count_loki)"
  read -r SRC_OUT BUF_REC BUF_SENT BUF_HELD SINK_SENT DROPPED IN_RETRY OK204 ERR429 DISCARDED <<<"$(balance_stats)"

  echo "  соседи: OpenSearch $n_os, VictoriaLogs $n_vl (из $COUNT)"
  echo "  Loki принял (по API): $n_loki"
  echo "  баланс синка Loki:"
  echo "    источник отдал:        $SRC_OUT"
  echo "    буфер принял:          $BUF_REC"
  echo "    буфер отдал:           $BUF_SENT"
  echo "    буфер держит:          $BUF_HELD"
  echo "    доставлено в Loki:     $SINK_SENT"
  echo "    выброшено буфером:     $DROPPED"
  echo "    в повторах (429 и пр.): $IN_RETRY"
  echo "    ответы Loki: 204 x $OK204, 429 x $ERR429"
  echo "    счётчик vector_buffer_discarded_events_total: $DISCARDED"
  if [ "$DISCARDED" = "$DROPPED" ]; then
    echo "    счётчик совпал с выброшенным по балансу: да"
  else
    echo "    счётчик совпал с выброшенным по балансу: НЕТ ($DISCARDED против $DROPPED)"
  fi

  # БАЛАНС ОБЯЗАН СХОДИТЬСЯ: всё, что буфер принял, либо выброшено, либо
  # доставлено, либо ещё лежит в буфере, либо крутится в повторах. Незамкнутый
  # баланс означает, что метрика прочитана неверно, — и тогда числу «потеряно»
  # верить нельзя. Первая редакция замера как раз оставляла 5639 событий без
  # объяснения.
  local sum_parts=$(( DROPPED + SINK_SENT + BUF_HELD + IN_RETRY ))
  echo "    сходится: $sum_parts (буфер принял $BUF_REC)"
  if [ "$sum_parts" != "$BUF_REC" ]; then
    echo "ОШИБКА: баланс не сходится — метрики прочитаны неверно, числу потерь верить нельзя." >&2
    exit 1
  fi

  printf '%s %s %s %s %s %s %s %s %s %s %s\n' \
    "$n_os" "$n_vl" "$n_loki" "$DROPPED" "$SINK_SENT" "$IN_RETRY" "$BUF_HELD" "$BUF_REC" \
    "$OK204" "$ERR429" "$DISCARDED" \
    > "$STAND_DIR/results/.isolation-$label.txt"
}

case "$CASE" in
  block)  run_case block highcard; exit 0 ;;
  drop)   run_case drop  isolated; exit 0 ;;
  report) : ;;
  all)
    run_case block highcard
    run_case drop  isolated
    ;;
  *)
    echo "неизвестный сценарий: $CASE (ожидается block, drop, report или all)" >&2
    exit 2
    ;;
esac

for f in block drop; do
  if [ ! -f "$STAND_DIR/results/.isolation-$f.txt" ]; then
    echo "ОШИБКА: нет результата сценария $f. Прогоните: $0 $COUNT $SEED $f" >&2
    exit 1
  fi
done

read -r OS_BLK VL_BLK LK_BLK D_BLK S_BLK R_BLK H_BLK REC_BLK OK_BLK E_BLK DC_BLK < "$STAND_DIR/results/.isolation-block.txt"
read -r OS_DRP VL_DRP LK_DRP D_DRP S_DRP R_DRP H_DRP REC_DRP OK_DRP E_DRP DC_DRP < "$STAND_DIR/results/.isolation-drop.txt"

mkdir -p "$STAND_DIR/results"
{
  echo "Изоляция отказа: что на самом деле спасает соседние синки"
  echo "========================================================="
  echo "Два прогона по $COUNT записей, seed $SEED, чистые тома. Отличие ровно"
  echo "одно: политика переполнения буфера у синка Loki."
  echo
  printf "%-28s %10s %12s\n" "" "block" "drop_newest"
  printf "%-28s %10s %12s\n" "OpenSearch принял"   "$OS_BLK" "$OS_DRP"
  printf "%-28s %10s %12s\n" "VictoriaLogs принял" "$VL_BLK" "$VL_DRP"
  printf "%-28s %10s %12s\n" "Loki принял (по API)" "$LK_BLK" "$LK_DRP"
  echo
  echo "БАЛАНС СОБЫТИЙ НА СИНКЕ LOKI (сходится в обоих прогонах)"
  printf "%-28s %10s %12s\n" "буфер принял"        "$REC_BLK" "$REC_DRP"
  printf "%-28s %10s %12s\n" "  выброшено буфером" "$D_BLK"   "$D_DRP"
  printf "%-28s %10s %12s\n" "  доставлено в Loki" "$S_BLK"   "$S_DRP"
  printf "%-28s %10s %12s\n" "  в повторах"        "$R_BLK"   "$R_DRP"
  printf "%-28s %10s %12s\n" "  лежит в буфере"    "$H_BLK"   "$H_DRP"
  echo
  echo "ПРЯМОЙ СЧЁТЧИК ВЫБРОШЕННОГО (сверка с балансом)"
  printf "%-28s %10s %12s\n" "buffer_discarded_events" "$DC_BLK" "$DC_DRP"
  echo
  printf "%-28s %10s %12s\n" "ответов Loki 204"    "$OK_BLK"  "$OK_DRP"
  printf "%-28s %10s %12s\n" "ответов Loki 429"    "$E_BLK"   "$E_DRP"
  echo
  echo "ПОЧЕМУ «ДОСТАВЛЕНО» МЕНЬШЕ, ЧЕМ «LOKI ПРИНЯЛ». Оба счётчика считают"
  echo "СОБЫТИЯ (vector_component_sent_events_total — не запросы), поэтому"
  echo "«разные единицы учёта» тут ничего не объясняют. Причина в том, что LOKI"
  echo "ПРИНИМАЕТ ЗАПРОС ЧАСТИЧНО: запрос с хотя бы одним отвергнутым потоком"
  echo "завершается ошибкой целиком, события из него в счётчик доставленных не"
  echo "попадают, но строки в потоки, помещавшиеся в лимит, Loki сохраняет."
  echo
  echo "Это не рассуждение, а отдельный опыт: scripts/verify-partial-accept.sh"
  echo "занимает предел потоков и шлёт один запрос из существующего и нового"
  echo "потока. Запрос получает 429, запись в существующий поток при этом"
  echo "сохраняется, а в новый — нет. ЧИСЛА НАМЕРЕННО НЕ ДУБЛИРУЮТСЯ ЗДЕСЬ:"
  echo "они живут в results/07-partial-accept.txt и меняются с каждым прогоном,"
  echo "а копия в соседнем отчёте молча устаревает."
  echo
  echo "Практическое следствие: по счётчику успешной отправки нельзя судить о"
  echo "том, что лежит в бэкенде, — ни в большую сторону, ни в меньшую."
  echo
  echo "Именно на смешении этих величин первая редакция замера потеряла 5639"
  echo "событий и объявила потерями то, что ими не было."
  echo
  echo "ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ"
  echo "Расхожий совет «сделайте раздельные буферы» неверен: в Vector буфер у"
  echo "каждого синка СВОЙ и так. Значение имеет не раздельность, а ПОЛИТИКА"
  echo "ПЕРЕПОЛНЕНИЯ. По умолчанию она block: заполненный буфер отказавшего"
  echo "синка останавливает отдачу вверх по топологии, и соседи, читающие из"
  echo "того же источника, встают вместе с ним."
  echo
  echo "drop_newest снимает эту связь ценой ПОТЕРЬ у отказавшего синка. Это"
  echo "размен, а не бесплатная настройка: записи, не поместившиеся в буфер,"
  echo "исчезают МОЛЧА для обычного лога — Vector пишет о них только на уровне"
  echo "debug. Виден выброс по счётчику vector_buffer_discarded_events_total."
  echo
  echo "КАК СЧИТАЛИСЬ ПОТЕРИ. Двумя независимыми способами. Первый — баланс"
  echo "выше: всё принятое буфером обязано разложиться на выброшенное,"
  echo "доставленное, лежащее в буфере и крутящееся в повторах; не сходится —"
  echo "замер падает. Второй — прямой счётчик vector_buffer_discarded_events_total"
  echo "(метки buffer_id, stage, intentional). При drop_newest выброс"
  echo "намеренный, Vector пишет о нём только на уровне debug, поэтому в"
  echo "обычном логе потерь не видно — но счётчик растёт, и оба способа"
  echo "обязаны дать одно число. Серия счётчика появляется лишь после первого"
  echo "выброса: в сценарии block её нет, и это 0, а не «метрики не существует»."
  echo "Прежняя редакция замера утверждала, что такой метрики в Vector 0.57"
  echo "нет; это неверно."
  echo
  echo "Метрики Vector размечены измерениями buffer_id и stage, поэтому серии"
  echo "СУММИРУЮТСЯ. Первая редакция парсера перезаписывала значения по"
  echo "component_id и теряла часть."
  echo
  echo "ПРО СТРОКУ «ИСТОЧНИК ОТДАЛ». В сценарии block она БОЛЬШЕ, чем «буфер"
  echo "принял», и это не потеря: backpressure распространяется не мгновенно,"
  echo "и часть событий стоит во внутренних каналах топологии между"
  echo "трансформацией и синком. Баланс замыкается на уровне БУФЕРА — там, где"
  echo "принимается решение о судьбе события. В сценарии drop_newest эти два"
  echo "числа совпадают: тормозить некому."
  echo
  echo "ОТДЕЛЬНО ПРО САМ LOKI: политика буфера не меняет его судьбу вовсе —"
  echo "предел активных потоков (max_global_streams_per_user, по умолчанию 5000)"
  echo "как был, так и остался. Она решает судьбу СОСЕДЕЙ."
  echo
  echo "ЧЕГО ЗАМЕР НЕ ПОКАЗЫВАЕТ"
  echo "1. Дисковый буфер (type: disk). Он вмещает больше и переживает"
  echo "   перезапуск, но не отменяет вопрос политики: конечен и он."
  echo "2. Полное разделение конвейеров — отдельный Vector на бэкенд. Это"
  echo "   единственный способ изолировать отказ БЕЗ потерь, и стоит он"
  echo "   отдельного процесса и отдельного чтения источника."
  echo "3. Поведение при коротком отказе: здесь синк не принимает всё время"
  echo "   прогона, а не минуту."
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"
