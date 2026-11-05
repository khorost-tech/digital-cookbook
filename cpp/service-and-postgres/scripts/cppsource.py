"""Разбор исходника C++ настолько, насколько нужен проверкам.

ЗАЧЕМ ЭТО ЕСТЬ. Первая редакция проверок искала подстроку по всему файлу
и держала ноль из трёх заявленных свойств: ревьюер провёл шесть диверсий,
и все шесть прошли зелёными. Причина одна — файл насыщен комментариями,
объясняющими ровно те свойства, которые проверки искали. Убранный из
строки подключения `application_name` оставался «найденным» в
комментарии, который рассказывал, почему он там обязан быть.

Отсюда два правила, на которых стоит весь модуль:

1. **комментарии вырезаются до поиска** — объяснение запрета не должно
   сходить за его соблюдение;
2. **ищется в теле функции, а не в файле** — свойство принадлежит месту,
   и найденное в соседней функции ничего о нём не говорит.

Полноценного разбора C++ здесь нет и не нужно: нужен лексер, который
знает про строковые литералы, чтобы не считать кавычку в комментарии за
начало строки, а фигурную скобку внутри литерала — за конец тела.
"""

import re


def strip_comments(text):
    """Вырезает `//` и `/* */`, не трогая строковые и символьные литералы.

    Комментарии заменяются пробелами, а не удаляются: так номера строк и
    расстояния между лексемами не едут, и сообщение упавшей проверки
    остаётся читаемым.
    """
    out = []
    i = 0
    size = len(text)
    while i < size:
        ch = text[i]
        if ch == '"' or ch == "'":
            quote = ch
            out.append(ch)
            i += 1
            while i < size:
                out.append(text[i])
                if text[i] == "\\" and i + 1 < size:
                    out.append(text[i + 1])
                    i += 2
                    continue
                if text[i] == quote:
                    i += 1
                    break
                i += 1
            continue
        if ch == "/" and i + 1 < size and text[i + 1] == "/":
            while i < size and text[i] != "\n":
                out.append(" ")
                i += 1
            continue
        if ch == "/" and i + 1 < size and text[i + 1] == "*":
            while i < size and not (text[i] == "*" and i + 1 < size
                                    and text[i + 1] == "/"):
                out.append("\n" if text[i] == "\n" else " ")
                i += 1
            out.append("  ")
            i += 2
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def function_body(text, header_pattern):
    """Возвращает тело функции, чей заголовок сошёлся с header_pattern.

    Скобки внутри строковых литералов не считаются: контрактная строка
    собирается из литералов с фигурными скобками, и наивный счётчик
    оборвал бы тело посреди неё.
    """
    match = re.search(header_pattern, text)
    if match is None:
        raise AssertionError("не найден заголовок функции: %s" % header_pattern)
    start = text.find("{", match.end() - 1)
    if start < 0:
        raise AssertionError("у функции нет тела: %s" % header_pattern)
    depth = 0
    i = start
    size = len(text)
    while i < size:
        ch = text[i]
        if ch == '"' or ch == "'":
            quote = ch
            i += 1
            while i < size:
                if text[i] == "\\":
                    i += 2
                    continue
                if text[i] == quote:
                    break
                i += 1
            i += 1
            continue
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return text[start:i + 1]
        i += 1
    raise AssertionError("тело функции не закрыто: %s" % header_pattern)
