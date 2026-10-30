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
    # оси 1–2
    ("исправленная пара с находкой", "fixtures/default.jsonl",
     r're:("variant": "fixed-curated".*?"issues": )0', r"\g<1>1"),
    ("курируемый конфиг пропустил дефект", "fixtures/default.jsonl",
     r're:("variant": "curated".*?"missed": )0', r"\g<1>1"),
    ("набор по умолчанию нашёл rowserrcheck", "fixtures/default.jsonl",
     r're:("variant": "standard".*?"missed_by_linter": \{)"rowserrcheck": 1, ', r"\g<1>"),
    ("«всё» без шума", "fixtures/default.jsonl", r're:("variant": "all".*?"extra": )\d+', r"\g<1>0"),
    ("счёт с обрезкой не отвергается", "scripts/analyze-default.py",
     'if "--max-same-issues 0" not in r["lint_cmd"] or "--uniq-by-line=false" not in r["lint_cmd"]:', "if False:"),
    # ось 3
    ("лимит на линтер не 50", "fixtures/truncate.jsonl", r're:("variant": "many/default".*?"shown": )50', r"\g<1>60"),
    ("необрезанный вывод по умолчанию не отвергается", "scripts/analyze-truncate.py",
     'if r["mode"] == "default" and r["outcome"] != "truncated":', "if False:"),
    ("обрезанный вывод с кодом 0", "fixtures/truncate.jsonl", r're:("variant": "line/default".*?"rc": )1', r"\g<1>0"),
    # ось 4
    ("контроль исключений что-то спрятал", "fixtures/exclude.jsonl",
     r're:("variant": "base".*?"hidden_want": \[)\]', r'\g<1>"x:1 errcheck"]'),
    ("std-error-handling спрятал меньше", "fixtures/exclude.jsonl",
     r're:("variant": "preset-std-error-handling".*?"hidden_want": \[.*?)"excl/gen/auto.go:9 errcheck", ', r"\g<1>"),
    ("strict спрятал и нестандартный заголовок", "fixtures/exclude.jsonl",
     r're:("variant": "generated-strict".*?"hidden_want": \[)', r'\g<1>"excl/gen/auto.go:9 errcheck", '),
    ("сверка с контролем снята", "scripts/analyze-exclude.py",
     'if r["found"] + len(r["hidden_want"]) != base["found"]:', "if False:"),
    # ось 5
    ("контроль без директив не нашёл", "fixtures/nolint.jsonl",
     r're:("variant": "control/bare".*?"shown": )\["errcheck"\]', r"\g<1>[]"),
    ("голый //nolint спрятал один дефект", "fixtures/nolint.jsonl",
     r're:("variant": "strict/two-bare".*?"suppressed": )\["errcheck", "noctx"\]', r'\g<1>["errcheck"]'),
    ("узкая директива спрятала чужой линтер", "fixtures/nolint.jsonl",
     r're:("variant": "strict/two-specific".*?"shown": )\["noctx"\]', r"\g<1>[]"),
    ("директива с причиной отмечена", "fixtures/nolint.jsonl",
     r're:("variant": "strict/with-reason".*?"nolintlint": )\[\]', r'\g<1>["без причины"]'),
    ("отметки nolintlint без nolintlint не отвергаются", "scripts/analyze-nolint.py",
     'if r["config"] in ("control", "plain") and r["nolintlint"]:', "if False:"),
    # ось 6
    ("new-from-rev не подхватил правку старой строки", "fixtures/legacy.jsonl",
     r're:("variant": "new-from-rev".*?"shown": \[)"legacy/a.go:8", ', r"\g<1>"),
    ("whole-files показал нетронутый файл", "fixtures/legacy.jsonl",
     r're:("variant": "whole-files".*?"shown": \[)', r'\g<1>"legacy/b.go:7", '),
    ("разные диапазоны не отвергаются", "scripts/analyze-legacy.py",
     'if len({(r["main"], r["feature"]) for r in rows}) != 1:', "if False:"),
    # ось 7
    ("analysistest упал", "fixtures/custom.jsonl", r're:("variant": "analysistest".*?"rc": )0', r"\g<1>1"),
    ("обычный бинарь принял ctxloss", "fixtures/custom.jsonl", r're:("variant": "plain-binary".*?"rc": )3', r"\g<1>1"),
    ("contextcheck поймал Poll", "fixtures/custom.jsonl",
     r're:("contextcheck": \{"caught": \["Audit", "Get", "GetTimeout"\], "missed": \["Enqueue", )"Poll"', r'\g<1>"X"'),
    ("ложное срабатывание не отвергается", "scripts/analyze-custom.py",
     'if s["false"]:', "if False:"),
    # ось 8: смысл разметки
    ("полный проход «держит» соединение", "fixtures/semantics.jsonl",
     r're:("variant": "TestDrainedRowsReleaseConnection".*?"in_use": )0', r"\g<1>1"),
    ("ранний выход не держит соединение", "fixtures/semantics.jsonl",
     r're:("variant": "TestSampleFirstHoldsConnection".*?"in_use": )1', r"\g<1>0"),
    ("исправление IDs меняет контракт", "fixtures/semantics.jsonl",
     r're:("variant": "TestIDsEmptyContract".*?"fixed_null": )1', r"\g<1>0"),
    ("Merge не делит память", "fixtures/semantics.jsonl",
     r're:("variant": "TestMergeAliasing".*?"sample_aliases": )1', r"\g<1>0"),
    ("мёртвый код посчитан ошибкой", "fixtures/default.jsonl",
     r're:("variant": "standard".*?"dead_places": )1', r"\g<1>0"),
    # рецензия: места и широта исключений
    ("мест найдено больше", "fixtures/default.jsonl", r're:("variant": "standard".*?"places_found": )5', r"\g<1>6"),
    ("широкий курируемый ничего не спрятал", "fixtures/exclude.jsonl",
     r're:("variant": "curated-broad".*?"hidden_want": \[)"excl/danger/danger.go:35 errcheck"', r"\g<1>"),
    # тренажёр
    ("нетронутый уровень зелёный", "fixtures/trainer.jsonl",
     r're:("variant": "l2/start".*?"outcome": )"red"', r'\g<1>"green"'),
    ("замечание без подсказки", "fixtures/trainer.jsonl",
     r're:("variant": "l1/start".*?"no_hint": )\[\]', r'\g<1>["x"]'),
    ("эталон не проходит", "fixtures/trainer.jsonl",
     r're:("variant": "l4/solution".*?"outcome": )"green"', r'\g<1>"red"'),
    # сводки и маркер
    ("сводка расходится с фикстурами", "fixtures/exclude.jsonl",
     r're:("variant": "preset-legacy".*?"hidden_noise": \[)"excl/gen/auto.go:9 gosec", ', r"\g<1>"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("default.jsonl": ")[0-9a-f]', r"\g<1>z"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "golangci-lint")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", "bin", ".git"))
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
