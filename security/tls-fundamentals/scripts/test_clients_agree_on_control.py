"""На контрольном случае все четверо обязаны сказать «соединился».

Это условие допуска, а не украшение. Отказ клиента на сломанном случае
доказывает что-то ТОЛЬКО если известно, что на целой цепочке этот же
клиент соединяется. Иначе отказ может объясняться его собственной
настройкой: своим хранилищем доверенных центров, требованием проверки
отзыва, недоступным портом.

Проверка уже окупилась дважды. Хостовый curl под Windows собран с
системным движком и отказал ЦЕЛОЙ цепочке — требовал состояния отзыва,
которого для частного центра нет. Без этого теста вся его колонка стала
бы ложными отказами, и выглядели бы они правдоподобно. Второй раз — тот
же curl спотыкался уже ПОСЛЕ рукопожатия, потому что сервер отвечал не
по HTTP; жалоба выглядела как отказ TLS.

Отдельно проверяется, что клиент не штампует «соединился» на чём
угодно: на чужом центре он обязан отказать. Без этой диверсии тест
согласия проходил бы и на четырёх сломанных клиентах.
"""

import unittest

from scripts import clients
from scripts.outcome import parse_line

PORT = 18461

# Чьё сообщение об ошибке называет проверяемое имя.
#
# openssl сюда не входит: он печатает «hostname mismatch» и не говорит,
# какое имя не совпало. Это его свойство, проверенное прогоном, а не наш
# недосмотр, — и заодно первое наблюдаемое различие между клиентами.
# Дописывать имя в detail из скрипта нельзя: тогда проверка сверяла бы
# наше же эхо, а не то, что увидел клиент.
NAMES_THE_HOST = ("curl", "go", "java")


class ControlAgreement(unittest.TestCase):
    def test_all_four_connect_on_valid_chain(self):
        with clients.Server("valid", PORT):
            rows = {name: parse_line(clients.run_client(name, "valid", PORT))
                    for name in clients.CLIENTS}
        for name, row in rows.items():
            self.assertEqual(
                row["outcome"], "connected",
                "%s не прошёл контроль (%s): до устранения его строки в "
                "матрице недействительны" % (name, row["detail"]))

    def test_every_client_reports_its_own_name(self):
        # Без этого две строки одного клиента могли бы выдать себя за
        # согласие четверых.
        with clients.Server("valid", PORT + 1):
            names = {parse_line(clients.run_client(name, "valid", PORT + 1))["client"]
                     for name in clients.CLIENTS}
        self.assertEqual(names, set(clients.CLIENTS))

    def test_each_client_checks_the_name_we_asked_about(self):
        """У заведомо чужого имени клиент обязан назвать ИМЕННО его.

        Диверсия, поймавшая настоящий дефект. Java соединялась по адресу
        host.docker.internal, а имя для проверки брала оттуда же — то
        есть сверяла сертификат с именем, которого мы не запрашивали.
        Контрольная строка при этом была зелёной: контроль проходил по
        неверной причине, и по цепочке отказов этого было не видно.

        Отказ сам по себе ничего не доказывает: надо, чтобы клиент
        отказал ПО ТОЙ ПРИЧИНЕ, о которой мы спрашивали.
        """
        bogus = "bogus.local"
        with clients.Server("valid", PORT + 3):
            for name in clients.CLIENTS:
                row = parse_line(clients.run_client(
                    name, "valid", PORT + 3, servername=bogus))
                self.assertEqual(row["outcome"], "rejected",
                                 "%s принял чужое имя: %r" % (name, row))
                if name in NAMES_THE_HOST:
                    self.assertIn(
                        bogus, row["detail"],
                        "%s отказал, но назвал не то имя, о котором его "
                        "спрашивали: %r" % (name, row["detail"]))
                else:
                    self.assertIn("hostname", row["detail"].lower(),
                                  "%s отказал не из-за имени: %r"
                                  % (name, row["detail"]))

    def test_none_of_them_is_a_rubber_stamp(self):
        # Диверсия: цепочка подписана чужим центром. Клиент, который и
        # здесь скажет «соединился», не проверяет ничего, и его согласие
        # на контроле ничего не стоит.
        with clients.Server("other_ca", PORT + 2):
            rows = {name: parse_line(clients.run_client(name, "other_ca", PORT + 2))
                    for name in clients.CLIENTS}
        for name, row in rows.items():
            self.assertEqual(row["outcome"], "rejected",
                             "%s не отверг чужой центр: %r" % (name, row))


if __name__ == "__main__":
    unittest.main()
