"""FastAPI: валидация через pydantic — его идиома и его цена.

Отклонения от умолчаний фреймворка и почему они сделаны:

* автодокументация отключена (`docs_url=None`, `openapi_url=None`) — у Flask и
  Django её нет, и держать её включённой значило бы дать FastAPI лишнюю
  работу при старте и лишние маршруты;
* ответ отдаётся `JSONResponse`, а не возвращается словарём: возврат словаря
  прогнал бы его ещё раз через pydantic-сериализацию ответа, чего у двух
  других приложений нет.

Валидацию НЕ отключаем: она здесь настоящая, pydantic-овская, и именно её
цену на `/echo` мы и хотим увидеть.
"""

import asyncio

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, Field

from common import (ERROR_BODY, ERROR_STATUS, PING_BODY, SLOW_SECONDS,
                    WAIT_SECONDS, cpu_body, echo_body)

app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)


@app.exception_handler(RequestValidationError)
async def validation_error(request: Request, exc: RequestValidationError):
    """Формат ответа на отвергнутый ввод приводится к общему.

    Сама валидация остаётся pydantic-овской — это предмет замера. Меняется
    только тело ошибки: по умолчанию pydantic отдаёт список объектов с типом,
    путём и контекстом, и требовать того же от ручных проверок значило бы
    навесить на них работу, которой у них нет.
    """
    return JSONResponse(ERROR_BODY, status_code=ERROR_STATUS)


class EchoIn(BaseModel):
    # Те же проверки, что делает common.validate_echo вручную для Flask и
    # Django: набор полей закрыт, id положительный, длина имени 1..64,
    # не больше 16 строковых тегов.
    model_config = ConfigDict(extra="forbid")

    id: int = Field(ge=1)
    name: str = Field(min_length=1, max_length=64)
    tags: list[str] = Field(max_length=16)


@app.get("/ping")
async def ping():
    return JSONResponse(PING_BODY)


@app.post("/echo")
async def echo(payload: EchoIn):
    return JSONResponse(echo_body(payload.id, payload.name, payload.tags))


@app.post("/wait")
async def wait(payload: EchoIn):
    await asyncio.sleep(WAIT_SECONDS)
    return JSONResponse(echo_body(payload.id, payload.name, payload.tags))


@app.post("/cpu")
async def cpu(payload: EchoIn):
    # Счётная работа в обработчике: на ней воркеры-процессы обходят GIL,
    # и картина обратная той, что на /wait.
    return JSONResponse(cpu_body(payload.id, payload.name, payload.tags))


@app.post("/slow")
async def slow(payload: EchoIn):
    await asyncio.sleep(SLOW_SECONDS)
    return JSONResponse(echo_body(payload.id, payload.name, payload.tags))
