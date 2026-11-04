"""Разбор оси упаковки: контроли обязаны быть зелёными раньше, чем что-либо.

Без контролей (glibc-dynamic, musl-static) отказ старой статики ничего не
доказывает: он неотличим от отказа сети внутри контейнера. Поэтому первая
проверка здесь — не про находку оси, а про то, что находку вообще можно
читать.

Пара про устаревшее правило (glibc-static / glibc-static-old) обязана
разойтись, и разойтись ИМЕННО так, как записано в expected.json: старая
даёт refused с EAI_SYSTEM, новая — resolved.

Чем именно различается пара — проверяется здесь же, но ЗАМЕРОМ, а не
объявлением. У неё различается образ сборки целиком, то есть две
переменные: версия glibc и компилятор. Раньше это держалось на имени
образа в images/variants.json — на договорённости, которую никто не
проверял; версии инструментов на оси упаковки не снимались вовсе.
Теперь они лежат в фикстуре, и ControlledPair ниже читает их оттуда.
"""

import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Путь берётся из окружения, чтобы диверсия работала над КОПИЕЙ
# фикстуры, а не над файлом репозитория, — так же, как на других осях.
FIXTURE = os.environ.get(
    "PACKAGE_FIXTURE", os.path.join(HERE, "fixtures", "package.jsonl"))

from scripts.outcome import read_fixture

CONTROLS = ("glibc-dynamic", "musl-static")
VARIANTS = os.path.join(HERE, "images", "variants.json")


def load():
    with io.open(VARIANTS, encoding="utf-8") as handle:
        return json.loads(handle.read())


def _load_resolve_rows():
    rows = read_fixture(FIXTURE)
    return {r["variant"]: r for r in rows
            if r["kind"] == "package" and r["probe"] == "resolve"}


class Controls(unittest.TestCase):
    """Без зелёных контролей отказ старой статики недоказателен."""

    def test_fixture_exists(self):
        self.assertTrue(os.path.exists(FIXTURE),
                         "нет fixtures/package.jsonl — прогон ещё не снят")

    def test_controls_resolve(self):
        by_variant = _load_resolve_rows()
        for name in CONTROLS:
            self.assertIn(name, by_variant, "нет строки resolve для %s" % name)
            self.assertEqual(
                by_variant[name]["outcome"], "resolved",
                "контроль %s не резолвит (%r) — сеть в контейнере под "
                "подозрением, остальные строки оси недействительны"
                % (name, by_variant[name].get("detail")))


class ExactSizeCompanion(unittest.TestCase):
    """У упакованного размера образа обязан быть точный спутник.

    Упакованный размер сравнивается между платформами С ДОПУСКОМ: он
    расходится на единицы байт из-за битов прав доступа файлов контекста
    сборки. Величина под допуском содержимое не стережёт — собственная
    диверсия стенда показала, что дописанные к бинарнику 8 байт сдвигают
    упакованный размер на 2 байта, причём вниз. Стережёт содержимое
    точно сравниваемая размерная величина рядом: объём файлов итоговой
    файловой системы образа.
    """

    def _healthz_rows(self):
        rows = read_fixture(FIXTURE)
        return {r["variant"]: r for r in rows
                if r["kind"] == "package" and r["probe"] == "healthz"}

    def test_every_variant_reports_the_unpacked_image_size(self):
        rows = self._healthz_rows()
        self.assertTrue(rows, "в фикстуре нет ни одной строки healthz")
        for name, row in sorted(rows.items()):
            self.assertIn(
                "image_unpacked_bytes", row,
                "%s: нет распакованного объёма образа — у упакованного "
                "размера, сравниваемого с допуском, не осталось точного "
                "спутника" % name)
            self.assertIsInstance(row["image_unpacked_bytes"], int)

    def test_the_unpacked_size_is_not_the_packed_one(self):
        """Две шкалы обязаны быть разными числами.

        Если бы распакованный объём снимался тем же способом, что и
        упакованный (docker image inspect), спутник был бы копией
        стерегомой величины и не стерёг бы ничего.
        """
        rows = self._healthz_rows()
        self.assertTrue(rows, "в фикстуре нет ни одной строки healthz")
        for name, row in sorted(rows.items()):
            self.assertNotEqual(
                row["image_unpacked_bytes"], row["image_bytes"],
                "%s: распакованный объём равен упакованному — величина "
                "снята тем же измерителем и спутником не является" % name)

    def test_the_unpacked_size_covers_the_binary(self):
        """Бинарник лежит в образе, значит объём образа не меньше него."""
        rows = self._healthz_rows()
        self.assertTrue(rows, "в фикстуре нет ни одной строки healthz")
        for name, row in sorted(rows.items()):
            self.assertGreaterEqual(
                row["image_unpacked_bytes"], row["binary_bytes"],
                "%s: распакованный объём образа меньше лежащего в нём "
                "бинарника — величина снята не с содержимого" % name)


class ToolVersions(unittest.TestCase):
    """Версии инструментов сняты замером, а не выведены из имени образа.

    Ось сборки снимает `g++ --version` и `cmake --version` в каждом
    собранном образе и объясняет почему: договорённость, которую никто
    не проверяет, ломается молча. Ось упаковки не снимала ничего — и
    именно поэтому утверждение «у пары различается ровно версия
    библиотеки» прожило до вычитки.
    """

    def _healthz_rows(self):
        rows = read_fixture(FIXTURE)
        return {r["variant"]: r for r in rows
                if r["kind"] == "package" and r["probe"] == "healthz"}

    def test_every_variant_reports_its_tool_versions(self):
        rows = self._healthz_rows()
        self.assertTrue(rows, "в фикстуре нет ни одной строки healthz")
        for name, row in sorted(rows.items()):
            for field in ("cxx_version", "cmake_version", "libc_version"):
                self.assertIn(
                    field, row,
                    "%s: версия инструмента %s не снята — различия между "
                    "вариантами снова держатся на имени образа в "
                    "variants.json" % (name, field))
                self.assertTrue(
                    any(ch.isdigit() for ch in str(row[field])),
                    "%s: %s = %r, в величине нет ни одной цифры — версией "
                    "это не является" % (name, field, row[field]))

    def test_variants_sharing_a_builder_report_the_same_versions(self):
        """Измеритель следует за образом, а не отвечает всегда одно и то же.

        Пять вариантов из шести делят три базовых образа: gcc:15 у
        glibc-dynamic, glibc-static и distroless, alpine:3.22 у пары
        musl. Компилятор и libc приходят ИЗ БАЗОВОГО ОБРАЗА, и внутри
        такой группы они обязаны совпасть — иначе измерено не то, что
        называется.

        cmake сюда НЕ входит, и это не поблажка: его каждый Dockerfile
        ставит сам. У glibc-static стоит 3.28.3 с релиза Kitware — ровно
        затем, чтобы совпасть с glibc-static-old, где пакетный cmake
        bullseye (3.18) слишком стар; у соседей по базовому образу
        остаётся пакетный 3.31.6. Требовать совпадения cmake внутри
        группы значило бы требовать поломки той единственной вещи,
        которую пара удерживает вручную.
        """
        variants = {v["name"]: v for v in load()}
        rows = self._healthz_rows()
        self.assertTrue(rows, "в фикстуре нет ни одной строки healthz")
        by_builder = {}
        for name, row in rows.items():
            by_builder.setdefault(variants[name]["builder"], {})[name] = row
        for builder, group in sorted(by_builder.items()):
            for field in ("cxx_version", "libc_version"):
                values = {n: r[field] for n, r in group.items()}
                self.assertEqual(
                    len(set(values.values())), 1,
                    "базовый образ сборки %s один, а %s разошлась: %r"
                    % (builder, field, values))


class ControlledPair(unittest.TestCase):
    """Чем различается пара про устаревшее правило — по замеру.

    Утверждение статьи: различается ОБРАЗ СБОРКИ ЦЕЛИКОМ, то есть версия
    glibc И компилятор; удержано равным всё, что можно удержать, включая
    версию cmake. Первое обязано быть видно в фикстуре, второе —
    проверено, а не объявлено.
    """

    def _healthz(self, name):
        rows = read_fixture(FIXTURE)
        for row in rows:
            if row["kind"] == "package" and row["probe"] == "healthz"                     and row["variant"] == name:
                return row
        raise AssertionError("нет строки healthz для %s" % name)

    def test_the_pair_shares_the_cmake_version(self):
        """Единственный инструмент пары, равенство которого сделано вручную.

        cmake 3.28.3 ставится в обе половины одинаковым способом вместо
        пакетного (3.18 в bullseye, 3.31.6 в trixie). Из образа сборки
        это равенство НЕ следует — значит, сломаться оно может молча, и
        проверять его обязан замер, а не комментарий.
        """
        new = self._healthz("glibc-static")
        old = self._healthz("glibc-static-old")
        self.assertEqual(
            new["cmake_version"], old["cmake_version"],
            "версия cmake разошлась у половин пары (%r против %r) — у пары "
            "появилась третья переменная, не следующая из образа сборки"
            % (new["cmake_version"], old["cmake_version"]))

    def test_the_pair_differs_in_compiler_and_libc(self):
        """Вторая переменная обязана быть ВИДНА в фикстуре.

        Пока версий не снимали, «различается ровно версия библиотеки»
        нельзя было ни подтвердить, ни опровергнуть замером: имя образа
        в variants.json одинаково молчит и о том, и о другом. Эта
        проверка требует, чтобы в фикстуре стояли ДВА различия — версия
        libc и версия компилятора, — и падает, если компилятор у половин
        пары окажется записан одинаковым: тогда утверждение о двух
        переменных надо переписывать, а не оставлять как есть.
        """
        new = self._healthz("glibc-static")
        old = self._healthz("glibc-static-old")
        self.assertNotEqual(
            new["libc_version"], old["libc_version"],
            "версия libc у половин пары совпала — пара перестала быть "
            "парой про версию библиотеки")
        self.assertNotEqual(
            new["cxx_version"], old["cxx_version"],
            "версия компилятора у половин пары совпала. Если это правда, "
            "переменная в паре одна, и статья с оговоркой про две "
            "переменные больше не описывает замер; если это ошибка "
            "измерителя — снята не та величина. Молча оставлять нельзя "
            "ни того, ни другого")


class RulePair(unittest.TestCase):
    """Пара про устаревшее правило обязана разойтись именно так."""

    def test_new_glibc_static_resolves(self):
        by_variant = _load_resolve_rows()
        row = by_variant["glibc-static"]
        self.assertEqual(row["outcome"], "resolved",
                          "новый glibc: ожидался resolved, получили %r"
                          % row)

    def test_old_glibc_static_refused_with_eai_system(self):
        by_variant = _load_resolve_rows()
        row = by_variant["glibc-static-old"]
        self.assertEqual(row["outcome"], "refused",
                          "старый glibc: ожидался refused, получили %r"
                          % row)
        self.assertIn("EAI_SYSTEM", row["detail"],
                       "ожидался символьный код EAI_SYSTEM в detail, "
                       "получили %r" % row["detail"])

    def test_pair_actually_diverges(self):
        by_variant = _load_resolve_rows()
        self.assertNotEqual(by_variant["glibc-static"]["outcome"],
                             by_variant["glibc-static-old"]["outcome"],
                             "пара про правило обязана разойтись — иначе "
                             "оси нечего показывать")


class SecondControl(unittest.TestCase):
    def test_musl_no_resolv_refused_with_eai_again(self):
        by_variant = _load_resolve_rows()
        row = by_variant["musl-no-resolv"]
        self.assertEqual(row["outcome"], "refused")
        self.assertIn("EAI_AGAIN", row["detail"])


class AllSixPresent(unittest.TestCase):
    def test_six_variants_have_resolve_rows(self):
        by_variant = _load_resolve_rows()
        self.assertEqual(
            sorted(by_variant),
            sorted(["glibc-dynamic", "glibc-static", "glibc-static-old",
                    "musl-static", "musl-no-resolv", "distroless"]))

    def test_distroless_resolves(self):
        by_variant = _load_resolve_rows()
        self.assertEqual(by_variant["distroless"]["outcome"], "resolved")


if __name__ == "__main__":
    unittest.main()
