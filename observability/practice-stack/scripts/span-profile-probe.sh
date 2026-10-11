#!/usr/bin/env bash
# Проба Span Profiles: можно ли получить профиль КОНКРЕТНОГО запроса.
#
#   ./scripts/up.sh all otel none go     # стенд с профилированием Go
#   ./scripts/span-profile-probe.sh [N]  # N запросов, по умолчанию 3000
#
# Почему отдельная проба. Среди меток СЕРИЙ Pyroscope span_id нет — и из этого
# легко (и ошибочно) заключить, что профиль одного запроса недоступен. Но
# otel-profiling-go ставит span_id pprof-меткой на СЭМПЛЫ, а корневому спану
# пишет атрибут pyroscope.profile.id; Pyroscope хранит span_id на уровне
# сэмплов и отдаёт профиль по нему через SelectMergeSpanProfile. Проба
# проверяет этот путь напрямую:
#   1) N запросов с меткой прогона;
#   2) из Tempo — значения pyroscope.profile.id корневых спанов этого прогона;
#   3) профиль каждого спана из Pyroscope; сколько непустых, что в стеке;
#   4) контроль: несуществующий span_id обязан дать пустой профиль, иначе
#      «непустой» ничего не доказывает.
# CPU одного запроса на стороне Go мал (основное время — ожидание бэкенда):
# при 100 Гц один сэмпл — это 10 мс, и большинство коротких запросов законно
# не получают ни одного. Поэтому проба проверяет два уровня:
#   - профиль по НАБОРУ span_id прогона — обязан быть непустым и содержать
#     функции обработки заказа;
#   - профили ОТДЕЛЬНЫХ спанов (выборка) — считается доля непустых.
# Окно запроса к Pyroscope шире окна прогона на 60 с с каждой стороны:
# pyroscope-go отправляет профиль кусками по 10–15 с с одной меткой времени, и
# короткое окно прогона (3000 запросов укладываются в секунды) может не
# содержать ни одной такой метки — запрос вернёт пусто при исправном пути.
# Так ошибались первые версии пробы. Отбор по span_id не даёт широкому окну
# захватить чужие запросы.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

N="${1:-3000}"
RUN_ID="spanprof-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/09-span-profiles.txt"

"$PYTHON_BIN" - "$N" "$RUN_ID" "$RESULT_FILE" <<'PY'
import json, sys, time, urllib.request, urllib.parse
from concurrent.futures import ThreadPoolExecutor

n, run_id, result_file = int(sys.argv[1]), sys.argv[2], sys.argv[3]
GO, TEMPO, PYRO = "http://127.0.0.1:9310", "http://127.0.0.1:9302", "http://127.0.0.1:9314"
CPU_TYPE = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
SAMPLE = 400  # сколько отдельных спанов проверять поштучно

lines = []
def out(s=""):
    print(s); lines.append(s)

def post_json(url, obj, headers=None):
    req = urllib.request.Request(url, data=json.dumps(obj).encode(), method="POST",
                                 headers={"Content-Type": "application/json", **(headers or {})})
    with urllib.request.urlopen(req, timeout=120) as r:
        return r.status, r.read()

def pyro(method, body):
    _, raw = post_json(f"{PYRO}/querier.v1.QuerierService/{method}", body)
    d = json.loads(raw)
    if "code" in d:
        raise SystemExit(f"ОШИБКА Pyroscope: {d}")
    fg = d.get("flamegraph") or {}
    return int(fg.get("total") or 0), fg.get("names") or []

t0 = int(time.time()) - 2
def one(_):
    try:
        return post_json(f"{GO}/order", {"sku": "SKU-0001", "quantity": 1}, {"X-Load-Run": run_id})[0]
    except Exception:
        return 0
with ThreadPoolExecutor(16) as ex:
    codes = list(ex.map(one, range(n)))
out(f"Проба Span Profiles, метка {run_id}: запросов {n}, успешных {codes.count(201)}")
out("=" * 72)

# Ждём, пока профили окна доедут: pyroscope-go отправляет пачками, сервер
# публикует их с задержкой. Критерий — CPU в расширенном окне перестал расти,
# но не раньше 90 с: на первых проверках «не растёт» значит «ещё не пришло».
time.sleep(90)
PAD = 60
t_end = int(time.time()) + 2
def window_total():
    return pyro("SelectMergeStacktraces", {"profile_typeID": CPU_TYPE,
        "label_selector": '{service_name="go-frontend"}', "start": (t0 - PAD) * 1000,
        "end": (t_end + PAD) * 1000, "max_nodes": 100})[0]
prev, stable = -1, 0
for _ in range(30):
    cur = window_total()
    stable = stable + 1 if cur == prev and cur > 0 else 0
    prev = cur
    if stable >= 3:
        break
    time.sleep(20)
t1 = t_end
out(f"CPU go-frontend в окне прогона (±{PAD} с): {prev / 1e9:.2f} с")

q = '{span.pyroscope.profile.id != "" && resource.service.name="go-frontend"} | select(span.pyroscope.profile.id)'
params = urllib.parse.urlencode({"q": q, "start": t0, "end": t1, "limit": n + 500, "spss": 3})
with urllib.request.urlopen(f"{TEMPO}/api/search?{params}", timeout=120) as r:
    found = json.load(r)
ids = set()
for tr in found.get("traces") or []:
    for ss in tr.get("spanSets") or []:
        for sp in ss.get("spans") or []:
            for a in sp.get("attributes") or []:
                if a["key"] == "pyroscope.profile.id":
                    ids.add(a["value"].get("stringValue"))
ids = sorted(filter(None, ids))
out(f"корневых спанов с pyroscope.profile.id в окне: {len(ids)}")

base = {"profile_typeID": CPU_TYPE, "label_selector": '{service_name="go-frontend"}',
        "start": (t0 - PAD) * 1000, "end": (t1 + PAD) * 1000, "max_nodes": 8000}
set_total, set_names = pyro("SelectMergeSpanProfile", dict(base, span_selector=ids)) if ids else (0, [])
own = sorted({x for x in set_names if x.startswith("main.")})
out(f"профиль по набору span_id: {set_total / 1e9:.2f} с CPU, фреймов {len(set_names)}; функции стенда:")
for nm in own:
    out(f"  {nm}")

per = []
for sid in ids[:SAMPLE]:
    tot, names = pyro("SelectMergeSpanProfile", dict(base, span_selector=[sid]))
    if tot:
        per.append((sid, tot, names))
out(f"отдельных спанов с непустым профилем: {len(per)} из {min(SAMPLE, len(ids))}")
if per:
    sid, tot, names = max(per, key=lambda p: (p[1], len(p[2])))
    out(f"  пример: span_id {sid}, {tot / 1e6:.0f} мс CPU, функции: {sorted({x for x in names if x.startswith('main.')})}")

ctl, _ = pyro("SelectMergeSpanProfile", dict(base, span_selector=["0000000000000000"]))
out(f"контроль, несуществующий span_id: {ctl}")

verdict = bool(ids) and set_total > 0 and "main.(*Service).CreateOrder" in own and ctl == 0
out()
out("ИТОГ: " + ("профиль по span_id доступен: по набору запросов — с функциями обработки заказа; "
                 f"у отдельного короткого запроса — только если в него попал сэмпл ({len(per)} из {min(SAMPLE, len(ids))}); контроль пуст"
                 if verdict else "ОТКАЗ — путь Span Profiles не подтвердился"))
open(result_file, "a", encoding="utf-8").write("\n".join(lines) + "\n\n")
sys.exit(0 if verdict else 1)
PY
