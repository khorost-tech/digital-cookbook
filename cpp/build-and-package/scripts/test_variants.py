"""Шесть вариантов упаковки, и каждый меняет ОДНУ вещь.

Вариант, который меняет две, ничего не доказывает: исход объясняется
двумя причинами сразу, и различить их нечем. Ровно на этом уже
спотыкался стенд TLS, где случай про порядок сертификатов ломал заодно
соответствие ключа.

Пара про устаревшее правило — glibc-static и glibc-static-old —
исключение, и оно названо вслух. У неё различается ОБРАЗ СБОРКИ
ЦЕЛИКОМ (gcc:15 против debian:bullseye), а значит переменных две:
версия glibc и компилятор. Одно и то же у пары — исходник, флаги,
рантайм scratch и место запуска; версия cmake удержана равной вручную
и потому проверяется замером.

Так было не всегда. Проверка ниже называлась «пара различается только
версией библиотеки», а телом сравнивала linkage и runtime и ТРЕБОВАЛА,
чтобы builder различался: она брала носитель второй переменной и
засчитывала его как доказательство того, что переменная одна.
Покраснеть от появления второй переменной такая проверка не могла в
принципе. Имя приведено к тому, что она проверяет, а рядом заведена
честная: у пары различается ровно builder и то, что из него следует.
"""

import io
import json
import os
import subprocess
import time
import unittest
import urllib.error
import urllib.request

from scripts import docker as dockerlib

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAMES = ["glibc-dynamic", "glibc-static", "glibc-static-old",
         "musl-static", "distroless", "musl-no-resolv"]


def load():
    with io.open(os.path.join(HERE, "images/variants.json"),
                 encoding="utf-8") as handle:
        return json.loads(handle.read())


class Variants(unittest.TestCase):
    def test_all_six_present(self):
        self.assertEqual([v["name"] for v in load()], NAMES)

    def test_every_variant_names_its_libc_and_linkage(self):
        for v in load():
            self.assertIn(v["libc"], ("glibc", "musl"))
            self.assertIn(v["linkage"], ("static", "dynamic"))

    def _rule_pair(self):
        return {v["name"]: v for v in load()
                if v["name"] in ("glibc-static", "glibc-static-old")}

    def test_the_rule_pair_shares_linkage_and_runtime_and_swaps_the_builder(
            self):
        """Имя говорит ровно то, что проверяет тело.

        Прежнее имя — «пара различается только версией библиотеки» —
        утверждало обратное тому, что проверялось: требование
        `builder` РАЗЛИЧАЕТСЯ и есть требование второй переменной,
        потому что вместе с образом сборки меняется компилятор.
        """
        pair = self._rule_pair()
        self.assertEqual(pair["glibc-static"]["linkage"],
                         pair["glibc-static-old"]["linkage"])
        self.assertEqual(pair["glibc-static"]["runtime"],
                         pair["glibc-static-old"]["runtime"])
        self.assertNotEqual(pair["glibc-static"]["builder"],
                            pair["glibc-static-old"]["builder"])

    def test_the_rule_pair_differs_in_the_builder_and_in_nothing_else(self):
        """Различаться у пары вправе builder и только то, что из него следует.

        Что из него следует: имя варианта (подпись плеча), файл сборки
        (у каждого образа он свой) и пояснение словами. Всё остальное —
        libc, linkage, runtime, аргументы запуска — обязано совпадать.
        Появись у пары ещё одно различие в объявлении, оно попало бы
        сюда, и проверка покраснела бы; прежняя проверка на такое
        покраснеть не могла.

        Что различие builder тянет за собой ВНУТРИ образов — версию
        glibc и версию компилятора, — эта проверка не видит: она читает
        объявление. Это снимает замер и проверяет ControlledPair в
        scripts/test_package_analysis.py.
        """
        pair = self._rule_pair()
        new, old = pair["glibc-static"], pair["glibc-static-old"]
        self.assertEqual(sorted(new), sorted(old),
                         "у половин пары разный набор полей")
        differing = {k for k in new if new[k] != old[k]}
        self.assertEqual(
            differing, {"name", "dockerfile", "builder", "note"},
            "у пары различается не то, что вправе различаться: %r. "
            "Вправе — builder и то, что из него следует (имя плеча, файл "
            "сборки, пояснение словами)." % sorted(differing))

    def test_every_dockerfile_exists(self):
        for v in load():
            path = os.path.join(HERE, "images", v["dockerfile"])
            self.assertTrue(os.path.exists(path), path)

    def test_every_variant_has_run_args_field(self):
        # Поле обязано быть у ВСЕХ шести, а не только у того варианта,
        # которому есть что туда положить — иначе его легко забыть у
        # следующего варианта с непустым плечом запуска.
        for v in load():
            self.assertIn("run_args", v, v["name"])
            self.assertIsInstance(v["run_args"], list, v["name"])

    def test_no_resolv_pair_differs_only_in_run_args(self):
        # musl-no-resolv когда-то был отдельным образом без
        # /etc/resolv.conf — но Docker подставляет свой resolv.conf в
        # любой контейнер на bridge-сети, так что отсутствие файла в
        # образе ничего не изолирует. Различие обязано быть в ЗАПУСКЕ
        # (подмена файла пустым через run_args), а не в образе: иначе
        # пара снова окажется неотличима от musl-static.
        pair = {v["name"]: v for v in load()
                if v["name"] in ("musl-static", "musl-no-resolv")}
        self.assertEqual(pair["musl-static"]["dockerfile"],
                         pair["musl-no-resolv"]["dockerfile"])
        self.assertEqual(pair["musl-static"]["run_args"], [])
        self.assertNotEqual(pair["musl-no-resolv"]["run_args"], [])


def _get_json(url, attempts=10, delay=0.5, request_timeout=15):
    """Опрашивает /resolve, пока контейнер не поднимется.

    Контейнер стартует не мгновенно — первые попытки соединения будут
    обрываться (ECONNREFUSED), это не ошибка теста, а обычный старт.

    request_timeout взят с запасом (15 c): у musl-no-resolv резолвер
    сам по себе медленный (пустой /etc/resolv.conf — не быстрый отказ,
    а таймаут внутри getaddrinfo), и это не ошибка теста, а часть
    измеряемого поведения.
    """
    last_error = None
    for _ in range(attempts):
        try:
            with urllib.request.urlopen(url, timeout=request_timeout) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except (urllib.error.URLError, ConnectionError, TimeoutError) as exc:
            last_error = exc
            time.sleep(delay)
    raise AssertionError("%s не ответил: %r" % (url, last_error))


class RunVariantReportsOwnName(unittest.TestCase):
    """musl-static и musl-no-resolv делят ОДИН образ (variants.json несёт
    один и тот же dockerfile для обоих, см. Dockerfile.musl-static) — имя
    варианта, запечённое в образ через ENV STAND_VARIANT, у них поэтому
    одинаковое. Без явной `-e STAND_VARIANT=<имя>` при запуске (см.
    scripts/docker.py:run_variant) оба плеча пары называли бы себя первым
    именем, и два разных прогона (с резолвером и без) склеились бы в
    фикстуре в одну строку. Эта проверка поднимает оба плеча вживую и
    смотрит, что каждое называет СЕБЯ, а не соседа.
    """

    @classmethod
    def setUpClass(cls):
        dockerlib.build_variant("musl-static")
        dockerlib.build_variant("musl-no-resolv")

    def _reported_variant(self, name):
        container = dockerlib.run_variant(name, extra_docker_args=["-p", "0:8080"])
        try:
            port_line = subprocess.run(
                ["docker", "port", container, "8080/tcp"],
                check=True, capture_output=True, text=True,
                encoding="utf-8", errors="replace").stdout.strip()
            host_port = port_line.rsplit(":", 1)[-1]
            body = _get_json(
                "http://127.0.0.1:%s/resolve?host=example.com" % host_port)
            return body["variant"]
        finally:
            subprocess.run(["docker", "stop", container],
                            capture_output=True, text=True,
                            encoding="utf-8", errors="replace")

    def test_musl_pair_each_reports_its_own_name(self):
        static_variant = self._reported_variant("musl-static")
        no_resolv_variant = self._reported_variant("musl-no-resolv")
        self.assertEqual(static_variant, "musl-static")
        self.assertEqual(no_resolv_variant, "musl-no-resolv")
        self.assertNotEqual(static_variant, no_resolv_variant)
