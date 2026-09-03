"""Каждое число README обязано находиться в фикстурах.

README пишется по памяти о числах, и это последнее место, где НАШЕ
поведение может проскочить как свойство протокола. Достаточно один раз
округлить, переставить или вспомнить не то — и в тексте появится число,
которого никто не мерил. Выглядеть оно будет ровно так же, как
измеренное.

Поэтому проверка механическая: все числа README собираются и сверяются с
теми, что встречаются в фикстурах либо считаются из них напрямую.

Числа, которые НЕ являются результатами, перечислены явным списком с
объяснением. Список — это признание, а не лазейка: каждая запись в нём
говорит, откуда число взялось, и его нельзя добавить молча.

ЧТО ЭТА ПРОВЕРКА ЛОВИТ, А ЧТО НЕТ.

Ловит ВЫДУМАННОЕ число — такое, которого нет ни в одной фикстуре.
Проверено диверсиями: подставленные 47 и 1553 покраснели.

НЕ ловит верное число, поставленное не туда. Подстановка «выигрыш в 12
сообщений» прошла незамеченной: 12 в фикстурах есть — столько расширений
у одного из клиентов. Это ограничение, а не недосмотр: сверка по
множеству чисел не может знать, о чём число говорит. За смысл отвечает
разбор каждой оси и чтение глазами.

Отдельно из сбора исключены поля с сырыми байтами. Пока они входили,
проверка была почти пустой: в тысячах шестнадцатеричных цифр находится
почти любое число, и первая же диверсия прошла незамеченной.
"""

import io
import json
import os
import re
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
README = os.path.join(HERE, "README.md")
FIXTURES = os.path.join(HERE, "fixtures")

# Версии вида 3.5.8 берутся целиком: разбитые на «3.5» и «8» они
# превращались бы в два числа, которых никто не мерил.
NUMBER = re.compile(r"\d+(?:[.,]\d+)*")

# Числа, которые не являются результатами замеров. Каждое — с
# объяснением, откуда взялось.
NOT_MEASUREMENTS = {
    "1": "номер версии, счёт «один из», порядковые",
    "2": "порядковые, число прогонов",
    "3": "порядковые",
    "4": "число клиентов и число осей стенда",
    "0": "нулевое смещение, порядковые",
    "1.0": "название версии протокола",
    "1.1": "название версии протокола",
    "1.2": "название версии протокола",
    "1.3": "название версии протокола",
    "8.18.0": "версия образа curl",
    "3.5.8": "версия образа openssl",
    "25": "версия образа Java",
    "1.27": "версия Go",
    "3.22": "версия базового образа",
    "2026": "опорная дата выпуска сертификатов",
    "3.0": "версия OpenSSL, на которой выпуск НЕ работает — названа как объяснение, почему нужен образ",
    "3.8": "нижняя граница версии Python, объявленная зависимость",
    "1460": "ОТОЗВАННОЕ число: обрезанное чтение из сокета, приведено как объяснение дефекта замера, а не как результат",
    "20260101000000": "опорная дата выпуска сертификатов",
}


# Поля с сырыми байтами. Числа из них НЕ считаются доказательством:
# это тысячи цифр подряд, в которых находится почти всё что угодно.
# Пока они входили в сбор, проверка пропускала выдуманное число, не
# моргнув, — диверсия это и показала.
BYTE_DUMPS = ("hex", "random", "downgrade_tail", "detail", "evidence",
              "server_saw", "version", "note", "how", "features_line")


def _numbers_from(value):
    """Числа из смыслового значения, но не из сырых байт."""
    if isinstance(value, bool):
        return set()
    if isinstance(value, (int, float)):
        return {str(value)}
    if isinstance(value, str):
        return set(NUMBER.findall(value))
    if isinstance(value, list):
        found = set()
        for item in value:
            found |= _numbers_from(item)
        return found
    return set()


def fixture_numbers():
    """Числа из фикстур плюс считаемые из них напрямую."""
    found = set()
    for name in sorted(os.listdir(FIXTURES)):
        if not name.endswith(".jsonl"):
            continue
        with io.open(os.path.join(FIXTURES, name), encoding="utf-8") as handle:
            text = handle.read()
        rows = [json.loads(l) for l in text.split("\n")
                if l.strip().startswith("{")]

        for row in rows:
            for key, value in row.items():
                if key in BYTE_DUMPS:
                    continue
                found |= _numbers_from(value)

        found.add(str(len(rows)))
        cases = {r["case"] for r in rows if "case" in r}
        clients = {r["client"] for r in rows if "client" in r}
        for group in (cases, clients):
            if group:
                found.add(str(len(group)))
        if cases and clients:
            found.add(str(len(cases) * len(clients)))
            found.add(str(len([c for c in cases if "valid" not in c])))
        for outcome in ("rejected", "connected"):
            hits = [r for r in rows if r.get("outcome") == outcome]
            if hits:
                found.add(str(len(hits)))
        supported = [r for r in rows if r.get("supported")]
        if supported:
            found.add(str(len(supported)))
        # Сколько РАЗЛИЧИМЫХ сообщений у каждого клиента на отказах: это
        # ключевое число оси о цепочке, и считается оно здесь же.
        for client in clients:
            said = {r.get("detail", "") for r in rows
                    if r.get("client") == client
                    and r.get("outcome") == "rejected"}
            if said:
                found.add(str(len(said)))

    with io.open(os.path.join(HERE, "pki/cases.json"), encoding="utf-8") as h:
        cases = json.loads(h.read())
    found.add(str(len(cases)))
    found.add(str(len([c for c in cases if c["breaks"]])))
    return found


def readme_numbers():
    with io.open(README, encoding="utf-8") as handle:
        text = handle.read()
    # Числа внутри блоков кода — это команды и пути, не утверждения.
    text = re.sub(r"```.*?```", " ", text, flags=re.S)
    return NUMBER.findall(text)


class ReadmeNumbers(unittest.TestCase):
    def test_readme_exists(self):
        self.assertTrue(os.path.exists(README), "README не написан")

    def test_every_number_is_backed_by_a_fixture(self):
        allowed = fixture_numbers() | set(NOT_MEASUREMENTS)
        unknown = sorted({n for n in readme_numbers() if n not in allowed})
        self.assertEqual(
            unknown, [],
            "числа из README не найдены ни в одной фикстуре и не объявлены "
            "не-замерами: %r. Либо число померено и должно попасть в "
            "фикстуру, либо оно не результат и обязано быть названо в "
            "списке с объяснением." % unknown)

    def test_the_exception_list_explains_itself(self):
        for number, why in NOT_MEASUREMENTS.items():
            self.assertTrue(why.strip(),
                            "%s внесён в список без объяснения" % number)


class ReadmeSaysWhatWasNotMeasured(unittest.TestCase):
    """Ограничения метода обязаны быть названы, а не подразумеваться."""

    REQUIRED = [
        ("выпущены нами", "сертификаты выпущены нами и датированы жёстко"),
        ("передаётся клиентам явно", "доверенный центр не берётся из системы"),
        ("не мерил", "сказано, чего стенд не мерил"),
    ]

    def test_limits_section_names_the_limits(self):
        with io.open(README, encoding="utf-8") as handle:
            text = handle.read().lower()
        for marker, what in self.REQUIRED:
            # assertIn печатал бы весь README целиком; проверяем сами.
            self.assertTrue(marker.lower() in text,
                            "в README не сказано: %s" % what)


if __name__ == "__main__":
    unittest.main()
