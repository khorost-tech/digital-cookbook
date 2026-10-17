"""Три инструмента и ОДНА задача, которую каждый решает своим способом.

ЗАДАЧА СФОРМУЛИРОВАНА ТАК: от отсутствия окружения до окружения с
установленным набором. Не «время команды install», а весь путь.

Почему именно так. Poetry управляет окружением сам: `poetry env use` с
чужим путём он игнорирует и всё равно создаёт своё. Значит либо все три
плеча создают окружение, либо сравнение придётся строить на том, что у
одного инструмента этап есть, а у двух других его нет. Первое честнее, и
конечное состояние у всех одинаково — это проверяется отдельно.

ЧЕГО ЗДЕСЬ НЕТ. Разрешения зависимостей: все три ставят по готовому
описанию (uv.lock, poetry.lock, requirements.txt), где версии уже
зафиксированы. Сравнивать `uv sync` с `poetry add` было бы сравнением
разных операций.
"""

import os
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path

APP = "/app"
VENVS = Path("/venvs")

# Куда каждый инструмент ставит. У Poetry путь свой, он его выбирает сам.
UV_VENV = VENVS / "uv"
PIP_VENV = VENVS / "pip"
POETRY_ROOT = VENVS / "poetry"


@dataclass(frozen=True)
class Tool:
    name: str
    caches: tuple


TOOLS = (
    Tool("uv", ("/caches/uv",)),
    Tool("pip", ("/caches/pip",)),
    Tool("poetry", ("/caches/poetry",)),
)
BY_NAME = {t.name: t for t in TOOLS}


# Куда каждый инструмент ходит за пакетами и МОЖНО ЛИ это переопределить
# снаружи, не трогая проект.
#
# У uv и pip адрес берётся из окружения — одна переменная, и готово.
# У Poetry источник записан в pyproject.toml и продублирован в
# poetry.lock, а переменные POETRY_REPOSITORIES_* задают адреса для
# ПУБЛИКАЦИИ, а не для установки. Первая версия стенда этого не знала и
# честно выставляла POETRY_REPOSITORIES_LOCAL_URL: Poetry её игнорировал,
# ставил из локального индекса и показывал 4,2 секунды «против PyPI» —
# ровно столько же, сколько без сети. Число выглядело правдоподобно и
# было бы опубликовано.
#
# Поэтому список не «какая переменная», а «переопределяется ли вообще»,
# и подключённость ручки ПРОВЕРЯЕТСЯ (см. assert_index_honoured).
INDEX_ENV = {
    "uv": ("UV_DEFAULT_INDEX",),
    "pip": ("PIP_INDEX_URL",),
}
# Почему инструмента нет в списке — текстом, чтобы это попало в фикстуру,
# а не осталось умолчанием.
NO_INDEX_OVERRIDE = {
    "poetry": ("источник записан в pyproject.toml и poetry.lock; сменить "
               "его снаружи нельзя, а правка проекта изменила бы задачу"),
}


def index_env(tool_name, url):
    """Окружение, отправляющее один инструмент по заданному адресу."""
    if tool_name not in INDEX_ENV:
        raise ValueError(f"{tool_name}: {NO_INDEX_OVERRIDE[tool_name]}")
    env = {name: url for name in INDEX_ENV[tool_name]}
    if tool_name == "pip":
        # Доверенный хост нужен только для простого HTTP локального
        # индекса. Для https он лишний и сбивает проверку сертификата.
        env["PIP_TRUSTED_HOST"] = "" if url.startswith("https") else "index"
    return env


DEAD_INDEX = "http://127.0.0.1:9/simple"


def assert_index_honoured(tool):
    """Проверяет, что переопределение адреса вообще работает.

    Установка с заведомо мёртвого адреса ОБЯЗАНА упасть. Если она
    проходит, значит инструмент ходил куда-то ещё — и любое число,
    снятое «против другого индекса», на самом деле снято против
    прежнего.

    Проверка стоит секунды и заводится не из осторожности: ровно этот
    случай на стенде и произошёл, причём молча и правдоподобно.
    """
    prepare(tool)
    try:
        install(tool, env_extra=index_env(tool.name, DEAD_INDEX))
    except RuntimeError:
        return True
    raise RuntimeError(
        f"{tool.name}: установка с мёртвого адреса {DEAD_INDEX} прошла — "
        "переопределение индекса не действует, и замер против другого "
        "индекса измерял бы прежний")


def _run(args, **kwargs):
    """Запуск с внятным отказом.

    capture_output прячет stderr внутри исключения, и падение выглядит как
    «exit status 2» без единого слова о причине. Здесь причина
    выкладывается наружу.
    """
    done = subprocess.run(args, capture_output=True, text=True, **kwargs)
    if done.returncode != 0:
        tail = (done.stderr or done.stdout or "").strip().splitlines()
        raise RuntimeError(
            f"{' '.join(args)} → код {done.returncode}: "
            + (" | ".join(tail[-3:]) if tail else "без вывода"))
    return done


def venv_path(tool):
    if tool.name == "uv":
        return UV_VENV
    if tool.name == "pip":
        return PIP_VENV
    out = _run(["poetry", "env", "info", "--path"], cwd=APP).stdout.strip()
    return Path(out)


def prepare(tool):
    """Убирает окружение прошлого прогона. Кэши НЕ трогает — ими
    распоряжается сценарий, у него состояния перечислены."""
    if tool.name == "poetry":
        if POETRY_ROOT.exists():
            shutil.rmtree(POETRY_ROOT)
        POETRY_ROOT.mkdir(parents=True, exist_ok=True)
        return
    target = UV_VENV if tool.name == "uv" else PIP_VENV
    if target.exists():
        shutil.rmtree(target)


def install(tool, env_extra=None):
    """Весь путь: создать окружение, если его нет, и установить набор.

    «Если его нет» — существенная оговорка. В состоянии noop окружение уже
    на месте, и пересоздавать его нельзя: повторный install у разработчика
    ничего не пересоздаёт, он проверяет, что всё стоит. Первая версия
    создавала окружение всегда и на этом состоянии просто падала — что
    к лучшему, потому что молчаливое пересоздание превратило бы noop в
    ещё один warm, и разница между ними исчезла бы незаметно.

    Poetry сюда не попадает: он сам решает, создавать окружение или взять
    существующее, и делает это внутри install.
    """
    env = dict(os.environ, **(env_extra or {}))
    if tool.name == "uv":
        if not UV_VENV.exists():
            _run(["uv", "venv", "--python", "3.14", str(UV_VENV)], env=env)
        _run(["uv", "pip", "install", "--python", f"{UV_VENV}/bin/python",
              "-r", f"{APP}/requirements.txt"], env=env)
        return
    if tool.name == "pip":
        # Окружение создаётся модулем venv, а не uv: иначе в плечо pip
        # затесалась бы работа конкурента, и «pip медленнее» означало бы
        # отчасти «uv быстрее создаёт окружения».
        if not PIP_VENV.exists():
            _run([_interpreter(), "-m", "venv", str(PIP_VENV)], env=env)
        _run(["pip", "--python", f"{PIP_VENV}/bin/python", "install",
              "-r", f"{APP}/requirements.txt"], env=env)
        return
    if tool.name == "poetry":
        _run(["poetry", "install", "--no-root"], cwd=APP,
             env=dict(env, POETRY_VIRTUALENVS_PATH=str(POETRY_ROOT)))
        return
    raise ValueError(tool.name)


def _interpreter():
    """Тот же интерпретатор 3.14, что использует uv."""
    found = sorted(Path("/opt/py-versions").glob("*/bin/python3.14"))
    if not found:
        raise FileNotFoundError("интерпретатор 3.14 не найден")
    return str(found[0])
