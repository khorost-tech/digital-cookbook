"""Flask как WSGI-приложение.

Отклонения от умолчаний фреймворка и почему они сделаны:

* `app.json.sort_keys = False` — Flask по умолчанию сортирует ключи, а два
  других приложения отдают их в порядке построения. Без этого байты не
  совпали бы, и проверка эквивалентности заблокировала бы замер;
* `app.json.compact = True` — компактные разделители, по той же причине.

Валидация делается вручную через `common.validate_echo` — той же строгости и
того же объёма, что pydantic-модель у FastAPI. Это не «Flask проще, поэтому
быстрее»: количество проверок выровнено намеренно.

Flask — WSGI, и поднимается он под gunicorn, тогда как ASGI-приложения идут
под uvicorn. Разницу серверов из результата не вычесть, поэтому в стенде есть
контрольные плечи: голое WSGI- и голое ASGI-приложение без фреймворка. Они
показывают пол, ниже которого сервер не опускается.
"""

import time

from flask import Flask, request
from flask.json.provider import DefaultJSONProvider

from common import (ERROR_BODY, ERROR_STATUS, PING_BODY, SLOW_SECONDS,
                    ValidationError, WAIT_SECONDS, cpu_body, echo_body,
                    validate_echo)


class StandJSONProvider(DefaultJSONProvider):
    """Сериализует по-прежнему сам Flask — выровнен только формат вывода.

    Умолчания Flask, из-за которых байты не совпадали с двумя другими
    приложениями: сортировка ключей, экранирование не-ASCII и перенос строки
    в конце тела. Первые два снимаются настройками, третий — только
    переопределением `response`, которое иначе дописывает перевод строки.
    """

    sort_keys = False
    compact = True
    ensure_ascii = False

    def response(self, *args, **kwargs):
        # Разделители задаются здесь явно: Flask применяет компактный режим
        # именно в response(), а не в dumps(), и переопределение без этого
        # вернуло бы пробелы после запятых и двоеточий.
        obj = self._prepare_response_obj(args, kwargs)
        payload = self.dumps(obj, separators=(",", ":"))
        return self._app.response_class(payload, mimetype=self.mimetype)


app = Flask(__name__)
app.json = StandJSONProvider(app)


@app.get("/ping")
def ping():
    return PING_BODY


@app.post("/echo")
def echo():
    try:
        ident, name, tags = validate_echo(request.get_json(silent=True))
    except ValidationError:
        return ERROR_BODY, ERROR_STATUS
    return echo_body(ident, name, tags)


@app.post("/wait")
def wait():
    try:
        ident, name, tags = validate_echo(request.get_json(silent=True))
    except ValidationError:
        return ERROR_BODY, ERROR_STATUS
    # Синхронное ожидание: у WSGI-приложения нет цикла событий, и это его
    # честная модель — воркер занят всё время ожидания.
    time.sleep(WAIT_SECONDS)
    return echo_body(ident, name, tags)


@app.post("/cpu")
def cpu():
    try:
        ident, name, tags = validate_echo(request.get_json(silent=True))
    except ValidationError:
        return ERROR_BODY, ERROR_STATUS
    return cpu_body(ident, name, tags)


@app.post("/slow")
def slow():
    try:
        ident, name, tags = validate_echo(request.get_json(silent=True))
    except ValidationError:
        return ERROR_BODY, ERROR_STATUS
    time.sleep(SLOW_SECONDS)
    return echo_body(ident, name, tags)
