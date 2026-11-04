"""Контракт строки, которую печатает любая проба стенда."""

import io
import json

OUTCOMES = ("resolved", "refused", "error")
FIELDS = ("kind", "variant", "probe", "outcome", "detail")


def parse_line(line):
    row = json.loads(line)
    for field in FIELDS:
        if field not in row:
            raise ValueError("нет обязательного поля %s" % field)
    if row["outcome"] not in OUTCOMES:
        raise ValueError("недопустимый исход %r" % row["outcome"])
    if row["outcome"] != "resolved" and not str(row["detail"]).strip():
        raise ValueError("исход %r обязан нести объяснение" % row["outcome"])
    return row


def _has_end_marker(text):
    """Маркер завершения обязан быть В КОНЦЕ файла, а не где угодно.

    Слово COMPLETE, случайно оказавшееся внутри значения какого-то поля
    (например, в detail), не должно засчитываться за маркер: тогда файл
    из одной оборванной строки данных выглядел бы как полный прогон.
    """
    return "\nCOMPLETE\n" in text or text.rstrip().endswith("COMPLETE")


def read_fixture(path):
    """Читает фикстуру целиком, требуя маркер завершения.

    Оборванная фикстура — прогон, упавший посередине. Разбирать её нельзя:
    получится частичная картина, выглядящая как полная. Пустой результат
    (маркер есть, а строк данных нет) — тоже ошибка: проверка, зелёная при
    отсутствии предмета проверки, не работает.
    """
    with io.open(path, encoding="utf-8") as handle:
        text = handle.read()
    if not _has_end_marker(text):
        raise ValueError("%s: нет маркера завершения" % path)
    rows = [parse_line(l) for l in text.split("\n") if l.strip().startswith("{")]
    if not rows:
        raise ValueError("%s: нет ни одной строки данных" % path)
    return rows


def read_measurements(path):
    """Строки без исхода: время, размеры, суммы.

    Исход бывает не у всего. У упаковки он есть — имя разрешилось
    или нет. У времени сборки исхода нет, есть величина. Требовать
    поле outcome от такой строки значило бы его выдумывать, а
    ослабить ради неё контракт — потерять его там, где он нужен.

    Маркер завершения требуется и здесь: оборванный прогон не
    должен выглядеть как полный. Пустой результат — тоже ошибка, той же
    причине, что и в read_fixture.
    """
    with io.open(path, encoding="utf-8") as handle:
        text = handle.read()
    if not _has_end_marker(text):
        raise ValueError("%s: нет маркера завершения" % path)
    rows = [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]
    if not rows:
        raise ValueError("%s: нет ни одной строки данных" % path)
    for row in rows:
        for field in ("kind", "variant"):
            if field not in row:
                raise ValueError("нет обязательного поля %s" % field)
    return rows
