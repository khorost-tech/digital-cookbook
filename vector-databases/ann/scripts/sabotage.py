"""Реестр диверсий стенда vector-databases/ann: каждая проверка обязана краснеть от своей поломки.

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
    ('перебор разошёлся с истиной', "fixtures/brute.jsonl",
     r're:("variant": "control".*?"gt_agree": )\d+', r"\g<1>199"),
    ('время перебора не линейно', "fixtures/brute.jsonl",
     r're:("variant": "scaling".*?"linear": )true', r"\g<1>false"),
    ('recall HNSW убывает с ef', "fixtures/hnsw.jsonl",
     r're:("variant": "m8-efc200".*?"recall_monotonic": )true', r"\g<1>false"),
    ('HNSW не дошла до 0,95', "fixtures/hnsw.jsonl",
     r're:("variant": "m16-efc200".*?"reaches_target": )true', r"\g<1>false"),
    ('связи HNSW больше расчёта', "fixtures/hnsw.jsonl",
     r're:("variant": "m32-efc200".*?"link_within": )true', r"\g<1>false"),
    ('недостижимых узлов много', "fixtures/hnsw.jsonl",
     r're:("variant": "m16-efc100".*?"unreachable_ok": )true', r"\g<1>false"),
    ('recall не растёт с M', "fixtures/hnsw.jsonl",
     r're:("variant": "compare".*?"recall_grows_with_m": )true', r"\g<1>false"),
    ('ef_construction 100 не быстрее', "fixtures/hnsw.jsonl",
     r're:("variant": "compare".*?"efc100_builds_faster": )true', r"\g<1>false"),
    ('nprobe = nlist не дал recall 1', "fixtures/ivf.jsonl",
     r're:("variant": "control".*?"recall_at_10": )1', r"\g<1>0.99"),
    ('recall IVF убывает с nprobe', "fixtures/ivf.jsonl",
     r're:("variant": "skewed723".*?"recall_monotonic": )true', r"\g<1>false"),
    ('мелкие списки не дешевле', "fixtures/ivf.jsonl",
     r're:("variant": "compare".*?"finer_cheaper_at_090": )true', r"\g<1>false"),
    ('перекос списков не виден', "fixtures/ivf.jsonl",
     r're:("variant": "compare".*?"skewed_lists_uneven": )true', r"\g<1>false"),
    ('размер кодов PQ не N·m', "fixtures/quant.jsonl",
     r're:("variant": "pq96".*?"code_size_ok": )true', r"\g<1>false"),
    ('SQ8 без пересчёта ниже 0,9', "fixtures/quant.jsonl",
     r're:("variant": "sq8".*?"raw_ok": )true', r"\g<1>false"),
    ('PQ без пересчёта не теряет', "fixtures/quant.jsonl",
     r're:("variant": "pq48".*?"raw_loses_much": )true', r"\g<1>false"),
    ('m = 96 не точнее m = 48', "fixtures/quant.jsonl",
     r're:("variant": "compare".*?"finer_more_accurate": )true', r"\g<1>false"),
    ('dot совпал с cosine на ненормированных', "fixtures/metric.jsonl",
     r're:("variant": "all".*?"scaled_dot_differs": )true', r"\g<1>false"),
    ('HNSW разошлась с Faiss', "fixtures/crosscheck.jsonl",
     r're:("variant": "faiss-hnsw".*?"within": )true', r"\g<1>false"),
    ('PQ разошлась с Faiss', "fixtures/crosscheck.jsonl",
     r're:("variant": "faiss-pq".*?"max_gap~": )[\d.]+', r"\g<1>0.5"),
    ('jvector не дошёл до 0,9', "fixtures/crosscheck.jsonl",
     r're:("variant": "jvector".*?"reaches_min": )true', r"\g<1>false"),
    ('исход оси — несовпадение', "fixtures/metric.jsonl",
     r're:("variant": "all".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("сводка сравнивает шумные точки", "scripts/run-manifest.py",
     "    if isinstance(value, list):\n        return [strip(v) for v in value]\n", ""),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("brute.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "ann")
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
