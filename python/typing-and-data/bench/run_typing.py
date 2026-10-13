"""Сценарий матрицы: кто ловит подсунутый дефект типов.

ПЯТЬ ПЛЕЧ, И КАЖДОЕ СМОТРИТ СО СВОЕЙ СТОРОНЫ.

    mypy          проверка типов по умолчанию
    mypy-strict   она же в строгом режиме
    pyright       другой анализатор, другие умолчания
    pydantic      разбор входных данных на границе
    runtime       просто выполнить и посмотреть

ИСХОД — КОД ВОЗВРАТА, А НЕ ТЕКСТ. Формат вывода у анализаторов меняется от
версии к версии, и разбор строк ломается молча, показывая ноль ошибок там,
где их три. Код возврата у всех пяти означает одно и то же: ноль — прошло.

ЧТО ДОКАЗЫВАЕТ ХЕШ ФАЙЛОВ. Для каждого состояния кода все пять плеч обязаны
видеть ОДИН И ТОТ ЖЕ исходник. Хеш снимается до первого плеча и после
последнего: расхождение означало бы, что какой-то проверяющий переписал
код под собой, и сравнение развалилось бы незаметно.

ЧЕГО ЗДЕСЬ НЕТ. Времени. Ни одна величина этого стенда не измеряется
секундами, поэтому ни прогревов, ни ротаций, ни знаковых тестов не нужно:
«поймал» и «не поймал» переживают любой перезамер.
"""

import hashlib
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

SRC = Path("/src")
# Перечень дефектов ищется и по смонтированному пути, и рядом с
# репозиторием: первое работает в контейнере, второе — на хосте, где этот
# модуль импортируют тесты. Без второго тесты пришлось бы гонять только
# внутри образа, и предохранители отбора выдержки остались бы непроверенными.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "app"))
sys.path.insert(0, str(SRC))

import defects  # noqa: E402

WORK = Path("/work")
# Файлы, из которых складывается состояние кода. Кэши анализаторов сюда не
# входят: mypy пишет .mypy_cache рядом, и включи мы его в хеш, каждое
# плечо «меняло» бы исходник.
TRACKED = ("orders.py", "boundary.py", "scenario.py", "data.json")

PYDANTIC_CHECK = """
import json
import boundary

for item in json.load(open("data.json", encoding="utf-8")):
    boundary.OrderIn.model_validate(item)
print("граница: все заказы разобраны")
"""


def snapshot(root: Path) -> str:
    """Хеш состояния кода — по содержимому отслеживаемых файлов."""
    digest = hashlib.sha256()
    for name in sorted(TRACKED):
        digest.update(name.encode())
        digest.update((root / name).read_bytes())
    return digest.hexdigest()[:16]


def prepare(defect: str) -> str:
    """Чистая копия исходников с применённым дефектом.

    Копия, а не правка на месте: оригинал в /src смонтирован только на
    чтение, и прерванный прогон не оставит в репозитории испорченный файл.
    """
    if WORK.exists():
        shutil.rmtree(WORK)
    shutil.copytree(SRC, WORK)

    if defect == defects.NONE:
        return snapshot(WORK)

    name, replacements = defects.PATCHES[defect]
    target = WORK / name
    text = target.read_text(encoding="utf-8")
    for old, new in replacements:
        found = text.count(old)
        if found != 1:
            raise RuntimeError(
                f"{defect}: замена «{old[:50]}» найдена {found} раз вместо "
                "одного — правка применилась бы не туда, и плечи сравнивали "
                "бы разный код")
        text = text.replace(old, new, 1)
    target.write_text(text, encoding="utf-8")
    return snapshot(WORK)


def run(args, arm: str):
    """Один проверяющий. Возвращает код возврата и хвост вывода."""
    env = dict(os.environ, PYTHONPATH=str(WORK), PYTHONDONTWRITEBYTECODE="1")
    done = subprocess.run(args, cwd=WORK, capture_output=True, text=True,
                          env=env)
    tail = ((done.stdout or "") + (done.stderr or "")).strip().splitlines()
    return done.returncode, tail


def check(arm: str, defect: str):
    """Плечо на текущем состоянии кода.

    Кэш mypy у каждого плеча свой: общий кэш между обычным и строгим
    режимом дал бы одному из них результат, посчитанный при других
    настройках.
    """
    cache = f"/tmp/mypy-{arm}-{defect}"
    if arm == "mypy":
        return run(["mypy", "--cache-dir", cache, "orders.py"], arm)
    if arm == "mypy-strict":
        return run(["mypy", "--strict", "--cache-dir", cache, "orders.py"],
                   arm)
    if arm == "pyright":
        return run(["pyright", "orders.py"], arm)
    if arm == "pydantic":
        return run(["python", "-c", PYDANTIC_CHECK], arm)
    if arm == "runtime":
        return run(["python", "scenario.py"], arm)
    raise ValueError(arm)


ARMS = ("mypy", "mypy-strict", "pyright", "pydantic", "runtime")


# Строки, которые в выводе есть, а смысла не несут: заголовки, итоги и
# служебное слово «Traceback». Первая версия отбора брала первую непустую
# строку и приносила ровно их — «/work/orders.py» и «Traceback (most recent
# call last):». Формально выдержка была, читать в ней было нечего.
NOISE_PREFIXES = ("Found ", "Success", "Traceback", "0 errors")
EXCEPTION_MARK = "Error"


def evidence(tail):
    """Короткая выдержка из вывода — чем именно недоволен проверяющий.

    Не для решения (оно принимается по коду возврата), а для читателя:
    строка «поймал» без единого слова о том, что он увидел, ничего не
    объясняет.

    Порядок поиска идёт от самого содержательного к самому общему:

        1. строка исключения (`TypeError: ...`, `ValidationError: ...`) —
           у падений она последняя значимая, а не первая;
        2. строка с диагностикой анализатора (содержит «error:»);
        3. первая непустая строка, не входящая в служебные.
    """
    lines = [line.strip() for line in tail if line.strip()]

    for line in reversed(lines):
        head = line.split(":", 1)[0]
        if head.endswith(EXCEPTION_MARK) and not line.startswith("http"):
            return line[:200]

    for line in lines:
        if "error:" in line.lower() and not line.lower().startswith("0 error"):
            return line[:200]

    for line in lines:
        if not line.startswith(NOISE_PREFIXES) and not line.startswith("/"):
            return line[:200]
    return None


def main():
    out_path = os.environ.get("OUT", "/fixtures/typing.txt")
    with open(out_path, "a", encoding="utf-8") as out:
        for defect in defects.DEFECTS:
            before = prepare(defect)
            row = {"defect": defect, "sha256_before": before, "arms": {}}
            for arm in ARMS:
                code, tail = check(arm, defect)
                row["arms"][arm] = {
                    "exit_code": code,
                    "caught": code != 0,
                    "evidence": evidence(tail) if code != 0 else None,
                    # Полный вывод, а не только выдержка: по одной строке
                    # нельзя сказать, что ДРУГИХ диагностик проверяющий не
                    # выдал. Так было с mypy --strict на untyped-body: в
                    # выдержку попало «нет аннотации», а сообщение о самой
                    # ошибке сложения осталось за кадром.
                    "output": [line for line in tail if line.strip()][:60]
                    if code != 0 else [],
                }
            row["sha256_after"] = snapshot(WORK)
            out.write(json.dumps(row, ensure_ascii=False, sort_keys=True)
                      + chr(10))
            out.flush()
            caught = [a for a in ARMS if row["arms"][a]["caught"]]
            print(f"{defect:20} поймали: {', '.join(caught) or 'никто'}",
                  file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
