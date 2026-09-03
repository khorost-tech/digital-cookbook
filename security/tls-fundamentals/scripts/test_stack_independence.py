"""Клиентов четыре, независимых реализаций меньше — и это надо считать.

Статья говорила о «четырёх независимых реализациях». Клиентов
действительно четыре, но `openssl` и `curl` работают на одной и той же
библиотеке, просто разных её версий. Независимых семей три.

Это не мелочь формулировки. Главное число оси — сколько различимых
причин отказа даёт клиент: 6, 6, 4, 4. Читалось как «двое против
двоих», а на деле это ОДНА СЕМЬЯ против двух: совпадение первых двух
объясняется общей библиотекой, а не независимым подтверждением.

Проверка требует, чтобы число семей БРАЛОСЬ ИЗ ФИКСТУРЫ, а не из памяти
автора, и чтобы совпадение внутри семьи не выдавали за независимое.
"""

import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def rows(name):
    with io.open(os.path.join(HERE, "fixtures", name + ".jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise AssertionError("%s: фикстура оборвана" % name)
    return [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]


def families():
    out = {}
    for row in rows("ech"):
        out.setdefault(row["tls_stack"], []).append(row["client"])
    return out


class Stacks(unittest.TestCase):
    def test_every_client_names_its_stack(self):
        for row in rows("ech"):
            self.assertNotEqual(
                row["tls_stack"], "неизвестно",
                "%s: не удалось определить семью по строке версии %r"
                % (row["client"], row["version"]))
            self.assertTrue(str(row["tls_stack_version"]).strip(),
                            "%s: не записана версия библиотеки" % row["client"])

    def test_there_are_fewer_families_than_clients(self):
        """Ровно то, что было завышено в статье."""
        clients = {r["client"] for r in rows("ech")}
        self.assertLess(
            len(families()), len(clients),
            "семей столько же, сколько клиентов — тогда формулировку "
            "«четыре независимые реализации» надо вернуть, а сейчас она "
            "неверна")

    def test_openssl_and_curl_share_a_family(self):
        shared = [name for name, members in families().items()
                  if len(members) > 1]
        self.assertEqual(len(shared), 1, "ожидалась ровно одна общая семья")
        self.assertEqual(sorted(families()[shared[0]]), ["curl", "openssl"],
                         "общая семья не у той пары, что ожидалась")

    def test_the_shared_family_still_differs_in_version(self):
        """Одна семья — не значит один и тот же код.

        Версии библиотеки у двух клиентов разные, и это стоит сказать: они
        не полностью избыточны, но и не независимы.
        """
        by_client = {r["client"]: r["tls_stack_version"] for r in rows("ech")}
        self.assertNotEqual(
            by_client["openssl"], by_client["curl"],
            "версии библиотеки совпали — тогда двух клиентов этой семьи "
            "надо считать одним замером")


class DistinctReasonsReadInFamilies(unittest.TestCase):
    """Совпадение внутри семьи нельзя выдавать за независимое."""

    def test_clients_of_one_family_agree_on_distinct_reasons(self):
        chain = rows("chain")
        counts = {}
        for row in chain:
            if row["outcome"] != "rejected":
                continue
            counts.setdefault(row["client"], set()).add(row["detail"])
        shared = [members for members in families().values()
                  if len(members) > 1][0]
        sizes = {len(counts[client]) for client in shared}
        self.assertEqual(
            len(sizes), 1,
            "клиенты одной семьи дали РАЗНОЕ число различимых причин %r — "
            "значит семья не объясняет совпадения, и вывод надо строить "
            "заново" % {c: len(counts[c]) for c in shared})


if __name__ == "__main__":
    unittest.main()
