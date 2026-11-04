"""Ось 3: воспроизводимость сборки. Сначала кэш, потом уже суммы.

ПОЧЕМУ ПОРЯДОК ИМЕННО ТАКОЙ. Совпадение двух сумм само по себе не значит
ничего: если Docker переиспользовал слой, второй «сборки» не было вовсе, и
сравнивались два имени одного и того же файла. Поэтому первыми идут
проверки того, что компиляция действительно выполнялась дважды, — метка
прогона, прочитанная ОБРАТНО из собранного образа, и время изменения
самого бинарника внутри образа. Только после них имеет смысл читать
identical.

ЧЕГО ЗДЕСЬ НЕТ. Здесь нет проверки «plain обязан совпасть» или
«debug-pinned обязан совпасть». Ожидания записаны в expected.json ДО
прогона, и превращать их в проверки значило бы объявлять прогон сломанным
всякий раз, когда действительность разошлась с записанным. Расхождение —
это находка, и её печатает разбор. Проверки здесь — про устройство
замера: контролируема ли пара, различает ли измеритель, выполнялись ли
обе сборки, приложено ли лечение к действительности.

ДВА ИСКЛЮЧЕНИЯ — оба контроля. Суммы source-changed ОБЯЗАНЫ разойтись:
измеритель, который не умеет ответить «различаются», не измеряет. Суммы
debug-plain ОБЯЗАНЫ разойтись тоже, но по другой причине: это
положительный контроль лечения. Пока сборка с -g и без закрепления
расходится, у флагов закрепления есть что закреплять; если бы и она
совпала, пара с -g мерила бы закрытый канал ровно так же, как пара без
-g, и достройка оси не дала бы ничего.

ОБА ПРОГОНА ПРОВЕРЯЮТСЯ ОДИНАКОВО. Проверки над фикстурой идут по ВСЕМ
снятым прогонам, а не только по последнему. Иначе утверждение «сумма та
же и во втором прогоне» строилось бы на данных, не прошедших ни улик
кэша, ни согласованности identical с суммами.
"""

import contextlib
import importlib.util
import io
import json
import os
import re
import tempfile
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(HERE, "scripts")
# Пути фикстур переопределяются окружением: scripts/sabotage.py
# натравливает проверки на КОПИИ фикстур и не трогает файлы в репозитории.
FIXTURE = os.environ.get(
    "REPRO_FIXTURE", os.path.join(HERE, "fixtures", "repro.jsonl"))
FIXTURE_RUN1 = os.environ.get(
    "REPRO_FIXTURE_RUN1", os.path.join(HERE, "fixtures", "repro.run-1.jsonl"))

from scripts.outcome import read_measurements
from scripts.repro import normalized_treatment, treatment_field

MEASURED = ("plain", "pinned", "debug-pinned", "debug-stripped")
CONTROLS = ("debug-plain", "source-changed")
ARMS = tuple(sorted(MEASURED + CONTROLS))

# Плечи, у которых суммы ОБЯЗАНЫ разойтись, и почему.
MUST_DIVERGE = {
    "source-changed": "во второй сборке добавлен флаг, меняющий код каждой "
                      "функции. Совпадение означает, что измеритель хеширует "
                      "один и тот же файл",
    "debug-plain": "две сборки с -g и без закрепления кладут объекты в разные "
                   "каталоги, и путь каталога лежит в отладочной информации. "
                   "Совпадение означает, что канала для лечения нет и здесь, "
                   "а значит пара с -g ничего не меряет",
}

# Плечи с отладочной информацией и плечи со strip — по устройству оси. Всё
# это сверяется с лечением, прочитанным из образа, а не принимается на веру.
WITH_DEBUG_INFO = ("debug-plain", "debug-pinned", "debug-stripped")
STRIPPED = ("plain", "pinned", "debug-stripped", "source-changed")
PINNING = ("pinned", "debug-pinned")

PAIRS = (("plain", "pinned"), ("debug-plain", "debug-pinned"))

# Пара механизма: debug-stripped обязан отличаться от debug-plain РОВНО
# шагом strip. В PAIRS она не входит — там плечи различаются флагами
# закрепления, — и до неё сверки инструментов и флагов не дотягивались
# вовсе. Посторонний флаг, добавленный этому плечу, прошёл бы весь набор
# проверок незамеченным, а вывод «канал закрывает strip» продолжал бы
# держаться на устройстве спецификации, а не на замере.
MECHANISM_PAIRS = (("debug-plain", "debug-stripped"),)

SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


def _rows(path=None):
    return {r["variant"]: r for r in read_measurements(path or FIXTURE)}


def _runs():
    """Все снятые прогоны: проверки идут по каждому, а не по последнему."""
    runs = [("прогон 2", _rows(FIXTURE))]
    if os.path.exists(FIXTURE_RUN1):
        runs.insert(0, ("прогон 1", _rows(FIXTURE_RUN1)))
    return runs


def _load(name, filename):
    """Загружает модуль с дефисом в имени файла.

    scripts/run-repro.py и scripts/analyze-repro.py обычным import не
    берутся: дефис не буква. Проверять их всё равно надо — контракт
    записи и защиты разбора снятыми данными не проверяются: снятые данные
    и есть то, что эти защиты пропускают.
    """
    spec = importlib.util.spec_from_file_location(
        name, os.path.join(SCRIPTS, filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _expected():
    with io.open(os.path.join(HERE, "expected.json"), encoding="utf-8") as fh:
        return json.loads(fh.read())["repro"]


class FixtureShape(unittest.TestCase):
    def test_fixture_exists(self):
        self.assertTrue(os.path.exists(FIXTURE),
                        "нет fixtures/repro.jsonl — прогон ещё не снят")

    def test_all_arms_present(self):
        for label, rows in _runs():
            self.assertEqual(sorted(rows), sorted(ARMS), label)

    def test_no_outcome_field(self):
        """У суммы исхода не бывает.

        Сумма не «разрешилась» и не «отказала» — она такая-то. Поле
        outcome в этой строке означало бы, что его выдумали.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotIn("outcome", row, "%s/%s" % (label, name))

    def test_kind_follows_the_role_of_the_arm(self):
        for label, rows in _runs():
            for name in MEASURED:
                self.assertEqual(rows[name]["kind"], "repro",
                                 "%s/%s" % (label, name))
            for name in CONTROLS:
                self.assertEqual(rows[name]["kind"], "repro-control",
                                 "%s/%s" % (label, name))

    def test_sums_look_like_sha256(self):
        for label, rows in _runs():
            for name, row in rows.items():
                for field in ("sha256_first", "sha256_second"):
                    self.assertRegex(
                        str(row[field]), SHA256_RE,
                        "%s/%s: %s не похоже на sha256" % (label, name, field))


class CacheDidNotMeasureItself(unittest.TestCase):
    """Главный враг этой оси, и первая же группа проверок про него.

    Совпадение сумм, полученное кэшем слоёв Docker, — ноль информации.
    Обе улики читаются из СОБРАННОГО ОБРАЗА, а не объявляются скриптом
    прогона: метка (docker run ... cat /nonce) и время изменения самого
    бинарника (stat внутри образа).
    """

    def test_nonces_differ_within_every_arm(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotEqual(
                    row["nonce_first"], row["nonce_second"],
                    "%s/%s: метка прогона у двух сборок совпала — второй "
                    "образ это первый образ, и сравнение сумм ничего не "
                    "значит" % (label, name))

    def test_binary_mtime_differs_within_every_arm(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotEqual(
                    row["binary_mtime_first"], row["binary_mtime_second"],
                    "%s/%s: бинарник внутри образа имеет одно и то же время "
                    "изменения — слой был переиспользован, компиляция во "
                    "второй раз не выполнялась" % (label, name))

    def test_all_binary_mtimes_in_a_run_are_pairwise_distinct(self):
        """Двенадцать сборок прогона — двенадцать различных сборок.

        Внутри плеча улики проверялись и раньше, а по прогону в целом —
        нет, и это было дырой: метка прогона у всех плеч одна и та же на
        позицию (так задумано), каталоги сборки общие, а у пары
        plain-первая и source-changed-первая совпадает ещё и весь набор
        аргументов сборки. Различность всех mtime — единственное, что
        отличает шесть плеч от одного, повторённого шесть раз.
        """
        for label, rows in _runs():
            seen = {}
            for name, row in sorted(rows.items()):
                for position in ("first", "second"):
                    mtime = row["binary_mtime_%s" % position]
                    where = "%s/%s" % (name, position)
                    self.assertNotIn(
                        mtime, seen,
                        "%s: время изменения бинарника %d встречается дважды "
                        "(%s и %s) — две сборки прогона были одной"
                        % (label, mtime, seen.get(mtime), where))
                    seen[mtime] = where

    def test_build_dirs_differ_within_every_arm(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotEqual(row["build_dir_first"],
                                    row["build_dir_second"],
                                    "%s/%s" % (label, name))

    def test_no_cache_declaration_matches_the_command_that_ran(self):
        """Поле no_cache — объявление, но объявление СВЯЗАННОЕ.

        Раньше ключ --no-cache был вписан в build_once жёстко, а поле
        бралось из спецификации: убрав ключ из кода, можно было оставить
        поле true, и все проверки прошли бы. Теперь в строку пишется
        полная командная строка docker build, и поле обязано отвечать
        тому, что в ней есть. Уликой это по-прежнему не называется —
        командную строку записывает тот же скрипт.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                for position in ("first", "second"):
                    argv = row["docker_argv_%s" % position]
                    self.assertEqual(
                        "--no-cache" in argv, bool(row["no_cache"]),
                        "%s/%s/%s: поле no_cache=%r не отвечает команде, "
                        "которая выполнялась: %r"
                        % (label, name, position, row["no_cache"], argv))

    def test_network_declaration_matches_the_command_that_ran(self):
        for label, rows in _runs():
            for name, row in rows.items():
                for position in ("first", "second"):
                    argv = row["docker_argv_%s" % position]
                    self.assertIn(
                        "--network=%s" % row["network"], argv,
                        "%s/%s/%s: поле network=%r не отвечает команде"
                        % (label, name, position, row["network"]))


class ControlledPair(unittest.TestCase):
    """Пара контролируема, только если различается ровно одно.

    Пар теперь две: plain/pinned без отладочной информации и
    debug-plain/debug-pinned с ней. В каждой различаться обязано лечение
    (флаги закрепления) и НЕ обязано различаться ничего больше: другой
    компилятор или другая версия fmt сделали бы разницу сумм необъяснимой.
    """

    def test_treatment_differs_between_arms_of_every_pair(self):
        for label, rows in _runs():
            for plain, pinned in PAIRS:
                self.assertNotEqual(
                    normalized_treatment(rows[plain], "first"),
                    normalized_treatment(rows[pinned], "first"),
                    "%s: плечи %s и %s получили одно и то же лечение — пара "
                    "не меряет ничего" % (label, plain, pinned))

    def test_pinning_arms_actually_pin(self):
        """Лечение читается из образа, а не объявляется прогоном."""
        for label, rows in _runs():
            for name in PINNING:
                treatment = rows[name]["treatment_first"]
                self.assertEqual(
                    treatment_field(treatment, "SOURCE_DATE_EPOCH"),
                    "1700000000", "%s/%s" % (label, name))
                self.assertIn("-ffile-prefix-map=",
                              treatment_field(treatment, "FLAGS"),
                              "%s/%s" % (label, name))

    def test_arms_without_pinning_pin_nothing(self):
        for label, rows in _runs():
            for name in ARMS:
                if name in PINNING:
                    continue
                treatment = rows[name]["treatment_first"]
                self.assertEqual(
                    treatment_field(treatment, "SOURCE_DATE_EPOCH"),
                    "<unset>", "%s/%s" % (label, name))
                self.assertNotIn("-ffile-prefix-map=",
                                 treatment_field(treatment, "FLAGS"),
                                 "%s/%s" % (label, name))

    def test_treatment_is_stable_within_an_arm(self):
        """Внутри плеча лечение обязано быть одним и тем же.

        Иначе «две сборки одного и того же» окажется двумя разными
        сборками, и расхождение сумм объяснялось бы лечением, а не
        воспроизводимостью.

        Сравнивается лечение БЕЗ каталога сборки. Дословное сравнение
        здесь было бы ошибкой самой проверки, а не находкой:
        -ffile-prefix-map по устройству называет каталог сборки, а
        каталоги у двух сборок разные намеренно — это тот источник
        расхождения, который флаг и закрепляет.
        """
        for label, rows in _runs():
            for name in MEASURED:
                self.assertEqual(normalized_treatment(rows[name], "first"),
                                 normalized_treatment(rows[name], "second"),
                                 "%s/%s" % (label, name))

    def test_normalization_does_not_erase_the_pinning(self):
        """Приведение к общему виду не должно стирать сам предмет спора.

        Если бы нормализация вычищала из строки всё, похожее на путь, она
        сравняла бы плечи пары, и проверка выше проходила бы всегда.
        """
        for label, rows in _runs():
            for plain, pinned in PAIRS:
                self.assertIn(
                    "-ffile-prefix-map=<BUILD_DIR>=/build",
                    normalized_treatment(rows[pinned], "first"), label)
                self.assertNotEqual(
                    normalized_treatment(rows[plain], "first"),
                    normalized_treatment(rows[pinned], "first"), label)

    def test_build_dir_field_is_named_by_the_treatment(self):
        """Поле build_dir_* обязано отвечать каталогу, который применился.

        На это поле опирается нормализация лечения: она вырезает из
        строки ровно тот путь, который в поле записан. Пока поле никто не
        сверял с действительностью, нормализация верила ему на слово — а
        значение в поле пишет тот же скрипт, что и вырезает. Каталог
        печатает сам шаг сборки в /repro_treatment.txt.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                for position in ("first", "second"):
                    self.assertEqual(
                        treatment_field(row["treatment_%s" % position],
                                        "BUILD_DIR"),
                        row["build_dir_%s" % position],
                        "%s/%s/%s: каталог сборки в поле и каталог, который "
                        "напечатал шаг сборки, — разные"
                        % (label, name, position))

    def test_debug_arms_really_carry_g(self):
        """-g обязан быть в лечении там, где плечо про него, и только там."""
        for label, rows in _runs():
            for name, row in rows.items():
                for position in ("first", "second"):
                    flags = treatment_field(row["treatment_%s" % position],
                                            "FLAGS").split()
                    self.assertEqual(
                        "-g" in flags, name in WITH_DEBUG_INFO,
                        "%s/%s/%s: флаги %r не отвечают устройству плеча"
                        % (label, name, position, flags))

    def test_strip_step_matches_the_arm(self):
        """Выполнялся ли strip, печатает сам шаг сборки, а не спецификация."""
        for label, rows in _runs():
            for name, row in rows.items():
                for position in ("first", "second"):
                    applied = treatment_field(row["treatment_%s" % position],
                                              "STRIP")
                    self.assertEqual(
                        applied, "yes" if name in STRIPPED else "no",
                        "%s/%s/%s: шаг сборки сообщил STRIP=%r"
                        % (label, name, position, applied))

    def test_unstripped_binaries_are_larger_than_stripped_ones(self):
        """Отладочная информация обязана быть видна в размере файла.

        Если бы плечи с -g и без strip дали файл того же размера, что
        стриппнутый, -g не применился бы, и вся достройка оси мерила бы
        не то, что называет. Проверяется не число, а знак отношения.
        """
        for label, rows in _runs():
            stripped = rows["plain"]["binary_bytes_first"]
            for name in ("debug-plain", "debug-pinned"):
                for position in ("first", "second"):
                    self.assertGreater(
                        rows[name]["binary_bytes_%s" % position], stripped,
                        "%s/%s/%s: бинарник с -g не больше стриппнутого — "
                        "отладочной информации в нём нет"
                        % (label, name, position))

    def test_mechanism_pair_differs_only_by_the_strip_step(self):
        """Пара механизма: различаться обязан ТОЛЬКО шаг strip.

        Плечо debug-stripped несёт отдельный вывод — «расхождение убирает
        strip, а не устройство исходника», — но до этой проверки его никто
        не сверял ни с debug-plain, ни с чем-либо ещё: PAIRS про флаги
        закрепления, и посторонний флаг, добавленный этому плечу, прошёл
        бы весь набор проверок. Утверждение держалось на устройстве
        спецификации; здесь оно становится замером, прочитанным из образа.
        """
        for label, rows in _runs():
            for base, stripped in MECHANISM_PAIRS:
                for field in ("cxx_version", "cmake_version", "fmt_version"):
                    self.assertEqual(
                        rows[base][field], rows[stripped][field],
                        "%s: плечи %s и %s различаются ещё и по «%s»"
                        % (label, base, stripped, field))
                for position in ("first", "second"):
                    ta = rows[base]["treatment_%s" % position]
                    tb = rows[stripped]["treatment_%s" % position]
                    self.assertEqual(
                        treatment_field(ta, "FLAGS").split(),
                        treatment_field(tb, "FLAGS").split(),
                        "%s/%s: наборы флагов у %s и %s различаются — значит "
                        "различие не одно, и «ровно шагом strip» неверно"
                        % (label, position, base, stripped))
                    self.assertEqual(
                        treatment_field(ta, "SOURCE_DATE_EPOCH"),
                        treatment_field(tb, "SOURCE_DATE_EPOCH"),
                        "%s/%s: у %s и %s различается SOURCE_DATE_EPOCH"
                        % (label, position, base, stripped))
                    self.assertNotEqual(
                        treatment_field(ta, "STRIP"),
                        treatment_field(tb, "STRIP"),
                        "%s/%s: шаг strip у %s и %s одинаков — пара механизма "
                        "не меряет ничего"
                        % (label, position, base, stripped))

    def test_pair_arms_share_toolchain_and_dependency(self):
        for label, rows in _runs():
            for plain, pinned in PAIRS:
                for field in ("cxx_version", "cmake_version", "fmt_version"):
                    self.assertEqual(
                        rows[plain][field], rows[pinned][field],
                        "%s: плечи %s и %s различаются ещё и по «%s» — "
                        "различие не одно" % (label, plain, pinned, field))

    def test_toolchain_is_the_same_in_both_builds_of_an_arm(self):
        """Симметрия: сверяются все три версии, а не одна.

        Раньше между двумя сборками плеча сверялась только версия
        компилятора, а версия cmake внутри плеча не сверялась вовсе.
        Асимметрия без причины: cmake задаёт команды компиляции ровно так
        же, и его подмена между двумя сборками объяснила бы расхождение
        сумм не хуже подмены компилятора.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                for field in ("cxx_version", "cmake_version", "fmt_version"):
                    self.assertEqual(
                        row["%s_first" % field], row["%s_second" % field],
                        "%s/%s: между двумя сборками плеча различается «%s»"
                        % (label, name, field))
                    self.assertEqual(row[field], row["%s_first" % field],
                                     "%s/%s/%s" % (label, name, field))


class MeasurerCanSayDifferent(unittest.TestCase):
    """Контроли: без них identical=true ничего не стоит.

    Два плеча обязаны разойтись, и по разным причинам: source-changed
    показывает, что измеритель вообще различает файлы, debug-plain — что
    у флагов закрепления есть что закреплять.
    """

    def test_controls_diverge(self):
        for label, rows in _runs():
            for name, why in sorted(MUST_DIVERGE.items()):
                row = rows[name]
                self.assertFalse(row["identical"],
                                 "%s/%s: суммы обязаны разойтись: %s"
                                 % (label, name, why))
                self.assertNotEqual(row["sha256_first"], row["sha256_second"],
                                    "%s/%s" % (label, name))

    def test_source_changed_changes_exactly_the_compilation_flags(self):
        for label, rows in _runs():
            row = rows["source-changed"]
            self.assertNotEqual(
                normalized_treatment(row, "first"),
                normalized_treatment(row, "second"),
                "%s: у отрицательного контроля обе сборки получили одно "
                "лечение — тогда неясно, чем он отличается" % label)
            self.assertIn("-fstack-protector-all",
                          normalized_treatment(row, "second"), label)

    def test_the_debug_pair_differs_only_by_pinning(self):
        """Положительный контроль и его плечо: -g есть у обоих."""
        for label, rows in _runs():
            plain = normalized_treatment(rows["debug-plain"], "first")
            pinned = normalized_treatment(rows["debug-pinned"], "first")
            self.assertNotEqual(plain, pinned, label)
            self.assertIn("-g", treatment_field(plain, "FLAGS").split(), label)
            self.assertIn("-g", treatment_field(pinned, "FLAGS").split(),
                          label)


class IdenticalFollowsFromTheSums(unittest.TestCase):
    """Поле identical обязано быть выводом из сумм, а не мнением.

    Записанное отдельно от сумм, оно могло бы разойтись с ними — и тогда
    таблица показывала бы одно, а суммы говорили другое.
    """

    def test_identical_matches_the_sums(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(
                    row["identical"],
                    row["sha256_first"] == row["sha256_second"],
                    "%s/%s" % (label, name))


class NetworkWasOff(unittest.TestCase):
    """R4: воспроизводимость меряется на сборке, не ходящей в сеть."""

    def test_every_build_declares_no_network(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["network"], "none",
                                 "%s/%s" % (label, name))


class BothRunsAreCheckedTheSameWay(unittest.TestCase):
    """Второй прогон обязан проходить те же защиты, что первый.

    В первой редакции проверки читали только REPRO_FIXTURE, а разбор
    применял защиты к последнему прогону и печатал первый сразу. То есть
    утверждение «сумма та же и в другом прогоне» строилось на данных, не
    прошедших ни улик кэша, ни согласованности identical с суммами.
    """

    def test_the_other_run_is_present_and_read(self):
        self.assertTrue(os.path.exists(FIXTURE_RUN1),
                        "нет второго прогона — устойчивость не проверена")
        self.assertEqual(len(_runs()), 2,
                         "проверки читают только один прогон")

    def test_analysis_guards_accept_both_runs(self):
        mod = _load("analyze_repro", "analyze-repro.py")
        for label, rows in _runs():
            mod.check_cache_did_not_measure_itself(rows, label)
            mod.check_measurer_can_say_different(rows, label)
            mod.check_pairs_are_controlled(rows, label)


class WriteTimeContract(unittest.TestCase):
    """Контракт строки проверяется ПРИ ЗАПИСИ, а не только над фикстурой.

    Разница существенная: проверка над готовой фикстурой ловит пропуск
    поля через час после прогона, когда пересъёмка стоит ещё один прогон.
    """

    def setUp(self):
        self.mod = _load("run_repro", "run-repro.py")
        self.good = {
            "kind": "repro", "variant": "plain", "role": "measured",
            "network": "none", "no_cache": True,
            "docker_argv_first": ["docker", "build", "--no-cache",
                                  "--network=none", "."],
            "docker_argv_second": ["docker", "build", "--no-cache",
                                   "--network=none", "."],
            "build_dir_first": "/build-a", "build_dir_second": "/build-b",
            "nonce_first": "1-a", "nonce_second": "2-b",
            "binary_mtime_first": 100, "binary_mtime_second": 200,
            "treatment_first": "BUILD_DIR=[/build-a] FLAGS=[] "
                               "SOURCE_DATE_EPOCH=[<unset>] STRIP=[yes]",
            "treatment_second": "BUILD_DIR=[/build-b] FLAGS=[] "
                                "SOURCE_DATE_EPOCH=[<unset>] STRIP=[yes]",
            "sha256_first": "a" * 64, "sha256_second": "a" * 64,
            "identical": True,
            "binary_bytes_first": 10, "binary_bytes_second": 10,
            "cxx_version_first": "g++ 15", "cxx_version_second": "g++ 15",
            "cmake_version_first": "cmake 3.31.6",
            "cmake_version_second": "cmake 3.31.6",
            "fmt_version_first": "10.1.0", "fmt_version_second": "10.1.0",
            "cxx_version": "g++ 15", "cmake_version": "cmake 3.31.6",
            "fmt_version": "10.1.0",
            "seconds_first": 1.0, "seconds_second": 1.0,
        }

    def _other(self, **changes):
        row = dict(self.good)
        row["variant"] = "pinned"
        row["binary_mtime_first"] = 300
        row["binary_mtime_second"] = 400
        row.update(changes)
        return row

    def test_good_row_passes(self):
        self.mod.check_row_contract(dict(self.good))

    def test_missing_nonce_is_refused(self):
        row = dict(self.good)
        del row["nonce_second"]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_null_sha_is_refused(self):
        row = dict(self.good)
        row["sha256_second"] = None
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_equal_nonces_are_refused_at_write_time(self):
        """Улика кэша ловится при записи, а не при разборе.

        Фикстура, у которой обе сборки — один и тот же образ, не должна
        вообще оказаться на диске: она выглядит полноценной.
        """
        row = dict(self.good)
        row["nonce_second"] = row["nonce_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_equal_mtimes_are_refused_at_write_time(self):
        row = dict(self.good)
        row["binary_mtime_second"] = row["binary_mtime_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_identical_inconsistent_with_sums_is_refused(self):
        row = dict(self.good)
        row["identical"] = False
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_no_cache_without_the_key_in_the_command_is_refused(self):
        """Объявление, которому не отвечает команда, на диск не идёт."""
        row = dict(self.good)
        row["docker_argv_first"] = ["docker", "build", "--network=none", "."]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_build_dir_not_named_by_the_treatment_is_refused(self):
        row = dict(self.good)
        row["build_dir_first"] = "/build-somewhere-else"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_toolchain_split_within_an_arm_is_refused(self):
        row = dict(self.good)
        row["cmake_version_second"] = "cmake 3.30.0"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_run_with_all_arms_passes(self):
        self.mod.check_run_contract([dict(self.good), self._other()])

    def test_mtime_shared_between_two_arms_is_refused(self):
        """Различность mtime требуется по прогону, а не по плечу."""
        with self.assertRaises(SystemExit):
            self.mod.check_run_contract(
                [dict(self.good), self._other(binary_mtime_first=100)])


class AnalysisGuards(unittest.TestCase):
    """Разбор обязан останавливаться до печати таблицы."""

    def setUp(self):
        self.mod = _load("analyze_repro", "analyze-repro.py")
        self.rows = _rows()

    def _copy(self):
        return {k: dict(v) for k, v in self.rows.items()}

    def test_healthy_run_passes_the_guards(self):
        self.mod.check_cache_did_not_measure_itself(self.rows, "прогон 2")
        self.mod.check_measurer_can_say_different(self.rows, "прогон 2")
        self.mod.check_pairs_are_controlled(self.rows, "прогон 2")

    def test_equal_nonce_stops_the_analysis(self):
        rows = self._copy()
        rows["plain"]["nonce_second"] = rows["plain"]["nonce_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_cache_did_not_measure_itself(rows, "прогон 2")

    def test_mtime_shared_between_arms_stops_the_analysis(self):
        rows = self._copy()
        rows["pinned"]["binary_mtime_first"] = \
            rows["plain"]["binary_mtime_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_cache_did_not_measure_itself(rows, "прогон 2")

    def test_control_that_did_not_diverge_stops_the_analysis(self):
        rows = self._copy()
        rows["source-changed"]["identical"] = True
        with self.assertRaises(SystemExit):
            self.mod.check_measurer_can_say_different(rows, "прогон 2")

    def test_positive_control_that_converged_stops_the_analysis(self):
        rows = self._copy()
        rows["debug-plain"]["identical"] = True
        with self.assertRaises(SystemExit):
            self.mod.check_measurer_can_say_different(rows, "прогон 2")

    def test_uncontrolled_pair_stops_the_analysis(self):
        rows = self._copy()
        rows["pinned"]["treatment_first"] = rows["plain"]["treatment_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_pairs_are_controlled(rows, "прогон 2")

    def test_uncontrolled_debug_pair_stops_the_analysis(self):
        rows = self._copy()
        rows["debug-pinned"]["treatment_first"] = \
            rows["debug-plain"]["treatment_first"]
        with self.assertRaises(SystemExit):
            self.mod.check_pairs_are_controlled(rows, "прогон 2")

    def test_stray_flag_in_the_mechanism_pair_stops_the_analysis(self):
        """Лишний флаг у debug-stripped обязан остановить разбор.

        Раньше он проходил незамеченным: пара механизма не сверялась ни по
        флагам, ни по инструментам, и вывод «отличается ровно шагом strip»
        оставался объявлением.
        """
        rows = self._copy()
        for position in ("first", "second"):
            key = "treatment_%s" % position
            rows["debug-stripped"][key] = rows["debug-stripped"][key].replace(
                "FLAGS=[-g", "FLAGS=[-g -fno-plt")
        with self.assertRaises(SystemExit):
            self.mod.check_pairs_are_controlled(rows, "прогон 2")

    def test_equal_strip_step_in_the_mechanism_pair_stops_the_analysis(self):
        rows = self._copy()
        for position in ("first", "second"):
            key = "treatment_%s" % position
            rows["debug-stripped"][key] = rows["debug-stripped"][key].replace(
                "STRIP=[yes]", "STRIP=[no]")
        with self.assertRaises(SystemExit):
            self.mod.check_pairs_are_controlled(rows, "прогон 2")


class HypothesesCaveatIsPrinted(unittest.TestCase):
    """Две из трёх «гипотез» гипотезами не были — и это обязано звучать.

    Проба ревьюера дала 0c510779… для «-g, затем strip» и 2d45ce66… для
    «-g с закреплением», а первая редакция НАШЕЙ ЖЕ фикстуры давала
    0c510779… для plain. Значит и равенство debug-stripped = plain, и
    неравенство debug-pinned ≠ plain были выводимы из уже наблюдённых
    данных, а не предсказаны: твёрдым прогнозом остаётся один пункт —
    отношение размеров, которого проба не давала.

    expected.json не правится: он предрегистрационный, и правка задним
    числом уничтожила бы его смысл. Поэтому дефект называется в печати
    разбора, а эта проверка держит оговорку на месте, чтобы её не
    потеряла следующая правка.
    """

    def _printed(self):
        mod = _load("analyze_repro", "analyze-repro.py")
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            mod.main()
        return mod, buf.getvalue()

    def test_caveat_names_the_single_firm_prognosis(self):
        mod, out = self._printed()
        self.assertEqual(mod.FIRM_HYPOTHESIS,
                         "debug_arms_are_really_built_with_g")
        self.assertIn("ОГОВОРКА: ГИПОТЕЗАМИ БЫЛИ НЕ ВСЕ ТРИ", out)
        self.assertIn("Твёрдым прогнозом здесь засчитывается ОДНА строка",
                      out)
        self.assertIn(mod.FIRM_HYPOTHESIS, out)

    def test_caveat_names_both_derived_items_and_their_source(self):
        mod, out = self._printed()
        self.assertEqual(len(mod.DERIVED_FROM_SEEN_DATA), 2)
        for key in mod.DERIVED_FROM_SEEN_DATA:
            self.assertIn(key, out)
        self.assertIn("ВЫВОДИМЫ", out)
        self.assertIn("0c510779", out)
        self.assertIn("2d45ce66", out)
        self.assertIn("7936180", out)

    def test_caveat_says_the_justification_is_inverted_and_not_edited(self):
        mod, out = self._printed()
        self.assertIn("перевёрнуто", out)
        self.assertIn("довод за знание", out)
        self.assertIn("предрегистрационный", out)


class DestinationIsCheckedBeforeTheBuilds(unittest.TestCase):
    """Ошибка вызова не должна стоить полного прогона оси.

    Относительное имя присоединяется к каталогу фикстур, поэтому вызов
    вида `run-repro.py fixtures/repro.jsonl` даёт `fixtures/fixtures/…`.
    Раньше это выяснялось ПОСЛЕ двенадцати сборок, и цена ошибки вызова
    уже была заплачена один раз. Теперь путь разбирается первым же
    действием.
    """

    def setUp(self):
        self.mod = _load("run_repro", "run-repro.py")

    def test_no_argument_gives_the_default_fixture(self):
        self.assertEqual(self.mod.resolve_out_path([]),
                         self.mod.DEFAULT_FIXTURE)

    def test_relative_name_lands_in_the_fixtures_dir(self):
        self.assertEqual(
            self.mod.resolve_out_path(["repro.run-1.jsonl"]),
            os.path.join(self.mod.FIXTURES_DIR, "repro.run-1.jsonl"))

    def test_missing_destination_directory_is_refused(self):
        """Именно тот вызов, который стоил прогона."""
        with self.assertRaises(SystemExit):
            self.mod.resolve_out_path(["fixtures/repro.jsonl"])

    def test_absolute_path_to_a_missing_directory_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(SystemExit):
                self.mod.resolve_out_path(
                    [os.path.join(tmp, "no-such-dir", "repro.jsonl")])

    def test_absolute_path_to_an_existing_directory_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = os.path.join(tmp, "repro.jsonl")
            self.assertEqual(self.mod.resolve_out_path([target]), target)


class ExpectationsWereRecordedBeforeTheRun(unittest.TestCase):
    """Записанное до прогона обязано существовать и быть полным.

    Проверяется не то, что оно СБЫЛОСЬ (это находка, а не поломка), а то,
    что для каждого плеча записано ожидание и что источник ожидания назван
    честно: предсказание или проба.
    """

    def test_every_arm_has_a_recorded_expectation(self):
        expected = _expected()
        for name in ARMS:
            found = (expected["hypothesis_before_run"].get(name)
                     or expected["known_from_pre_run_probes"].get(name))
            self.assertIsNotNone(found, "нет ожидания для плеча %s" % name)
            self.assertIn("expected_identical", found, name)

    def test_probe_results_are_not_filed_as_predictions(self):
        """Исход плеч с -g известен из пробы, а не предсказан.

        Записать его в гипотезы значило бы выдать знание за предвидение.
        Проверка держит границу: этих плеч в блоке гипотез быть не должно,
        а в блоке пробы у каждого обязано стоять основание.
        """
        expected = _expected()
        for name in WITH_DEBUG_INFO:
            self.assertNotIn(name, expected["hypothesis_before_run"], name)
            entry = expected["known_from_pre_run_probes"][name]
            self.assertIn("проба", entry["basis"], name)

    def test_hypotheses_that_remain_are_recorded(self):
        hypothesis = _expected()["hypothesis_before_run"]
        for key in ("debug_arms_are_really_built_with_g",
                    "debug_stripped_gives_the_stripped_sum",
                    "debug_pinned_does_not_equal_the_stripped_sum"):
            self.assertIn(key, hypothesis)
            self.assertIn("expected", hypothesis[key])

    def test_sources_of_divergence_named_in_advance(self):
        named = _expected()["hypothesis_before_run"][
            "named_sources_of_divergence_in_advance"]
        self.assertGreaterEqual(len(named), 3)

    def test_what_will_not_be_claimed_is_recorded(self):
        text = _expected()["what_will_not_be_claimed"]
        self.assertIn("ОДНОЙ машине", text)
        self.assertIn("ПОВТОР В ТОМ ЖЕ ОКНЕ", text)

    def test_claim_scope_names_the_configuration_not_the_source(self):
        """Вывод приписывается конфигурации сборки, а не исходнику.

        Тот же исходник с -g расходится, поэтому «на этом исходнике
        закреплять нечего» — утверждение про не ту переменную.
        """
        text = _expected()["if_they_coincide_unexpectedly"]
        self.assertIn("Release, без -g, со strip", text)
        self.assertNotIn("Это утверждение про данный исходник", text)


if __name__ == "__main__":
    unittest.main()
