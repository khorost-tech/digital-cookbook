"""Сценарий тестов: кто ловит подсунутый дефект и сколько это стоит.

ДВЕ ЧАСТИ, И ОНИ ОТВЕЧАЮТ НА РАЗНОЕ.

1. МАТРИЦА. Пять состояний кода (исправное и четыре дефекта) на трёх
   наборах: моки, фейк на SQLite, настоящий PostgreSQL в контейнере.
   Результат — целые «да/нет»: упал хоть один тест набора или нет.

   Строка «исправный код» здесь не для красоты. Без неё «набор поймал
   дефект» ничего не значит: набор, который падает всегда, ловит любой
   дефект и ни одного. Контроль обязан быть зелёным у всех трёх.

   Четвёртое плечо — property-based набор на hypothesis: те же правила,
   но данные придумывает генератор, а ожидание считает независимый
   образец на Python. Для него меряется ещё и ЧИСЛО ПРИМЕРОВ до первого
   падения — в двух прогонах, потому что счёт со сжатием считал бы не то
   (см. hypothesis_run).

2. ЦЕНА. Время прогона и ЧИСЛО СТАРТОВ КОНТЕЙНЕРА при разных областях
   видимости приспособления. Старты считаются в conftest и пишутся в
   файл: выводить их из области видимости («session — значит один») —
   значит проверять своё представление о pytest, а не pytest.
"""

import json
import os
import subprocess
import sys
import time
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import measure  # noqa: E402

ROOT = "/work"
TESTS = f"{ROOT}/tests"
VENV = "/venvs/tests"
PY = f"{VENV}/bin/python"

# Список дефектов — из tests/defects.py, модуля без зависимостей:
# интерпретатор сценария не знает SQLAlchemy и импортировать repo.py не
# может, а дубликат списка тут уже соврал однажды. Подробности — там.
sys.path.insert(0, TESTS)
from defects import DEFECTS, WHAT  # noqa: E402,F401
SUITES = {
    "mocks": "test_with_mocks.py",
    "sqlite": "test_with_sqlite.py",
    "postgres": "test_with_postgres.py",
    "hypothesis": "test_with_hypothesis.py",
}
SCOPES = ("session", "function")
# Столько примеров генератору отводится на один прогон. Число уходит в
# фикстуру: «контроль прошёл» значит «прошёл на СТОЛЬКИХ примерах», и без
# этого числа утверждение неполное.
MAX_EXAMPLES = int(os.environ.get("HYP_MAX_EXAMPLES", "100"))


def run(args, **kwargs):
    done = subprocess.run(args, capture_output=True, text=True, **kwargs)
    if done.returncode != 0:
        tail = (done.stderr or done.stdout or "").strip().splitlines()
        raise RuntimeError(f"{' '.join(args)} → код {done.returncode}: "
                           + (" | ".join(tail[-5:]) or "без вывода"))
    return done.stdout


def prepare_venv():
    """Окружение для тестов — из того же локального индекса, что и всё."""
    if os.path.exists(PY):
        return
    run(["uv", "venv", "--python", "3.14", VENV])
    run(["uv", "pip", "install", "--python", PY, "--no-index",
         "--find-links", "/wheels", "pytest", "sqlalchemy", "psycopg",
         "psycopg-binary", "testcontainers", "docker", "hypothesis"])


def pytest_run(suite_file, defect, scope, starts_path, report_path):
    """Один прогон набора. Код возврата НЕ проверяется — падение тестов
    здесь не авария сценария, а измеряемый результат."""
    env = dict(os.environ, DEFECT=defect, STARTS_OUT=starts_path,
               PYTHONPATH=TESTS, PYTHONDONTWRITEBYTECODE="1",
               HYP_MAX_EXAMPLES=str(MAX_EXAMPLES))
    args = [PY, "-m", "pytest", suite_file, "-q", "-p", "no:cacheprovider",
            "--junitxml", report_path, "--pg-scope", scope]
    started = time.perf_counter()
    done = subprocess.run(args, cwd=TESTS, capture_output=True, text=True,
                          env=env)
    elapsed = time.perf_counter() - started
    if not os.path.exists(report_path):
        tail = (done.stderr or done.stdout or "").strip().splitlines()
        raise RuntimeError(f"pytest не оставил отчёта ({suite_file}, "
                           f"{defect}): " + " | ".join(tail[-6:]))
    return elapsed, parse_report(report_path), read_starts(starts_path)


def hypothesis_run(defect, shrink, starts_path, report_path):
    """Прогон property-based набора в одном из двух режимов.

    БЕЗ СЖАТИЯ считается, сколько примеров понадобилось, чтобы наткнуться
    на ошибку. СО СЖАТИЕМ добывается минимальный контрпример. Считать
    примеры на прогоне со сжатием нельзя: после первого падения
    hypothesis прогоняет тело ещё десятки раз, сокращая пример, и
    счётчик покажет работу сжатия, а не поиска.
    """
    env = dict(os.environ, DEFECT=defect, STARTS_OUT=starts_path,
               PYTHONPATH=TESTS, PYTHONDONTWRITEBYTECODE="1",
               HYP_MAX_EXAMPLES=str(MAX_EXAMPLES),
               HYP_SHRINK="1" if shrink else "0")
    args = [PY, "-m", "pytest", SUITES["hypothesis"], "-q", "-p",
            "no:cacheprovider", "--junitxml", report_path]
    started = time.perf_counter()
    subprocess.run(args, cwd=TESTS, capture_output=True, text=True, env=env)
    elapsed = time.perf_counter() - started
    cases = parse_report(report_path)
    failures = parse_failures(report_path)
    with open(starts_path, encoding="utf-8") as f:
        counters = json.load(f)
    return {
        "seconds": round(elapsed, 3),
        "caught": any(v in ("failure", "error") for v in cases.values()),
        "examples": counters.get("examples"),
        "counterexample": falsifying(failures),
    }


# Как hypothesis озаглавливает контрпример. Первая версия разбора искала
# «Falsifying example» — фразу, которой в выводе нет вовсе, и молча
# возвращала None для КАЖДОГО пойманного дефекта. Замер при этом выглядел
# исправным: поимки на месте, счётчики на месте, просто контрпримеров
# нет — а именно они и были результатом.
COUNTEREXAMPLE_MARKER = "Failing test case:"


def falsifying(failures):
    """Контрпример дословно — из отчёта, а не из вывода pytest.

    Блок печатается дважды: один раз внутри трассировки, с колонкой «E»
    от pytest, и один раз в конце — как его составил hypothesis. Берётся
    последний: он и есть авторский текст, без чужой разметки.
    """
    for text in failures.values():
        if COUNTEREXAMPLE_MARKER not in text:
            continue
        block = text[text.rindex(COUNTEREXAMPLE_MARKER):]
        return block.strip()
    return None


def parse_failures(path):
    root = ET.parse(path).getroot()
    out = {}
    for case in root.iter("testcase"):
        for child in case:
            if child.tag in ("failure", "error"):
                out[case.get("name")] = (child.text or "") + (child.get(
                    "message") or "")
    return out


def parse_report(path):
    """Исходы по тестам из junit-xml — целые «прошёл / не прошёл».

    Разбор отчёта, а не текста вывода: формат вывода pytest меняется от
    версии к версии, и разбор строк ломается молча, показывая ноль
    упавших там, где упало три.
    """
    root = ET.parse(path).getroot()
    out = {}
    for case in root.iter("testcase"):
        outcome = "passed"
        for child in case:
            if child.tag in ("failure", "error"):
                outcome = child.tag
                break
            if child.tag == "skipped":
                outcome = "skipped"
        out[case.get("name")] = outcome
    return out


def read_starts(path):
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f).get("postgres", 0)


def main():
    rounds = int(os.environ.get("ROUNDS", "3"))
    seed = int(os.environ.get("SEED", "0"))
    out_path = os.environ.get("OUT", "/fixtures/tests.txt")
    starts_path = "/tmp/starts.json"
    report_path = "/tmp/report.xml"

    prepare_venv()

    with open(out_path, "a", encoding="utf-8") as out:
        for defect in DEFECTS:
            for suite, path in SUITES.items():
                scope = "session"
                seconds, cases, starts = pytest_run(
                    path, defect, scope, starts_path, report_path)
                failed = sorted(name for name, res in cases.items()
                                if res in ("failure", "error"))
                row = {
                    "kind": "matrix",
                    "defect": defect,
                    "suite": suite,
                    "tests": len(cases),
                    "failed": len(failed),
                    "failed_names": failed,
                    "caught": bool(failed),
                    "seconds": round(seconds, 3),
                    "container_starts": starts,
                }
                out.write(json.dumps(row, ensure_ascii=False,
                                     sort_keys=True) + chr(10))
                out.flush()
                print(f"{defect:12} {suite:9} упало {len(failed)}/{len(cases)}"
                      f" за {seconds:.1f} с", file=sys.stderr)

        for defect in DEFECTS:
            for shrink in (False, True):
                row = hypothesis_run(defect, shrink, starts_path,
                                     report_path)
                row.update({"kind": "hypothesis", "defect": defect,
                            "shrink": shrink,
                            "max_examples": MAX_EXAMPLES})
                out.write(json.dumps(row, ensure_ascii=False,
                                     sort_keys=True) + chr(10))
                out.flush()
                print(f"hypothesis {defect:12} сжатие={int(shrink)} "
                      f"примеров {row['examples']} "
                      f"поймал={int(row['caught'])}", file=sys.stderr)

        # ЦЕНА меряется только на исправном коде. На дефектном часть
        # тестов падает раньше, чем доходит до работы, и время означало
        # бы «сколько занимает упасть», а не «сколько стоит проверка».
        for r in range(1, rounds + 1):
            order = measure.latin_square(seed, r, len(SCOPES))
            for idx in order:
                scope = SCOPES[idx]
                seconds, cases, starts = pytest_run(
                    SUITES["postgres"], "none", scope, starts_path,
                    report_path)
                out.write(json.dumps({
                    "kind": "cost",
                    "suite": "postgres",
                    "scope": scope,
                    "round": r,
                    "seconds": round(seconds, 3),
                    "tests": len(cases),
                    "container_starts": starts,
                    "failed": sum(1 for v in cases.values()
                                  if v in ("failure", "error")),
                }, ensure_ascii=False, sort_keys=True) + chr(10))
                out.flush()
                print(f"цена: scope={scope:8} {seconds:.1f} с, "
                      f"стартов {starts}", file=sys.stderr)
            for suite in ("mocks", "sqlite"):
                seconds, cases, starts = pytest_run(
                    SUITES[suite], "none", "session", starts_path,
                    report_path)
                out.write(json.dumps({
                    "kind": "cost",
                    "suite": suite,
                    "scope": "—",
                    "round": r,
                    "seconds": round(seconds, 3),
                    "tests": len(cases),
                    "container_starts": starts,
                    "failed": sum(1 for v in cases.values()
                                  if v in ("failure", "error")),
                }, ensure_ascii=False, sort_keys=True) + chr(10))
                out.flush()
            print(f"круг {r} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
