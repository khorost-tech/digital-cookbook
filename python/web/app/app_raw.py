"""Контрольные плечи: тот же контракт БЕЗ фреймворка.

Зачем они нужны. FastAPI и Django идут под uvicorn, Flask — под gunicorn.
Разницу серверов из результата не вычесть, и сравнение «фреймворк против
фреймворка» через две разные машинерии было бы сравнением машинерий.

Эти два приложения дают пол: сколько стоит тот же ответ на голом ASGI и на
голом WSGI, при том же сервере и той же проверке входных данных. Дальше цена
фреймворка читается как надбавка над своим полом, а не как разница с чужим.

Валидация здесь та же самая — `common.validate_echo`. Без неё контроль мерил
бы меньший объём работы и занижал бы пол.
"""

import asyncio
import json
import time

from common import (ERROR_BODY, ERROR_STATUS, PING_BODY, SLOW_SECONDS,
                    ValidationError, WAIT_SECONDS, cpu_body, dumps,
                    echo_body, validate_echo)

JSON_HEADER = [(b"content-type", b"application/json")]


def _handle(raw_body, kind="echo"):
    """Общая часть обоих контролей: разбор, проверка, тело ответа."""
    try:
        payload = json.loads(raw_body)
    except ValueError:
        return ERROR_STATUS, ERROR_BODY
    try:
        ident, name, tags = validate_echo(payload)
    except ValidationError:
        return ERROR_STATUS, ERROR_BODY
    maker = cpu_body if kind == "cpu" else echo_body
    return 200, maker(ident, name, tags)


# --------------------------------------------------------------- ASGI

async def _read_body(receive):
    chunks = []
    while True:
        message = await receive()
        chunks.append(message.get("body", b""))
        if not message.get("more_body", False):
            break
    return b"".join(chunks)


async def asgi_app(scope, receive, send):
    if scope["type"] != "http":
        return
    path = scope["path"]

    if path == "/ping":
        status, body = 200, PING_BODY
    elif path in ("/echo", "/wait", "/cpu", "/slow"):
        status, body = _handle(await _read_body(receive),
                               "cpu" if path == "/cpu" else "echo")
        if status == 200 and path == "/wait":
            await asyncio.sleep(WAIT_SECONDS)
        elif status == 200 and path == "/slow":
            await asyncio.sleep(SLOW_SECONDS)
    else:
        status, body = 404, {"detail": "not found"}

    payload = dumps(body).encode("utf-8")
    await send({"type": "http.response.start", "status": status,
                "headers": JSON_HEADER + [(b"content-length",
                                           str(len(payload)).encode())]})
    await send({"type": "http.response.body", "body": payload})


# --------------------------------------------------------------- WSGI

def wsgi_app(environ, start_response):
    path = environ.get("PATH_INFO", "")

    if path == "/ping":
        status, body = 200, PING_BODY
    elif path in ("/echo", "/wait", "/cpu", "/slow"):
        length = int(environ.get("CONTENT_LENGTH") or 0)
        status, body = _handle(environ["wsgi.input"].read(length),
                               "cpu" if path == "/cpu" else "echo")
        if status == 200 and path == "/wait":
            time.sleep(WAIT_SECONDS)
        elif status == 200 and path == "/slow":
            time.sleep(SLOW_SECONDS)
    else:
        status, body = 404, {"detail": "not found"}

    payload = dumps(body).encode("utf-8")
    start_response(f"{status} OK", [("content-type", "application/json"),
                                    ("content-length", str(len(payload)))])
    return [payload]
