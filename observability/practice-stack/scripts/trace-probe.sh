#!/usr/bin/env bash
# Сквозная проверка целостности трейса: содержит ли трейс ОДНОГО запроса все
# ожидаемые участки — от входа в Go до записи заказа подписчиком в PostgreSQL.
#
#   ./scripts/trace-probe.sh            # ожидается целый трейс
#   ./scripts/trace-probe.sh broken     # ожидается разрыв (стенд поднят с поломкой)
#
# Смысл в точном числе. «Трейс есть» ничего не значит: при разрыве трейсы тоже
# есть, просто их два вместо одного, и на глаз в интерфейсе это выглядит почти
# так же. Поэтому здесь считаются спаны и сервисы, а не факт наличия данных.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

EXPECT="${1:-whole}"
RUN_ID="probe-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/03-tracing.txt"

log "прогон одного запроса по медленному пути, метка $RUN_ID"
# Один запрос по SKU-0004: у него самый длинный путь и заведомо есть спан БД.
innet -s -o /dev/null --max-time 20 -X POST \
  -H "Content-Type: application/json" -H "X-Load-Run: $RUN_ID" \
  --data '{"sku":"SKU-0004","quantity":1}' http://go-frontend:8080/order

log "ожидание, пока трейс станет доступен поиску"
# Дожидаемся появления, а не спим наугад: Tempo отдаёт свежие данные через
# live-store, но с задержкой закрытия блока.
found=0
for _ in $(seq 1 30); do
  count="$(tempo_search_count "{span.load.run=\"$RUN_ID\"}")"
  if [ "$count" -ge 1 ] 2>/dev/null; then
    found=1
    break
  fi
  sleep 2
done
if [ "$found" != "1" ]; then
  echo "ОШИБКА: трейс с меткой $RUN_ID не появился в Tempo" >&2
  exit 1
fi

log "разбор трейсов"
"$PYTHON_BIN" - "$RUN_ID" "$EXPECT" "$RESULT_FILE" <<'PY'
import json, subprocess, sys, time

run_id, expect, result_file = sys.argv[1], sys.argv[2], sys.argv[3]

def innet(args):
    return subprocess.run(
        ["docker", "run", "--rm", "--network", "practice-stack_default",
         "curlimages/curl:latest"] + args,
        capture_output=True, text=True, encoding="utf-8").stdout

now = int(time.time()); start = now - 600

# Все трейсы этого прогона. Их число — и есть проверяемая величина: целый путь
# даёт ОДИН трейс, разорванный — больше одного.
found = json.loads(innet(["-s", "--max-time", "25", "-G", "http://tempo:3200/api/search",
    "--data-urlencode", f'q={{span.load.run="{run_id}"}}',
    "--data-urlencode", f"start={start}", "--data-urlencode", f"end={now+60}",
    "--data-urlencode", "limit=20"]))
traces = found.get("traces") or []

# Асинхронная ветка может уехать в отдельный трейс, у которого метки прогона нет
# вовсе (метку несёт заголовок HTTP-запроса, а не сообщение). Поэтому отдельно
# ищем спаны подписчика за то же окно.
consumer = json.loads(innet(["-s", "--max-time", "25", "-G", "http://tempo:3200/api/search",
    "--data-urlencode", 'q={name="orders.created process"}',
    "--data-urlencode", f"start={start}", "--data-urlencode", f"end={now+60}",
    "--data-urlencode", "limit=20"]))
consumer_traces = consumer.get("traces") or []

lines = []
def out(s=""):
    print(s)
    lines.append(s)

out(f"Проверка целостности трейса, метка {run_id}")
out("=" * 60)
out(f"трейсов с меткой прогона: {len(traces)}")

all_spans = []
for tr in traces:
    tid = tr["traceID"]
    raw = json.loads(innet(["-s", "--max-time", "25", f"http://tempo:3200/api/v2/traces/{tid}"]))
    spans = []
    for rs in raw["trace"]["resourceSpans"]:
        svc = [a["value"]["stringValue"] for a in rs["resource"]["attributes"]
               if a["key"] == "service.name"][0]
        for ss in rs["scopeSpans"]:
            for s in ss["spans"]:
                dur = (int(s["endTimeUnixNano"]) - int(s["startTimeUnixNano"])) / 1e6
                spans.append((svc, s["name"], s.get("kind", ""), round(dur, 1)))
    all_spans.extend(spans)
    out()
    out(f"трейс {tid}: спанов {len(spans)}, сервисов {len({s[0] for s in spans})}")
    for svc, name, kind, dur in sorted(spans, key=lambda x: -x[3]):
        out(f"  {svc:14s} {name[:34]:34s} {kind[:20]:20s} {dur:8.1f} мс")

services = {s[0] for s in all_spans}
names = {s[1] for s in all_spans}

# Ожидаемый состав целого трейса: вход, бизнес-операция, исходящий вызов,
# публикация события у Go; серверный спан, SELECT, обработка события и INSERT у Java.
expected_parts = {
    "вход go-frontend": any(n.startswith("POST /order") for n in names),
    "бизнес-спан create_order": "create_order" in names,
    "исходящий HTTP-вызов": any(n.startswith("HTTP ") for n in names),
    "публикация в NATS": any("publish" in n for n in names),
    "серверный спан java": any("/inventory/" in n for n in names),
    "запрос SELECT": any(n.startswith("SELECT") for n in names),
    "обработка события": any("process" in n for n in names),
    "запись INSERT": any(n.startswith("INSERT") for n in names),
}

out()
out("ОЖИДАЕМЫЕ УЧАСТКИ")
for part, ok in expected_parts.items():
    out(f"  {'есть  ' if ok else 'НЕТ   '} {part}")

out()
out(f"сервисов в одном трейсе: {sorted(services)}")
out(f"трейсов со спаном 'orders.created process' за окно: {len(consumer_traces)}")

missing = [p for p, ok in expected_parts.items() if not ok]
verdict_ok = None

if expect == "whole":
    # Целый путь: ровно один трейс с меткой, оба сервиса, все участки на месте.
    verdict_ok = (len(traces) == 1 and len(services) == 2 and not missing)
    out()
    if verdict_ok:
        out(f"ИТОГ: трейс целый — 1 трейс, {len(all_spans)} спанов, 2 сервиса, все участки на месте.")
    else:
        out(f"ИТОГ: ОТКАЗ. Трейсов {len(traces)}, сервисов {len(services)}, "
            f"нет участков: {missing or 'нет'}")
else:
    # Ожидается разрыв: участки распались по разным трейсам.
    verdict_ok = (len(traces) > 1 or len(services) < 2 or bool(missing))
    out()
    if verdict_ok:
        out(f"ИТОГ: разрыв воспроизведён. Трейсов с меткой {len(traces)}, "
            f"сервисов в них {len(services)}, недостающие участки: {missing or 'нет'}")
    else:
        out("ИТОГ: ОТКАЗ — разрыв НЕ воспроизвёлся, трейс оказался целым.")

open(result_file, "a", encoding="utf-8").write("\n".join(lines) + "\n\n")
sys.exit(0 if verdict_ok else 1)
PY
rc=$?

echo
echo "дописано: results/03-tracing.txt"
exit "$rc"
