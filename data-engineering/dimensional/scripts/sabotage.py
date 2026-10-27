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
    ("SCD2 приписал не тому региону", "fixtures/scd.jsonl",
     r're:("variant": "asof-scd2".*?"misattributed": )0', r"\g<1>1"),
    ("SCD1 не исказил историю", "fixtures/scd.jsonl",
     r're:("variant": "asof-scd1".*?"misattributed": )\d+', r"\g<1>0"),
    ("по заказам ошибка не больше итогов", "fixtures/scd.jsonl",
     r're:("variant": "asof-scd1".*?"wrong_amount": )\d+', r"\g<1>1"),
    ("SCD3 помнит два шага", "fixtures/scd.jsonl",
     r're:("variant": "first-scd3".*?"wrong_twice": )10', r"\g<1>0"),
    ("бизнес-ключ разделил владельцев id 77", "fixtures/scd.jsonl",
     r're:("variant": "current-scd2".*?"misattributed": )\d+', r"\g<1>0"),
    ("факт заказа без двойного счёта", "fixtures/grain.jsonl",
     r're:("variant": "category-order".*?"ratio": )[\d.]+', r"\g<1>1.0"),
    ("сумма остатков по дням — норма", "fixtures/additivity.jsonl",
     r're:("variant": "stock-sum".*?"ratio": )[\d.]+', r"\g<1>1.0"),
    ("внутреннее соединение ничего не потеряло", "fixtures/keys.jsonl",
     r're:("variant": "late-inner".*?"missing": )\d+', r"\g<1>0"),
    ("SCD1 не приписал чужие заказы", "fixtures/keys.jsonl",
     r're:("variant": "recycled-scd1".*?"got_orders": )\d+', r"\g<1>8"),
    ("контроль не проверяется", "scripts/analyze.py",
     'if by[v]["outcome"] != "match":', "if False:"),
    ("сводка расходится с фикстурами", "fixtures/additivity.jsonl",
     r're:("variant": "cancel-avg".*?"got": )[\d.]+', r"\g<1>0.5"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("scd.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "dimensional")
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
