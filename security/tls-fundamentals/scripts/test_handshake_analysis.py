"""Число сообщений рукопожатия: что подтверждено, а что нет.

Считаются сообщения, а не время и не пакеты. Время зависит от машины,
пакеты — от сети; сообщения задаёт протокол.

Здесь же закреплён вывод, ОПРОВЕРГНУВШИЙ ожидание. Ждали, что новая
версия заметно короче по числу сообщений. Выигрыш оказался в ОДНО
сообщение. Существенная разница — в пересылках, то есть в числе ожиданий
ответа через сеть; именно она стоит времени, и именно её надо называть.

Отдельная история — граница счёта. Сперва считалось «до завершающего
сообщения клиента», и версии выходили равными: в старой клиент
заканчивает РАНЬШЕ сервера, в новой позже, и у старой из счёта выпадал
ответ сервера. Числа были верные, граница — нет. Проверка ниже требует,
чтобы обе стороны завершили.
"""

import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _runner():
    path = os.path.join(HERE, "scripts/run-handshake.py")
    spec = importlib.util.spec_from_file_location("run_handshake", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def rows():
    with io.open(os.path.join(HERE, "fixtures/handshake.jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise AssertionError("фикстура оборвана")
    return [json.loads(l) for l in text.split("\n") if l.strip().startswith("{")]


def first():
    out = {}
    for row in rows():
        out.setdefault(row["case"], row)
    return out


class Reproducibility(unittest.TestCase):
    def test_both_runs_agree_exactly(self):
        """Не усреднение, а проверка: число не должно плавать."""
        by_case = {}
        for row in rows():
            by_case.setdefault(row["case"], []).append(row["messages"])
        for case, seen in by_case.items():
            self.assertEqual(len(seen), 2, "%s: прогонов не два" % case)
            self.assertEqual(seen[0], seen[1],
                             "%s: прогоны разошлись — считается не то, что "
                             "мы думаем" % case)


class Boundary(unittest.TestCase):
    def test_both_sides_finished(self):
        """Иначе сравнение версий нечестное.

        В старой версии клиент завершает раньше сервера. Оборвав счёт на
        клиенте, мы срезали бы у неё одно сообщение и получили равенство
        там, где его нет.
        """
        for case, row in first().items():
            sides = {m.split(":")[0] for m in row["messages"]
                     if m.endswith("Finished")}
            self.assertEqual(sides, {"клиент", "сервер"},
                             "%s: завершили не обе стороны — граница счёта "
                             "срезает часть рукопожатия" % case)

    def test_tickets_are_counted_apart(self):
        # Билеты приходят уже ПОСЛЕ рукопожатия, и их число задаёт
        # настройка сервера, а не версия протокола.
        for case, row in first().items():
            self.assertNotIn("NewSessionTicket", " ".join(row["messages"]),
                             "%s: билет попал в счёт рукопожатия" % case)


class WhatTheNumbersShow(unittest.TestCase):
    def test_the_gain_in_messages_is_small(self):
        """Ожидание «заметно короче» опровергнуто — закрепляем это."""
        data = first()
        gain = (data["full_12"]["messages_total"]
                - data["full_13"]["messages_total"])
        self.assertEqual(gain, 1,
                         "выигрыш по сообщениям изменился — вывод статьи "
                         "надо переписывать, а не подгонять")

    def test_the_gain_in_flights_is_where_it_matters(self):
        data = first()
        self.assertLess(data["full_13"]["flights"], data["full_12"]["flights"],
                        "по пересылкам выигрыша нет — тогда рассказывать "
                        "про скорость не о чем")

    def test_resumption_drops_exactly_the_certificate_messages(self):
        data = first()
        full = [m.split(": ", 1)[1] for m in data["full_13"]["messages"]]
        short = [m.split(": ", 1)[1] for m in data["resumed_13"]["messages"]]
        dropped = sorted(m for m in full if m not in short)
        self.assertEqual(dropped, ["Certificate", "CertificateVerify"],
                         "возобновление убирает не то, что мы думаем: вывод "
                         "«не повторяется проверка сертификатной цепочки» "
                         "держится именно на этом составе")


class WordingIsTheSameEverywhere(unittest.TestCase):
    """Одна формулировка о возобновлении на всех поверхностях.

    Статью однажды поправили, а стенд остался с прежним и неверным
    тезисом: будто при возобновлении подлинность сервера не проверяется
    вовсе. Сервер при возобновлении не остаётся неподтверждённым — он
    подтверждает себя ключом, связанным с исходным соединением.

    Формулировка пережила правку в README, в expected.json, в печати
    разбора и в тесте. Тест при этом ЗАЩИЩАЛ ошибку. Поэтому проверка
    теперь смотрит на все поверхности сразу.
    """

    SURFACES = ("README.md", "expected.json", "scripts/analyze-handshake.py",
                "scripts/test_handshake_analysis.py")

    # Строка собирается из кусков нарочно: иначе страж находил бы сам
    # себя и краснел на собственном определении.
    WRONG = "пропускает " + "проверку подлинности"

    @staticmethod
    def _flat(path):
        """Текст со схлопнутыми пробелами.

        Без этого поиск слепнет на переносе строки — и слеп: отозванная
        формулировка уцелела в README ровно потому, что была разбита
        переводом строки, а поиск шёл по точной строке. Ошибка того же
        рода, что и в самих проверках статьи.
        """
        with io.open(path, encoding="utf-8") as handle:
            return " ".join(handle.read().split())

    def test_the_retracted_wording_is_gone_everywhere(self):
        for name in self.SURFACES:
            self.assertNotIn(
                self.WRONG, self._flat(os.path.join(HERE, name)),
                "%s: вернулась отозванная формулировка о возобновлении" % name)

    def test_every_surface_states_it_the_same_way(self):
        marker = "не повторяет проверку сертификатной цепочки"
        for name in ("README.md", "expected.json"):
            self.assertIn(
                marker, self._flat(os.path.join(HERE, name)),
                "%s: нет общей формулировки о возобновлении" % name)


class CounterDiversions(unittest.TestCase):
    """Счётчик обязан краснеть на подставных данных."""

    def test_counter_stops_only_after_both_sides_finish(self):
        count = _runner().count
        raw = "\n".join([
            ">>> TLS 1.3, Handshake [length 0001], ClientHello",
            "<<< TLS 1.3, Handshake [length 0001], ServerHello",
            ">>> TLS 1.3, Handshake [length 0001], Finished",
            "<<< TLS 1.3, Handshake [length 0001], Finished",
            "<<< TLS 1.3, Handshake [length 0001], ЛишнееПослеКонца",
        ])
        messages, _ = count(raw)
        self.assertEqual(len(messages), 4,
                         "счёт не остановился там, где рукопожатие кончилось")

    def test_counter_does_not_stop_at_the_first_finished(self):
        count = _runner().count
        raw = "\n".join([
            ">>> TLS 1.2, Handshake [length 0001], ClientHello",
            ">>> TLS 1.2, Handshake [length 0001], Finished",
            "<<< TLS 1.2, Handshake [length 0001], Finished",
        ])
        messages, _ = count(raw)
        self.assertEqual(len(messages), 3,
                         "счёт оборвался на первом завершении — ровно та "
                         "ошибка, из-за которой версии выходили равными")

    def test_tickets_never_enter_the_count(self):
        count = _runner().count
        raw = "\n".join([
            ">>> TLS 1.3, Handshake [length 0001], ClientHello",
            "<<< TLS 1.3, Handshake [length 0001], Finished",
            ">>> TLS 1.3, Handshake [length 0001], Finished",
            "<<< TLS 1.3, Handshake [length 0001], NewSessionTicket",
        ])
        messages, tickets = count(raw)
        self.assertEqual(len(messages), 3)
        self.assertEqual(tickets, 1)

    def test_flights_count_direction_changes(self):
        flights = _runner().flights
        self.assertEqual(flights([("клиент", "a"), ("сервер", "b"),
                                  ("сервер", "c"), ("клиент", "d")]), 3)
        self.assertEqual(flights([("клиент", "a"), ("клиент", "b")]), 1)


if __name__ == "__main__":
    unittest.main()
