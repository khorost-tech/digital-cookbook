"""Реестр диверсий стенда vector-databases/dedicated: каждая проверка обязана краснеть от своей поломки.

Прогон: копия стенда во временном каталоге. Сначала контрольная копия без
диверсий: её набор обязан пройти; иначе «покраснело» у диверсий ничего не
значит. Затем каждая диверсия по одной: применить замену текста, прогнать
набор (без Docker: STAND_NO_DOCKER=1), ожидать провал. Неприменившаяся диверсия — проваленная
проверка, а не пропуск.

    python3 scripts/sabotage.py            → по строке на диверсию, код 0 — все сработали

Пока ставится маркер (STAND_MARKER_PENDING=1), диверсии, нацеленные на сам
маркер, пропускаются и называются отдельно.
"""

import glob
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
    ('точный поиск Qdrant разошёлся', "fixtures/qdrant.jsonl",
     r're:("variant": "exact".*?"only_near_ties": )true', r'\g<1>false'),
    ('загружен не весь корпус в Milvus', "fixtures/milvus.jsonl",
     r're:("variant": "load".*?"rows": )522931', r'\g<1>522930'),
    ('recall Weaviate убывает с ef', "fixtures/weaviate.jsonl",
     r're:("variant": "hnsw".*?"recall_monotonic": )true', r'\g<1>false'),
    ('Milvus далеко от pgvector', "fixtures/milvus.jsonl",
     r're:("variant": "hnsw".*?"near_pgvector": )true', r'\g<1>false'),
    ('фильтр Qdrant дал неполную выдачу', "fixtures/qdrant.jsonl",
     r're:("variant": "filter".*?"always_full": )true', r'\g<1>false'),
    ('фильтр Weaviate потерял соседей', "fixtures/weaviate.jsonl",
     r're:("variant": "filter".*?"recall_min_ok": )true', r'\g<1>false'),
    ('Java Milvus вернула другую выдачу', "fixtures/milvus.jsonl",
     r're:("variant": "java".*?"identical": )true', r'\g<1>false'),
    ('int8 без пересчёта потерял больше допуска', "fixtures/qdrant.jsonl",
     r're:("variant": "quant".*?"codes_within_tolerance": )true', r'\g<1>false'),
    ('int8 с пересчётом потерял больше 0,01', "fixtures/qdrant.jsonl",
     r're:("variant": "quant".*?"rescore_within_001": )true', r'\g<1>false'),
    ('ACORN не потерял соседей при 10%', "fixtures/weaviate.jsonl",
     r're:("variant": "filter".*?"acorn_loses_at_10": )true', r'\g<1>false'),
    ('Weaviate не увидел свежие сразу', "fixtures/weaviate.jsonl",
     r're:("variant": "fresh".*?"all_at_once": )true', r'\g<1>false'),
    ('непроиндексированный сегмент не медленнее', "fixtures/qdrant.jsonl",
     r're:("variant": "fresh".*?"unindexed_slower": )true', r'\g<1>false'),
    ('Bounded не увидел свежие', "fixtures/milvus.jsonl",
     r're:("variant": "fresh".*?"all_at_once": )true', r'\g<1>false'),
    ('sweeping не лучше acorn при 10%', "fixtures/weaviate.jsonl",
     r're:("variant": "filter-sweeping".*?"sweeping_better_at_10": )true', r'\g<1>false'),
    ('Strong не увидел свежие', "fixtures/milvus.jsonl",
     r're:("variant": "fresh-strong".*?"all_at_once": )true', r'\g<1>false'),
    ('исход оси — несовпадение', "fixtures/weaviate.jsonl",
     r're:("variant": "java".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("сводка сравнивает шумные точки", "scripts/run-manifest.py",
     "    if isinstance(value, list):\n        return [strip(v) for v in value]\n", ""),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("qdrant.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "dedicated")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", ".git", "out", "bin", "target"))
    return stand


def run_suite(stand):
    env = dict(os.environ)
    env["STAND_NO_DOCKER"] = "1"
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
            if rel == "scripts/run-manifest.py" and len(glob.glob(os.path.join(HERE, "fixtures", "manifests", "*.json"))) < 2:
                # сравнение сводок ловит её только при двух сессиях; после первой сравнивать не с чем
                print("пропущена (сводка одна, сравнивать не с чем): %s" % name)
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
        print("пропущено: %d" % skipped)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
