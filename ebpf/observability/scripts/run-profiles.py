"""Ось 2: eBPF-профилирование (Alloy pyroscope.ebpf → Pyroscope) против заданной доли CPU.

Сжигатель (cmd/burn) делает 3 : 1 итераций в двух функциях — эталон 75% / 25%,
допуск ±5 п.п. (expected.json). Варианты — сборки и режимы:
  go126           Go 1.26, с символами, цикл внутри burnSelfA/B (контроль);
  go126-shared    то же, burnA/B вызывают общую листовую work;
  go126-stripped  Go 1.26, -s -w;
  go127           Go 1.27 (профилировщик Alloy 1.20 его не поддерживает);
  go127-stripped  Go 1.27, -s -w;
  pprof-shared    контроль для go126-shared: тот же процесс (Go 1.26, режим shared)
                  одновременно снимает себя runtime/pprof — раскрутка средствами
                  рантайма Go; окно pprof покрывает окно Alloy.

    python3 scripts/run-profiles.py              → все варианты, фикстура
"""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, PROFILES  # noqa: E402

SERVICE = {"go126": "burn-go126-self", "go126-shared": "burn-go126-pprof-shared", "go126-stripped": "burn-go126-stripped-self",
           "go127": "burn-go127-self", "go127-stripped": "burn-go127-stripped-self"}
WANT = {"A": 0.75, "B": 0.25}
TOL = 0.05
WINDOW = 90
STATE = {}

QUERY = r'''
import json, sys, urllib.request, urllib.parse
svc, frm, until = sys.argv[1], sys.argv[2], sys.argv[3]
q = urllib.parse.urlencode({"query": 'process_cpu:cpu:nanoseconds:cpu:nanoseconds{service_name="%s"}' % svc,
                            "from": frm, "until": until, "format": "json"})
fb = json.load(urllib.request.urlopen("http://pyroscope:4040/pyroscope/render?" + q))["flamebearer"]
names, tot, own = fb["names"], {}, {}
for lv in fb["levels"]:
    for i in range(0, len(lv), 4):
        n = names[lv[i + 3]]
        tot[n] = tot.get(n, 0) + lv[i + 1]
        own[n] = own.get(n, 0) + lv[i + 2]
print("RESULT " + json.dumps({"ticks": fb["numTicks"], "total": tot, "self": own}))
'''


def prepare():
    """Профиль compose «profiling» на окно замера, затем остановка: сжигатели занимают CPU."""
    stack.compose("--profile", "profiling", "up", "-d", "--build", "--wait", timeout=1800)
    time.sleep(30)  # профилировщик находит процессы и начинает присылать профили
    STATE["from"] = int(time.time())
    time.sleep(WINDOW)
    STATE["until"] = int(time.time())
    _, logs = stack.compose("logs", "alloy", check=False)
    STATE["unsupported"] = sorted({l.split("Failed to load ")[1].split(" ")[0] for l in logs.split("\n")
                                   if "unsupported Go version" in l})
    for name, svc in SERVICE.items():
        _, out = stack.tools("python3", "-c", QUERY, svc, str(STATE["from"]), str(STATE["until"]))
        STATE[name] = stack.result(out)
    # профиль runtime/pprof того же процесса: процесс печатает его по окончании окна pprof
    for _ in range(60):
        _, logs = stack.compose("logs", "burn-go126-shared", check=False)
        if "RESULT " in logs:
            STATE["pprof"] = stack.result(logs.replace("burn-go126-shared-1  | ", ""))
            break
        time.sleep(2)
    else:
        raise SystemExit("burn-go126-shared не напечатал профиль runtime/pprof")
    stack.compose("--profile", "profiling", "stop", "alloy", "pyroscope", "burn-go126", "burn-go126-shared",
                  "burn-go126-stripped", "burn-go127", "burn-go127-stripped")


def measure_pprof():
    r = STATE["pprof"]
    sa, sb = r["cum_ns"]["main.burnA"] / r["total_ns"], r["cum_ns"]["main.burnB"] / r["total_ns"]
    ok = abs(sa - WANT["A"]) <= TOL and abs(sb - WANT["B"]) <= TOL
    return {"probe": "runtime/pprof of the same process, %d s" % r["seconds"], "go": r["go"],
            "has_samples~": r["total_ns"] > 0, "unsupported_go": False,
            "burn_functions": ["main.burnA", "main.burnB"] if r["cum_ns"]["main.burnA"] and r["cum_ns"]["main.burnB"] else [],
            "share_a~": round(sa, 3), "share_b~": round(sb, 3), "within_tolerance": ok,
            "outcome": "match" if ok else "mismatch",
            "detail": "runtime/pprof того же процесса (%s): burnA %.1f%%, burnB %.1f%% по полному времени (эталон 75 / 25, допуск ±5 п.п.)" % (
                r["go"], 100 * sa, 100 * sb)}


def measure(name):
    if not STATE:
        prepare()
    if name == "pprof-shared":
        return measure_pprof()
    r = STATE[name]
    tot = r["total"]
    a = tot.get("main.burnSelfA", tot.get("main.burnA", 0))
    b = tot.get("main.burnSelfB", tot.get("main.burnB", 0))
    named = sorted(k for k in tot if k.startswith("main."))
    exe = "/burn-" + name.replace("-shared", "")
    # без имён функций сэмплы ядра то есть, то нет — наличие сэмплов и верхняя функция вне сравнения («~»)
    row = {"probe": "%s, window %d s" % (SERVICE[name], WINDOW), "has_samples~": r["ticks"] > 0,
           "ticks~": r["ticks"], "main_functions~": named, "unsupported_go": exe in STATE["unsupported"],
           # main.main — вызывающий листовых burnSelf*: раскрутка на arm64 его обычно теряет, но не всегда.
           # «в профиле» — не меньше 1% CPU: единичные сэмплы в самом кадре burnA/B — шум на краю
           "burn_functions": sorted(n for n in named if "burn" in n and r["ticks"] and tot.get(n, 0) / r["ticks"] >= 0.01)}
    own = r["self"]
    top = max(own, key=own.get) if own else None
    row["top_self" + ("" if row["burn_functions"] or name == "go126-shared" else "~")] = top
    if row["burn_functions"]:
        sa, sb = a / r["ticks"], b / r["ticks"]
        row.update({"share_a~": round(sa, 3), "share_b~": round(sb, 3),
                    "within_tolerance": abs(sa - WANT["A"]) <= TOL and abs(sb - WANT["B"]) <= TOL})
        row["detail"] = "burnA %.1f%%, burnB %.1f%% (эталон 75 / 25, допуск ±5 п.п.)" % (100 * sa, 100 * sb)
    else:
        row["within_tolerance"] = False
        row["detail"] = ("сэмплов нет" if not r["ticks"] else
                         "burnA и burnB в профиле нет (меньше 1%% CPU); больше всего собственного CPU — %s (%.1f%%)" % (
                             top, 100 * own.get(top, 0) / r["ticks"]))
    if row["unsupported_go"]:
        row["detail"] += "; профилировщик: unsupported Go version"
    row["outcome"] = "match" if row["within_tolerance"] else "mismatch"
    return row


if __name__ == "__main__":
    common.run("profiles", measure, list(SERVICE) + ["pprof-shared"], PROFILES, END_MARKER)
