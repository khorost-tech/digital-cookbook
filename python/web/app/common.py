"""Общий контракт трёх приложений-близнецов.

ВСЕ три приложения обязаны звать функции отсюда, а не воспроизводить логику у
себя. Если у приложения появится своя версия формирования ответа, замер начнёт
сравнивать реализации, а не фреймворки.

Тело ответа детерминировано: ни временных меток, ни случайных значений, ни
порядка ключей, зависящего от реализации. Иначе побайтовое сравнение из
scripts/smoke.sh работать не будет, а без него весь стенд бессмыслен —
сравнивались бы разные объёмы работы.
"""

import json

WAIT_SECONDS = 0.02

# Ожидание заведомо дольше таймаута дренажа: нужно, чтобы увидеть, что
# сервер делает с запросом, который не успевает завершиться к концу
# корректного завершения.
SLOW_SECONDS = 5.0

# Размер счётной задачи ЗАМЕРЕН, а не подобран по аналогии: при 93000 шагах
# одна операция занимает около 20 мс, столько же, сколько ожидание на /wait.
# Первая версия ставила 30000 «по опыту соседнего стенда» и давала 6.3 мс —
# втрое меньше, то есть ручки сравнивались бы разного объёма.
# Замер: 30000 -> 6.31 мс, 60000 -> 12.86, 85000 -> 18.56, 90000 -> 19.30.
CPU_STEPS = 93000
_MASK = 0xFFFFFFFF

# Ключи перечислены явно и в фиксированном порядке: словари Python сохраняют
# порядок вставки, поэтому один и тот же порядок построения даёт один и тот же
# порядок в JSON у всех трёх фреймворков.
PING_BODY = {"ok": True, "service": "twin"}

# Тело ответа на отвергнутый ввод — общее для всех пяти целей. У pydantic
# формат ошибки принципиально свой (список объектов с типом, путём и
# контекстом), и приводить остальных к нему значило бы заставить их делать
# работу, которой у них нет. Текст ошибки предметом замера не является —
# важно, что ввод ОТВЕРГНУТ, и это проверяется статусом.
ERROR_BODY = {"detail": "invalid"}
ERROR_STATUS = 422


class ValidationError(ValueError):
    """Ошибка проверки входных данных. Одна на все три приложения."""


def validate_echo(payload):
    """Проверки, одинаковые по объёму для всех трёх приложений.

    FastAPI делает то же самое через pydantic — это его идиома и его цена,
    которую мы и хотим измерить. Flask и Django проверяют вручную здесь,
    чтобы количество и строгость проверок совпадали. Асимметрия намеренная и
    названа: сравнивается «одинаковая гарантия каждым своим способом», а не
    «кто меньше проверяет».
    """
    if not isinstance(payload, dict):
        raise ValidationError("body must be an object")

    unknown = set(payload) - {"id", "name", "tags"}
    if unknown:
        raise ValidationError(f"unknown fields: {sorted(unknown)}")

    if "id" not in payload or "name" not in payload or "tags" not in payload:
        raise ValidationError("id, name and tags are required")

    ident = payload["id"]
    if not isinstance(ident, int) or isinstance(ident, bool):
        raise ValidationError("id must be an integer")
    if ident < 1:
        raise ValidationError("id must be positive")

    name = payload["name"]
    if not isinstance(name, str):
        raise ValidationError("name must be a string")
    if not 1 <= len(name) <= 64:
        raise ValidationError("name length must be 1..64")

    tags = payload["tags"]
    if not isinstance(tags, list):
        raise ValidationError("tags must be a list")
    if len(tags) > 16:
        raise ValidationError("at most 16 tags")
    for tag in tags:
        if not isinstance(tag, str):
            raise ValidationError("tags must be strings")

    return ident, name, tags


def cpu_task(steps: int = CPU_STEPS) -> int:
    """Счётная работа на чистом Python-байткоде.

    Библиотечные функции не годятся: многие отпускают GIL, и замер показал бы
    поведение библиотеки, а не счётную нагрузку. Детерминирована, поэтому
    контрольная сумма в ответе доказывает, что все пять целей посчитали одно
    и то же — иначе побайтовое сравнение проверяло бы только форму.
    """
    x = 1
    for i in range(steps):
        x = (x * 1103515245 + 12345 + i) & _MASK
        x ^= x >> 7
    return x


def cpu_body(ident, name, tags):
    """Тело ответа /cpu: то же, что у /echo, плюс контрольная сумма."""
    body = echo_body(ident, name, tags)
    body["checksum"] = cpu_task()
    return body


def echo_body(ident, name, tags):
    """Тело ответа. Порядок ключей фиксирован построением."""
    return {
        "id": ident,
        "name": name,
        "tags": list(tags),
        "tag_count": len(tags),
        "name_length": len(name),
    }


def dumps(body):
    """Сериализация для тех плеч, где мы её задаём сами (голые ASGI и WSGI).

    Фреймворки сериализуют СВОИМИ средствами — это часть измеряемой работы.
    Настройки у них выставлены так, чтобы байты совпали с этой функцией:
    компактные разделители, без сортировки ключей, без переноса в конце.
    """
    return json.dumps(body, separators=(",", ":"), ensure_ascii=False)
