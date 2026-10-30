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
    ("папка сохранила изменения первого писателя", "fixtures/writers.jsonl",
     r're:("variant": "folder".*?"a_kept": )0', r"\g<1>26"),
    ("Iceberg не отклонил второй коммит", "fixtures/writers.jsonl",
     r're:("variant": "iceberg".*?"b_rejected": )1', r"\g<1>0"),
    ("папка не показала половину", "fixtures/reader.jsonl",
     r're:("variant": "folder".*?"seen_mid": )\d+', r"\g<1>0"),
    ("Iceberg показал незакоммиченное", "fixtures/reader.jsonl",
     r're:("variant": "iceberg".*?"seen_mid": )0', r"\g<1>130"),
    ("merge-on-read переписал файл", "fixtures/delete.jsonl",
     r're:("variant": "merge-on-read".*?"rewritten_files": )0', r"\g<1>1"),
    ("у папки появилась история", "fixtures/delete.jsonl",
     r're:("variant": "folder".*?"history": )false', r"\g<1>true"),
    ("папка не воскресила значения", "fixtures/schema.jsonl",
     r're:("variant": "folder".*?"readded_old_values": )\d+', r"\g<1>0"),
    ("Iceberg запланировал все файлы", "fixtures/partitions.jsonl",
     r're:("variant": "iceberg".*?"files_planned": )1', r"\g<1>8"),
    ("сжатие не уменьшило файлы", "fixtures/maintenance.jsonl",
     r're:("variant": "compact".*?"data_files": )1', r"\g<1>50"),
    ("контроль писателей снят", "scripts/analyze.py", 'if by[v]["lost_a"]:', "if False:"),
    ("сводка расходится с фикстурами", "fixtures/maintenance.jsonl",
     r're:("variant": "expire".*?"metadata_objects": )\d+', r"\g<1>1"),
    ("merge-on-read стёр строку без уплотнения", "fixtures/erase.jsonl",
     r're:("variant": "merge-on-read".*?"physical_after_expire": )1', r"\g<1>0"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("writers.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "iceberg")
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
