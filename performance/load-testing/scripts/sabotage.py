"""Реестр диверсий стенда performance/load-testing: каждая проверка обязана краснеть от своей поломки.

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
    ("открытый генератор спрятал паузу", "fixtures/model.jsonl",
     r're:("variant": "vegeta".*?"outcome": )"matches"', r'\g<1>"hides"'),
    ("закрытый генератор показал хвост", "fixtures/model.jsonl",
     r're:("variant": "hey".*?"p99_hidden": )true', r"\g<1>false"),
    ("wrk восстановил середину распределения", "fixtures/model.jsonl",
     r're:("variant": "wrk".*?"p90_hidden": )true', r"\g<1>false"),
    ("k6 с 20 VU ничего не потерял", "fixtures/model.jsonl",
     r're:("variant": "k6-open-capped".*?"dropped_any": )true', r"\g<1>false"),
    ("контроль модели не сошёлся", "fixtures/model.jsonl",
     r're:("variant": "control-vegeta".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("SLO не выполнен при 50% ёмкости", "fixtures/types.jsonl",
     r're:("variant": "load".*?"slo_met": )true', r"\g<1>false"),
    ("разгрузка после всплеска не сошлась", "fixtures/types.jsonl",
     r're:("variant": "spike".*?"recovery_within": )true', r"\g<1>false"),
    ("soak не дошёл до OOM", "fixtures/types.jsonl",
     r're:("variant": "soak".*?"oom_killed": )true', r"\g<1>false"),
    ("Gatling удержал 16 000/с", "fixtures/ceiling.jsonl",
     r're:("variant": "gatling-open@16000".*?"holds": )false', r"\g<1>true"),
    ("сервис упёрся раньше генераторов", "fixtures/ceiling.jsonl",
     r're:("variant": "wrk@max".*?"achieved_rate~": )\d+', r"\g<1>9000"),
    ("устойчивый сервис не дал запасных ответов", "fixtures/chaos.jsonl",
     r're:("variant": "down-resilient".*?"fallback_in_range": )true', r"\g<1>false"),
    ("порог не оборвал эксперимент", "fixtures/chaos.jsonl",
     r're:("variant": "down-naive-abort".*?"aborted": )true', r"\g<1>false"),
    ("повтор не скрыл потерю реплики", "fixtures/chaos.jsonl",
     r're:("variant": "kill-replica".*?"errors~": )\d+', r"\g<1>500"),
    ("полная операция уложилась в порог", "fixtures/chaos.jsonl",
     r're:("variant": "kill-replica".*?"held_by_operation": )false', r"\g<1>true"),
    ("HAProxy перенаправил запросы", "fixtures/chaos.jsonl",
     r're:("variant": "kill-replica".*?"no_redispatch": )true', r"\g<1>false"),
    ("устойчивый не держит окно сбоя", "fixtures/chaos.jsonl",
     r're:("variant": "down-resilient".*?"held_in_fault_window": )true', r"\g<1>false"),
    ("худшая секунда в норме", "fixtures/chaos.jsonl",
     r're:("variant": "kill-replica-noretry".*?"worst_second_over": )true', r"\g<1>false"),
    ("занятость не предсказала ёмкость", "fixtures/types.jsonl",
     r're:("variant": "stress".*?"capacity_matches_busy": )true', r"\g<1>false"),
    ("Locust одним процессом занял оба ЦП", "fixtures/ceiling.jsonl",
     r're:("variant": "locust-closed@max\+cpu".*?"cpu_pct_median_load~": )\d+', r"\g<1>199"),
    ("контроль модели снят", "scripts/analyze.py",
     'bad = [v for v, r in by.items() if v.startswith("control-") and r["outcome"] != "match"]', "bad = []"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("model.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "load-testing")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", ".git", "out", "bin", "target"))
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
