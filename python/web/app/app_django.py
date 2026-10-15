"""Django как ASGI-приложение, приведённое к общему контракту.

Отклонения от умолчаний перечислены в докстринге app_django_settings.py:
пустой middleware, без ORM, без установленных приложений. Здесь — только
маршруты и обработчики.

Ответ отдаётся `JsonResponse` с компактными разделителями: у Django по
умолчанию `json.dumps` ставит пробелы после разделителей, и байты не совпали
бы с двумя другими приложениями. Сериализацию при этом делает сам Django —
это часть измеряемой работы, мы лишь выравниваем формат вывода.
"""

import asyncio
import json
import os

import django
from django.conf import settings
from django.http import JsonResponse
from django.urls import path

os.environ.setdefault("DJANGO_SETTINGS_MODULE", "app_django_settings")
if not settings.configured:
    django.setup()

from common import (ERROR_BODY, ERROR_STATUS, PING_BODY, SLOW_SECONDS,  # noqa: E402
                    ValidationError, WAIT_SECONDS, cpu_body, echo_body,
                    validate_echo)

DUMPS_PARAMS = {"separators": (",", ":"), "ensure_ascii": False}


def _json(body, status=200):
    return JsonResponse(body, status=status, json_dumps_params=DUMPS_PARAMS)


def _parse(request):
    try:
        payload = json.loads(request.body)
    except ValueError as exc:
        raise ValidationError("body is not valid JSON") from exc
    return validate_echo(payload)


async def ping(request):
    return _json(PING_BODY)


async def echo(request):
    try:
        ident, name, tags = _parse(request)
    except ValidationError:
        return _json(ERROR_BODY, status=ERROR_STATUS)
    return _json(echo_body(ident, name, tags))


async def wait(request):
    try:
        ident, name, tags = _parse(request)
    except ValidationError:
        return _json(ERROR_BODY, status=ERROR_STATUS)
    await asyncio.sleep(WAIT_SECONDS)
    return _json(echo_body(ident, name, tags))


async def cpu(request):
    try:
        ident, name, tags = _parse(request)
    except ValidationError:
        return _json(ERROR_BODY, status=ERROR_STATUS)
    return _json(cpu_body(ident, name, tags))


async def slow(request):
    try:
        ident, name, tags = _parse(request)
    except ValidationError:
        return _json(ERROR_BODY, status=ERROR_STATUS)
    await asyncio.sleep(SLOW_SECONDS)
    return _json(echo_body(ident, name, tags))


urlpatterns = [
    path("ping", ping),
    path("echo", echo),
    path("wait", wait),
    path("cpu", cpu),
    path("slow", slow),
]

from django.core.asgi import get_asgi_application  # noqa: E402

application = get_asgi_application()
