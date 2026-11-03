"""Реестр диверсий: каждая проверка обязана краснеть от своей поломки.

Прогон: копия стенда во временном каталоге. Сначала контрольная копия без
диверсий: её набор обязан пройти; иначе «покраснело» у диверсий ничего не
значит. Затем каждая диверсия по одной: применить замену текста, прогнать
набор (без Docker), ожидать провал. Неприменившаяся диверсия — проваленная
проверка, а не пропуск.

    python3 scripts/sabotage.py            → по строке на диверсию, код 0 — все сработали

Пока ставится маркер (STAND_MARKER_PENDING=1), диверсии, нацеленные на сам
маркер, пропускаются и называются отдельно.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (имя, файл относительно стенда, было, стало). «Было» с приставкой re: —
# регулярное выражение, заменяется первое совпадение.
SABOTAGES = [
    ("SDK разошёлся с генератором", "fixtures/traces.jsonl",
     r're:("variant": "sdk-routes".*?"mismatched_routes": \[)\]', r'\g<1>"GET /fail"]'),
    ("Beyla разошлась с генератором", "fixtures/traces.jsonl",
     r're:("variant": "beyla-routes".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("эвристика маршрута дала много имён", "fixtures/traces.jsonl",
     r're:("variant": "beyla-route-names".*?"routes_8081": \[)', r'\g<1>"/v2/users/1", '),
    ("у SQL-спанов Beyla нашёлся родитель", "fixtures/traces.jsonl",
     r're:("variant": "beyla-links".*?"parent_found": )\d+', r"\g<1>500"),
    ("SDK не увидел CREATE при старте", "fixtures/traces.jsonl",
     r're:("variant": "sdk-sql".*?"startup_create_seen": )true', r"\g<1>false"),
    ("контрольный профиль вне допуска", "fixtures/profiles.jsonl",
     r're:("variant": "go126".*?"within_tolerance": )true', r"\g<1>false"),
    ("общая листовая функция не прячет вызывающего", "fixtures/profiles.jsonl",
     r're:("variant": "go126-shared".*?"within_tolerance": )false', r"\g<1>true"),
    ("pprof тоже потерял вызывающих", "fixtures/profiles.jsonl",
     r're:("variant": "pprof-shared".*?"within_tolerance": )true', r"\g<1>false"),
    ("Go 1.27 поддержан профилировщиком", "fixtures/profiles.jsonl",
     r're:("variant": "go127".*?"unsupported_go": )true', r"\g<1>false"),
    ("фон Beyla сравним с нагрузкой", "fixtures/cost.jsonl",
     r're:("variant": "idle".*?"background_below_1pct": )true', r"\g<1>false"),
    ("контроль трейсов снят", "scripts/analyze.py", 'if by["sdk-routes"]["mismatched_routes"]:', "if False:"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("traces.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "observability")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", ".git", "out", "bin"))
    return stand


def run_suite(stand):
    env = dict(os.environ)
    env.pop("STAND_WITH_DOCKER", None)
    proc = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "test_*.py"],
                          cwd=stand, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return proc.returncode, proc.stdout.decode("utf-8", "replace")


def main():
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        rc, out = run_suite(make_copy(os.path.join(tmp, "control")))
        if rc != 0:
            print("КОНТРОЛЬНАЯ КОПИЯ НЕ ПРОШЛА — диверсии не значат ничего\n" + out[-3000:])
            return 1
        print("контрольная копия: зелёная")
        pending = os.environ.get("STAND_MARKER_PENDING") == "1"
        skipped = 0
        for i, (name, rel, old, new) in enumerate(SABOTAGES):
            if pending and rel == "fixtures/RUN-COMPLETE":
                print("пропущена (маркер ставится): %s" % name)
                skipped += 1
                continue
            stand = make_copy(os.path.join(tmp, "s%d" % i))
            path = os.path.join(stand, rel)
            with open(path, encoding="utf-8") as f:
                text = f.read()
            if old.startswith("re:"):
                changed, n = re.subn(old[3:], new, text, count=1)
            else:
                changed, n = text.replace(old, new, 1), text.count(old)
            if n == 0:
                print("НЕ ПРИМЕНИЛАСЬ: %s (%s)" % (name, rel))
                failures += 1
                continue
            with open(path, "w", encoding="utf-8") as f:
                f.write(changed)
            rc, _ = run_suite(stand)
            ok = rc != 0
            failures += 0 if ok else 1
            print("%s: %s" % ("покраснело" if ok else "ОСТАЛОСЬ ЗЕЛЁНЫМ", name))
    print("диверсий: %d, не сработало: %d" % (len(SABOTAGES) - skipped, failures))
    if skipped:
        print("пропущено на время постановки маркера: %d" % skipped)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
