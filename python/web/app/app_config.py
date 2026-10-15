"""Две реализации одной конфигурации: наивная и строгая.

Разница между ними обычно описывается словами «валидируйте конфиг на старте».
Здесь она измеряется: что происходит с процессом, когда переменной нет или
её значение недопустимо.

* naive — `os.getenv` по месту использования. Процесс поднимается, порт
  слушает, и всё выглядит здоровым ровно до первого запроса, которому нужна
  эта переменная.
* strict — проверка при импорте. Процесс не поднимается вовсе.

Оба приложения обслуживают один и тот же контракт и обязаны отдавать
одинаковые ответы при КОРРЕКТНОЙ конфигурации — иначе сравнивалось бы
поведение разных приложений, а не разных подходов к конфигурации.

Какой режим поднимать, задаёт APP_MODE, конфигурацию — DATABASE_URL и
POOL_SIZE.
"""

import json
import os
import sys

MODE = os.environ.get("APP_MODE", "naive")

REQUIRED = ("DATABASE_URL", "POOL_SIZE")


class ConfigError(Exception):
    """Ошибка конфигурации. Одна на оба режима."""


def check_config(env):
    """Проверки, одинаковые для обоих режимов. Различается только МОМЕНТ,
    когда они выполняются: на старте или при первом обращении."""
    missing = [k for k in REQUIRED if not env.get(k)]
    if missing:
        raise ConfigError(f"нет обязательных переменных: {', '.join(missing)}")

    url = env["DATABASE_URL"]
    if not url.startswith(("postgresql://", "postgres://")):
        raise ConfigError(f"DATABASE_URL должен быть postgresql://, получено: {url[:32]}")

    raw = env["POOL_SIZE"]
    try:
        size = int(raw)
    except ValueError:
        raise ConfigError(f"POOL_SIZE должен быть целым, получено: {raw!r}") from None
    if not 1 <= size <= 100:
        raise ConfigError(f"POOL_SIZE должен быть 1..100, получено: {size}")

    return {"database_url": url, "pool_size": size}


# СТРОГИЙ режим: проверка при импорте, то есть до того, как сервер начнёт
# слушать порт. Неверная конфигурация означает, что процесс не поднимется.
CONFIG = None
if MODE == "strict":
    try:
        CONFIG = check_config(os.environ)
    except ConfigError as exc:
        print(f"КОНФИГУРАЦИЯ ОТВЕРГНУТА НА СТАРТЕ: {exc}", file=sys.stderr)
        raise SystemExit(78)          # EX_CONFIG из sysexits.h


def body():
    """НАИВНЫЙ режим читает конфигурацию здесь — при обработке запроса."""
    conf = CONFIG if CONFIG is not None else check_config(os.environ)
    return {"ok": True, "pool_size": conf["pool_size"]}


async def app(scope, receive, send):
    if scope["type"] != "http":
        return

    if scope["path"] == "/health":
        # Здоровье, как его часто пишут: отвечает, не заглядывая в
        # конфигурацию. Именно поэтому наивный режим выглядит здоровым.
        status, payload = 200, {"ok": True}
    else:
        try:
            status, payload = 200, body()
        except ConfigError as exc:
            status, payload = 500, {"error": str(exc)}

    data = json.dumps(payload, separators=(",", ":"),
                      ensure_ascii=False).encode("utf-8")
    await send({"type": "http.response.start", "status": status,
                "headers": [(b"content-type", b"application/json"),
                            (b"content-length", str(len(data)).encode())]})
    await send({"type": "http.response.body", "body": data})
