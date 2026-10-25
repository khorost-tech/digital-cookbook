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
    # ось 1
    ("ETL на v1 не совпал", "fixtures/elt.jsonl", r're:("variant": "etl/initial".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("ELT ходил в источник при пересчёте", "fixtures/elt.jsonl",
     r're:("variant": "elt/rule-change".*?"source_queries": )0', r"\g<1>28"),
    ("счёт дней не сходится", "scripts/analyze-elt.py", 'if r["matched_days"] + len(r["mismatch"]) != r["days"]:', "if False:"),
    # ось 2
    ("append без дублей", "fixtures/idempotency.jsonl",
     r're:("variant": "append/rerun".*?"duplicates": )\d+', r"\g<1>0"),
    ("транзакция потеряла строки при падении", "fixtures/idempotency.jsonl",
     r're:("variant": "delete-insert/crash-reload".*?"after_crash": \{.*?"missing": )0', r"\g<1>5"),
    ("падение не случилось", "fixtures/idempotency.jsonl",
     r're:("variant": "merge/crash-reload".*?"runs": \["success", )"failed"', r'\g<1>"success"'),
    # ось 3
    ("по часам взяты все дни", "fixtures/interval.jsonl", r're:("variant": "now".*?"distinct_picked": )1', r"\g<1>14"),
    ("@daily Airflow 3 дал прошлые сутки", "fixtures/interval.jsonl",
     r're:("variant": "scheduled-trigger".*?"day_offset": )0', r"\g<1>-1"),
    ("ручной запуск взял логическую дату", "fixtures/interval.jsonl",
     r're:("variant": "manual-trigger".*?"picked_logical": )false', r"\g<1>true"),
    # ось 4
    ("повтор backfill без флага создал запуски", "fixtures/backfill.jsonl",
     r're:("variant": "repeat-none".*?"runs": )0', r"\g<1>14"),
    ("depends_on_past не остановил следующие дни", "fixtures/backfill.jsonl",
     r're:("variant": "fail-ordered".*?"not_run": )9', r"\g<1>0"),
    ("чистый диапазон принят", "fixtures/backfill.jsonl",
     r're:("variant": "ordered-clean".*?"cli_rc": )\d+', r"\g<1>0"),
    ("ETL с фактами пошёл в источник", "fixtures/elt.jsonl",
     r're:("variant": "etl-facts/rule-change".*?"source_queries": )0', r"\g<1>28"),
    ("отказ CLI не проверяется", "scripts/analyze-backfill.py",
     'if (r["cli_rc"] != 0) != (r["variant"] in REFUSED):', "if False:"),
    # ось 5
    ("окна в 1 день хватило", "fixtures/late.jsonl",
     r're:("variant": "event-lookback1".*?"orders_missing": )20', r"\g<1>0"),
    ("инкрементальная витрина нашла поздние без окна", "fixtures/late.jsonl",
     r're:("variant": "ingest-inc".*?"orders_missing": )20', r"\g<1>0"),
    # ось 6
    ("повторы сделал раннер", "scripts/analyze-retries.py",
     'if r["try_number"] != min(r["fail_times"] + 1, r["retries"] + 1):', "if False:"),
    ("повтор append без дублей", "fixtures/retries.jsonl",
     r're:("variant": "r3-append-k1".*?"duplicates": )250', r"\g<1>0"),
    # сводки и маркер
    ("сводка расходится с фикстурами", "fixtures/late.jsonl",
     r're:("variant": "event".*?"revenue_missing": )200000', r"\g<1>1"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("elt.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "pipelines")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", ".git"))
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
