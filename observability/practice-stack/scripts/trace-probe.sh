#!/usr/bin/env bash
# Сквозная проверка целостности трейса: как распределились по трейсам участки
# ОДНОГО запроса — от входа в Go до записи заказа подписчиком в PostgreSQL.
#
#   ./scripts/trace-probe.sh          # стенд без поломки: ожидается один целый трейс
#   ./scripts/trace-probe.sh http     # поднят с поломкой http: ожидается разрыв на HTTP
#   ./scripts/trace-probe.sh nats     # поднят с поломкой nats: ожидается разрыв на NATS
#
# Смысл в точном составе. «Трейс есть» ничего не значит: при разрыве трейсы тоже
# есть, просто их два вместо одного. И «какого-то участка нет» тоже ничего не
# доказывает — участок пропадает и при потере экспорта, и при задержке доставки,
# и при выключенной инструментации. Разрыв доказан, только если ОБЕ части одного
# запроса найдены, лежат в РАЗНЫХ трейсах и каждая содержит ровно ожидаемые
# участки. Части связываются независимо от trace context:
#   - исходный трейс — по метке прогона в спане create_order (load.run);
#   - серверная часть Java — по заголовку X-Load-Run, который Go передаёт обычным
#     бизнес-заголовком, а агент Java пишет в http.request.header.x-load-run;
#   - часть подписчика NATS — по order_id из ответа: Java пишет в лог
#     «заказ сохранён order_id=…» вместе со своим trace_id, лог лежит в Loki.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

EXPECT="${1:-whole}"
case "$EXPECT" in
  whole|http|nats) ;;
  *) echo "режим: whole, http или nats (получено: $EXPECT)" >&2; exit 2 ;;
esac
RUN_ID="probe-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/03-tracing.txt"
# Начало окна поиска — момент перед запросом. order_id вида ord-SKU-0004-<n>
# уникален только в пределах жизни процесса go-frontend: счётчик сбрасывается
# при рестарте, и в Loki лежат записи с тем же order_id от прошлых подъёмов.
T0="$(( $(date +%s) - 2 ))"

log "прогон одного запроса по медленному пути, метка $RUN_ID"
# Один запрос по SKU-0004: у него самый длинный путь и заведомо есть спан БД.
# Ответ нужен целиком: order_id из него связывает запрос с записью подписчика.
RESPONSE="$(innet -s --max-time 20 -X POST \
  -H "Content-Type: application/json" -H "X-Load-Run: $RUN_ID" \
  --data '{"sku":"SKU-0004","quantity":1}' http://go-frontend:8080/order)"
ORDER_ID="$("$PYTHON_BIN" -c 'import json,sys; print(json.loads(sys.argv[1])["order_id"])' "$RESPONSE" 2>/dev/null || true)"
if [ -z "$ORDER_ID" ]; then
  echo "ОШИБКА: в ответе нет order_id: $RESPONSE" >&2
  exit 1
fi
log "order_id из ответа: $ORDER_ID"

log "разбор трейсов"
"$PYTHON_BIN" - "$RUN_ID" "$EXPECT" "$RESULT_FILE" "$ORDER_ID" "$T0" <<'PY'
import json, subprocess, sys, time

run_id, expect, result_file, order_id, t0 = sys.argv[1:6]

def innet(args):
    return subprocess.run(
        ["docker", "run", "--rm", "--network", "practice-stack_default",
         "curlimages/curl:latest"] + args,
        capture_output=True, text=True, encoding="utf-8").stdout

def jload(text):
    # Пустой или не-JSON ответ (Tempo/Loki ещё не готовы, разовая ошибка) —
    # это «пока не найдено», а не падение: retry ниже спросит ещё раз.
    try:
        return json.loads(text or "{}")
    except json.JSONDecodeError:
        return {}

def norm(tid):
    # Tempo отдаёт traceID без ведущих нулей, логи — полными 32 символами.
    return (tid or "").lower().lstrip("0")

# Окно — с момента запроса: всё, что раньше, к этому запросу отношения не имеет.
start = int(t0)

def tempo_search(q, limit=50):
    found = jload(innet(["-s", "--max-time", "25", "-G", "http://tempo:3200/api/search",
        "--data-urlencode", f"q={q}",
        "--data-urlencode", f"start={start}", "--data-urlencode", f"end={int(time.time())+60}",
        "--data-urlencode", f"limit={limit}"]))
    return [t["traceID"] for t in (found.get("traces") or [])]

def fetch(tid):
    raw = jload(innet(["-s", "--max-time", "25", f"http://tempo:3200/api/v2/traces/{tid}"]))
    spans = []
    for rs in (raw.get("trace") or {}).get("resourceSpans", []):
        svc = [a["value"]["stringValue"] for a in rs["resource"]["attributes"]
               if a["key"] == "service.name"][0]
        for ss in rs["scopeSpans"]:
            for sp in ss["spans"]:
                dur = (int(sp["endTimeUnixNano"]) - int(sp["startTimeUnixNano"])) / 1e6
                attrs = {}
                for a in sp.get("attributes", []):
                    v = a["value"]
                    if "arrayValue" in v:
                        attrs[a["key"]] = [x.get("stringValue") for x in v["arrayValue"].get("values", [])]
                    else:
                        attrs[a["key"]] = next(iter(v.values()), None)
                spans.append({"svc": svc, "name": sp["name"], "kind": sp.get("kind", ""),
                              "dur": round(dur, 1), "attrs": attrs})
    return spans

def retry(fn, ok, tries=30, pause=2):
    for _ in range(tries):
        v = fn()
        if ok(v):
            return v
        time.sleep(pause)
    return v

# 1. Исходный трейс: метку прогона несёт спан create_order у Go.
origin = retry(lambda: tempo_search(f'{{span.load.run="{run_id}"}}', 20), lambda v: len(v) >= 1)

# 2. Серверная часть Java: трейсы java-backend за окно, у которых в спане есть
#    заголовок с меткой прогона. Фильтр по атрибуту делается здесь, а не в
#    TraceQL: значение заголовка агент пишет массивом строк.
def java_http():
    hits = []
    for tid in tempo_search('{resource.service.name="java-backend" && name=~"GET /inventory.*"}'):
        for sp in fetch(tid):
            if run_id in (sp["attrs"].get("http.request.header.x-load-run") or []):
                hits.append(tid)
                break
    return hits
http_part = retry(java_http, lambda v: len(v) >= 1)

# 3. Часть подписчика: запись Java в Loki с этим order_id и её trace_id.
def consumer_logs():
    raw = jload(innet(["-s", "--max-time", "25", "-G",
        "http://loki:3100/loki/api/v1/query_range",
        # Точное совпадение: подстрока order_id=ord-SKU-0004-1 нашла бы и -10, -11.
        "--data-urlencode", f'query={{service_name="java-backend"}} |~ "order_id={order_id}( |$)"',
        "--data-urlencode", f"start={start}000000000",
        "--data-urlencode", f"end={int(time.time())+60}000000000",
        "--data-urlencode", "limit=20"]))
    return sorted({s["stream"].get("trace_id", "") for s in (raw.get("data") or {}).get("result", [])})
consumer_part = retry(consumer_logs, lambda v: len(v) >= 1)

lines = []
def out(s=""):
    print(s)
    lines.append(s)

PARTS = {
    "вход go-frontend":        lambda n: n.startswith("POST /order"),
    "бизнес-спан create_order": lambda n: n == "create_order",
    "исходящий HTTP-вызов":     lambda n: n.startswith("HTTP "),
    "публикация в NATS":       lambda n: "publish" in n,
    "серверный спан java":     lambda n: "/inventory/" in n,
    "запрос SELECT":           lambda n: n.startswith("SELECT"),
    "обработка события":       lambda n: n.endswith("process"),
    "запись INSERT":           lambda n: n.startswith("INSERT"),
}
ALL = set(PARTS)
GO_SIDE = {"вход go-frontend", "бизнес-спан create_order", "исходящий HTTP-вызов", "публикация в NATS"}
HTTP_SIDE = {"серверный спан java", "запрос SELECT"}
NATS_SIDE = {"обработка события", "запись INSERT"}

def parts_of(spans):
    names = [sp["name"] for sp in spans]
    return {p for p, f in PARTS.items() if any(f(n) for n in names)}

def show(title, tid, spans):
    out()
    out(f"{title}: трейс {tid}, спанов {len(spans)}, сервисов {len({sp['svc'] for sp in spans})}")
    for sp in sorted(spans, key=lambda x: -x["dur"]):
        out(f"  {sp['svc']:14s} {sp['name'][:34]:34s} {sp['kind'][:20]:20s} {sp['dur']:8.1f} мс")

out(f"Проверка целостности трейса, режим {expect}, метка {run_id}, order_id {order_id}")
out("=" * 72)
out(f"исходных трейсов (по load.run):            {len(origin)}")
out(f"трейсов серверной части java (X-Load-Run): {len(http_part)}")
out(f"trace_id подписчика из Loki (по order_id): {len(consumer_part)}")

problems = []
if len(origin) != 1:
    problems.append(f"исходных трейсов {len(origin)}, ожидался 1")
if len(http_part) != 1:
    problems.append(f"трейсов серверной части java {len(http_part)}, ожидался 1")
if len(consumer_part) != 1 or not consumer_part[0]:
    problems.append(f"записей подписчика с trace_id: {consumer_part}, ожидалась 1")

if not problems:
    a, h, c = origin[0], http_part[0], consumer_part[0]
    spans_a = fetch(a)
    show("исходный", a, spans_a)
    pa = parts_of(spans_a)
    same_h, same_c = norm(h) == norm(a), norm(c) == norm(a)
    spans_h = spans_a if same_h else fetch(h)
    spans_c = spans_a if same_c else fetch(c)
    if not same_h:
        show("серверная часть java", h, spans_h)
    if not same_c:
        show("подписчик NATS", c, spans_c)
    ph, pc = parts_of(spans_h), parts_of(spans_c)

    # Ожидаемая раскладка по режимам: (часть java в исходном трейсе?, подписчик
    # в исходном трейсе?, участки исходного трейса). При поломке http у Go нет
    # и клиентского спана — клиент без обёртки otelhttp не создаёт его вовсе.
    plan = {
        "whole": (True,  True,  ALL),
        "http":  (False, True,  ALL - HTTP_SIDE - {"исходящий HTTP-вызов"}),
        "nats":  (True,  False, ALL - NATS_SIDE),
    }[expect]
    want_h_same, want_c_same, want_a = plan

    out()
    out("РАСКЛАДКА")
    out(f"  серверная часть java в исходном трейсе: {'да' if same_h else 'нет'} (ожидается {'да' if want_h_same else 'нет'})")
    out(f"  подписчик NATS в исходном трейсе:       {'да' if same_c else 'нет'} (ожидается {'да' if want_c_same else 'нет'})")
    out(f"  участки исходного трейса: {sorted(pa)}")
    out(f"  ожидались ровно:          {sorted(want_a)}")
    if same_h != want_h_same:
        problems.append("серверная часть java не там, где ожидалась")
    if same_c != want_c_same:
        problems.append("подписчик NATS не там, где ожидался")
    if pa != want_a:
        problems.append(f"состав исходного трейса: лишние {sorted(pa - want_a)}, недостающие {sorted(want_a - pa)}")
    if not same_h and ph != HTTP_SIDE:
        problems.append(f"отдельный трейс java содержит {sorted(ph)}, ожидалось ровно {sorted(HTTP_SIDE)}")
    if not same_c and pc != NATS_SIDE:
        problems.append(f"отдельный трейс подписчика содержит {sorted(pc)}, ожидалось ровно {sorted(NATS_SIDE)}")

out()
if not problems:
    msg = {
        "whole": "трейс целый — все участки запроса в одном трейсе",
        "http":  "разрыв на HTTP-границе доказан — часть java найдена по X-Load-Run в отдельном трейсе с ровно ожидаемыми участками",
        "nats":  "разрыв на NATS доказан — подписчик найден по order_id в отдельном трейсе с ровно ожидаемыми участками",
    }[expect]
    out(f"ИТОГ: {msg}.")
else:
    out("ИТОГ: ОТКАЗ — " + "; ".join(problems))

open(result_file, "a", encoding="utf-8").write("\n".join(lines) + "\n\n")
sys.exit(0 if not problems else 1)
PY
rc=$?

echo
echo "дописано: results/03-tracing.txt"
exit "$rc"
