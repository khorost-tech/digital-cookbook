"""Реестр диверсий стенда vector-databases/pgvector: каждая проверка обязана краснеть от своей поломки.

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
    ('точный перебор разошёлся не на равных скорах', "fixtures/exact.jsonl",
     r're:("variant": "control".*?"only_near_ties": )true', r'\g<1>false'),
    ('граф не поместился в 2 ГБ', "fixtures/hnsw.jsonl",
     r're:("variant": "mwm2gb".*?"spilled": )false', r'\g<1>true'),
    ('recall pgvector убывает с ef_search', "fixtures/hnsw.jsonl",
     r're:("variant": "mwm2gb".*?"recall_monotonic": )true', r'\g<1>false'),
    ('HNSW pgvector далеко от своей HNSW', "fixtures/hnsw.jsonl",
     r're:("variant": "mwm2gb".*?"near_ann": )true', r'\g<1>false'),
    ('индекс меньше векторов', "fixtures/hnsw.jsonl",
     r're:("variant": "mwm2gb".*?"index_holds_vectors": )true', r'\g<1>false'),
    ('64 МБ не переполнились', "fixtures/hnsw.jsonl",
     r're:("variant": "mwm64mb".*?"spilled": )true', r'\g<1>false'),
    ('переполнение не замедлило построение', "fixtures/hnsw.jsonl",
     r're:("variant": "compare".*?"spill_slower": )true', r'\g<1>false'),
    ('IVFFlat далеко от своей IVF', "fixtures/ivfflat.jsonl",
     r're:("variant": "lists723".*?"near_ann": )true', r'\g<1>false'),
    ('индекс взяли не только для <#>', "fixtures/ops.jsonl",
     r're:("variant": "all".*?"only_ip_uses_index": )true', r'\g<1>false'),
    ('перебор не медленнее индекса', "fixtures/ops.jsonl",
     r're:("variant": "all".*?"others_10x_slower": )true', r'\g<1>false'),
    ('фильтр без итераций дал полную выдачу', "fixtures/filter.jsonl",
     r're:("variant": "hnsw".*?"off_short_when_selective": )true', r'\g<1>false'),
    ('итеративное сканирование не добрало строк', "fixtures/filter.jsonl",
     r're:("variant": "hnsw".*?"iterative_full": )true', r'\g<1>false'),
    ('план ушёл мимо HNSW без B-tree', "fixtures/filter.jsonl",
     r're:("variant": "hnsw".*?"always_hnsw": )true', r'\g<1>false'),
    ('при 0,1% не взят B-tree', "fixtures/filter.jsonl",
     r're:("variant": "hnsw-btree-custom".*?"btree_at_01pct": )true', r'\g<1>false'),
    ('при 0,1% выдача не точная', "fixtures/filter.jsonl",
     r're:("variant": "hnsw-btree-custom".*?"exact_at_01pct": )true', r'\g<1>false'),
    ('общий план не перебил частный', "fixtures/filter.jsonl",
     r're:("variant": "hnsw-btree".*?"generic_plan_wins": )true', r'\g<1>false'),
    ('halfvec не меньше', "fixtures/quant.jsonl",
     r're:("variant": "halfvec".*?"smaller_15x": )true', r'\g<1>false'),
    ('halfvec потерял recall', "fixtures/quant.jsonl",
     r're:("variant": "halfvec".*?"same_recall": )true', r'\g<1>false'),
    ('битовые коды с пересчётом ниже 0,9', "fixtures/quant.jsonl",
     r're:("variant": "binary-centered".*?"rescored_1000_ok": )true', r'\g<1>false'),
    ('знаковый код не теряет соседей', "fixtures/quant.jsonl",
     r're:("variant": "hamming".*?"sign_code_loses": )true', r'\g<1>false'),
    ('центрирование не помогло', "fixtures/quant.jsonl",
     r're:("variant": "hamming".*?"centering_fixes": )true', r'\g<1>false'),
    ('граф по битам лучше точного Хэмминга', "fixtures/quant.jsonl",
     r're:("variant": "binary".*?"graph_not_better_than_exact": )true', r'\g<1>false'),
    ('Java вернула другую выдачу', "fixtures/java.jsonl",
     r're:("variant": "jdbc".*?"identical": )true', r'\g<1>false'),
    ('исход оси — несовпадение', "fixtures/ops.jsonl",
     r're:("variant": "all".*?"outcome": )"match"', r'\g<1>"mismatch"'),
    ("сводка сравнивает шумные точки", "scripts/run-manifest.py",
     "    if isinstance(value, list):\n        return [strip(v) for v in value]\n", ""),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("exact.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "pgvector")
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
