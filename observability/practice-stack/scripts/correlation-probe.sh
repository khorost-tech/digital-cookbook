#!/usr/bin/env bash
# Проверка связки трейс <-> лог в ОБЕ стороны.
#
#   ./scripts/correlation-probe.sh
#
# Проверять одну сторону недостаточно. «Из лога есть ссылка на трейс» ещё не
# значит, что по трейсу найдутся логи: связка может работать в одну сторону,
# если, например, trace_id попал в лог, но структура запроса к Loki не позволяет
# по нему искать. Поэтому здесь два независимых перехода:
#
#   1. трейс -> логи: берём trace_id из Tempo, ищем по нему записи в Loki и
#      сверяем, что это записи ТОГО ЖЕ запроса (по order_id);
#   2. лог -> трейс: берём trace_id из записи Loki и запрашиваем трейс в Tempo.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

RUN_ID="corr-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/04-logs-correlation.txt"

log "прогон одного заказа, метка $RUN_ID"
innet -s -o /dev/null --max-time 20 -X POST \
  -H "Content-Type: application/json" -H "X-Load-Run: $RUN_ID" \
  --data '{"sku":"SKU-0001","quantity":2}' http://go-frontend:8080/order

log "ожидание, пока трейс и логи станут доступны"
for _ in $(seq 1 30); do
  count="$(tempo_search_count "{span.load.run=\"$RUN_ID\"}")"
  if [ "$count" -ge 1 ] 2>/dev/null; then
    break
  fi
  sleep 2
done

"$PYTHON_BIN" - "$RUN_ID" "$RESULT_FILE" <<'PY'
import json, subprocess, sys, time

run_id, result_file = sys.argv[1], sys.argv[2]

def innet(args):
    return subprocess.run(
        ["docker", "run", "--rm", "--network", "practice-stack_default",
         "curlimages/curl:latest"] + args,
        capture_output=True, text=True, encoding="utf-8").stdout

now = int(time.time()); start = now - 600
lines = []
def out(s=""):
    print(s)
    lines.append(s)

out(f"Корреляция трейс <-> лог в обе стороны, метка {run_id}")
out("=" * 60)

# --- сторона 1: трейс -> логи ------------------------------------------------
found = json.loads(innet(["-s", "--max-time", "25", "-G", "http://tempo:3200/api/search",
    "--data-urlencode", f'q={{span.load.run="{run_id}"}}',
    "--data-urlencode", f"start={start}", "--data-urlencode", f"end={now+60}",
    "--data-urlencode", "limit=5"]))
traces = found.get("traces") or []
if not traces:
    out("ОТКАЗ: трейс прогона не найден в Tempo")
    open(result_file, "a", encoding="utf-8").write("\n".join(lines) + "\n\n")
    sys.exit(1)

raw_trace_id = traces[0]["traceID"]
# ЛОВУШКА. Tempo отдаёт trace id БЕЗ ведущих нулей: в ответе поиска пришло
# 31 шестнадцатеричный символ вместо 32. В логах же trace_id полный, как его
# определяет спецификация OTLP. Значит переход «из трейса в логи» подстановкой
# значения из API Tempo не сработает у каждого шестнадцатого трейса — ровно у
# тех, чей идентификатор начинается с нуля. Дополняем до 32 символов.
trace_id = raw_trace_id.rjust(32, "0")
out(f"1) трейс -> логи")
out(f"   trace_id из Tempo: {raw_trace_id} ({len(raw_trace_id)} символов)")
if trace_id != raw_trace_id:
    out(f"   после дополнения нулями: {trace_id} ({len(trace_id)})")

# Ищем логи по trace_id. Он лежит в structured metadata, поэтому фильтр по нему
# ставится ПОСЛЕ селектора лейблов — сам селектор остаётся низкой кардинальности.
logs = json.loads(innet(["-s", "--max-time", "25", "-G",
    "http://loki:3100/loki/api/v1/query_range",
    "--data-urlencode", f'query={{service_name=~"go-frontend|java-backend"}} | trace_id="{trace_id}"',
    "--data-urlencode", "limit=50"]))
streams = logs["data"]["result"]
records = [(s["stream"].get("service_name"), v[1]) for s in streams for v in s["values"]]
out(f"   записей в Loki по этому trace_id: {len(records)}")
services_in_logs = sorted({r[0] for r in records})
out(f"   сервисы в найденных записях: {services_in_logs}")
for svc, msg in records[:6]:
    out(f"     {svc:14s} {msg[:90]}")

# Сверка «это тот же запрос»: в записях должен встретиться order_id, и он же
# должен быть в атрибутах спана create_order.
raw = json.loads(innet(["-s", "--max-time", "25", f"http://tempo:3200/api/v2/traces/{trace_id}"]))
span_order_id = None
for rs in raw["trace"]["resourceSpans"]:
    for ss in rs["scopeSpans"]:
        for s in ss["spans"]:
            for a in s.get("attributes", []):
                if a["key"] == "order.id":
                    span_order_id = a["value"]["stringValue"]
out(f"   order.id из атрибутов спана: {span_order_id}")
order_in_logs = span_order_id and any(span_order_id in msg for _, msg in records)
out(f"   тот же order_id встречается в логах: {'да' if order_in_logs else 'НЕТ'}")

# --- сторона 2: лог -> трейс -------------------------------------------------
out()
out("2) лог -> трейс")
sample = json.loads(innet(["-s", "--max-time", "25", "-G",
    "http://loki:3100/loki/api/v1/query_range",
    "--data-urlencode", 'query={service_name="go-frontend"} | trace_id != ""',
    "--data-urlencode", "limit=1"]))
sample_streams = sample["data"]["result"]
log_trace_id = sample_streams[0]["stream"].get("trace_id") if sample_streams else None
out(f"   trace_id, взятый ИЗ ЗАПИСИ лога: {log_trace_id}")

back = innet(["-s", "--max-time", "25", f"http://tempo:3200/api/v2/traces/{log_trace_id}"]) if log_trace_id else ""
back_ok = False
if back.strip().startswith("{"):
    try:
        d = json.loads(back)
        spans = sum(len(ss["spans"]) for rs in d["trace"]["resourceSpans"] for ss in rs["scopeSpans"])
        back_ok = spans > 0
        out(f"   трейс по этому id найден в Tempo: да, спанов {spans}")
    except Exception:
        out("   трейс по этому id НЕ разобрался")
else:
    out("   трейс по этому id в Tempo НЕ найден")

# --- вердикт -----------------------------------------------------------------
out()
ok = (len(records) > 0 and len(services_in_logs) == 2 and order_in_logs and back_ok)
if ok:
    out("ИТОГ: связка работает в обе стороны. По трейсу находятся логи ОБОИХ "
        "сервисов того же запроса, по логу находится трейс.")
else:
    out(f"ИТОГ: ОТКАЗ. записей {len(records)}, сервисов {len(services_in_logs)}, "
        f"order_id совпал: {order_in_logs}, обратный переход: {back_ok}")

open(result_file, "a", encoding="utf-8").write("\n".join(lines) + "\n\n")
sys.exit(0 if ok else 1)
PY
rc=$?
echo
echo "дописано: results/04-logs-correlation.txt"
exit "$rc"
