"""Ось сборки: сначала контроли и окно замера, потом уже разница.

Порядок проверок здесь не случаен. Расхождение между vcpkg, conan и
системной зависимостью можно читать только после того, как доказано
несколько вещей:

  * все сравниваемые плечи меряют ОДНО И ТО ЖЕ — получение зависимости
    входит в замер у каждого из них. Пока окно замера различалось,
    cold_seconds у системного плеча значило «компиляция сервиса», а у
    менеджерских — «установка менеджера, получение пакета, сборка
    библиотеки и компиляция сервиса»; кратность между ними сравнивала
    несравнимое;
  * system-offline СОБИРАЕТСЯ — плечо с заранее полученной зависимостью
    не ходит в сеть, и задача 5 может мерить на нём воспроизводимость;
  * no-fmt ПАДАЕТ на find_package — в базовом образе менеджерских плеч
    fmt нет, и их успех приписывается менеджеру;
  * vcpkg-latest ПАДАЕТ на компиляции — незакреплённый реестр
    подсовывает мажорную версию, которой не просили.

Контроль, упавший не на своём шаге, ничего не доказывает: no-fmt,
упавший на сети, и no-fmt, упавший на find_package, дают одинаковый код
возврата и разные утверждения. Поэтому проверяется ШАГ падения, а не
только его факт.

Часть проверок здесь читает не фикстуру, а сам код: контракт строки при
записи и защиты разбора снятыми данными не проверить — снятые данные и
есть то, что они пропускают или отвергают.
"""

import importlib.util
import io
import os
import re
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(HERE, "scripts")
# Путь фикстуры переопределяется окружением: скрипт диверсий
# (scripts/sabotage.py) натравливает проверки на КОПИЮ фикстуры и не
# трогает файл в репозитории.
FIXTURE = os.environ.get(
    "BUILD_FIXTURE", os.path.join(HERE, "fixtures", "build.jsonl"))

from scripts.outcome import read_measurements

# Измеряемые плечи. Первые три сравнимы между собой по времени:
# получение зависимости у всех трёх входит в замер. Четвёртое меряет
# другое окно и стоит в таблице отдельно.
COMPARABLE = ("system", "vcpkg", "conan")
MEASURED = COMPARABLE + ("system-prebaked",)
CONTROLS = ("system-offline", "vcpkg-latest", "no-fmt")


def _rows():
    return {r["variant"]: r for r in read_measurements(FIXTURE)}


def _load(name, filename):
    """Загружает модуль с дефисом в имени файла.

    scripts/analyze-build.py и scripts/run-build.py обычным import не
    берутся: дефис не буква. Проверять их всё равно надо — защиты
    разбора и контракт записи снятыми данными не проверяются.
    """
    spec = importlib.util.spec_from_file_location(
        name, os.path.join(SCRIPTS, filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FixtureShape(unittest.TestCase):
    def test_fixture_exists(self):
        self.assertTrue(os.path.exists(FIXTURE),
                        "нет fixtures/build.jsonl — прогон ещё не снят")

    def test_no_outcome_field_on_build_rows(self):
        """У времени сборки исхода не бывает.

        Поле outcome в этих строках означало бы, что его выдумали:
        сборка не «разрешилась» и не «отказала», она заняла столько-то
        секунд. Контракт исхода живёт в read_fixture и применяется
        только к оси упаковки.
        """
        for name, row in _rows().items():
            self.assertNotIn("outcome", row,
                             "%s: у строки величины не должно быть поля "
                             "outcome" % name)

    def test_all_arms_present(self):
        self.assertEqual(sorted(_rows()),
                         sorted(MEASURED + CONTROLS))

    def test_measured_rows_have_kind_build(self):
        rows = _rows()
        for name in MEASURED:
            self.assertEqual(rows[name]["kind"], "build", name)


class MeasurementWindow(unittest.TestCase):
    """Главная находка ревью: две величины стояли под одним именем."""

    def test_every_arm_records_its_measurement_window(self):
        for name, row in _rows().items():
            self.assertIn(row.get("measurement_window"),
                          ("dependency_inside", "dependency_outside"),
                          "%s: не записано окно замера — по колонке "
                          "cold_seconds нельзя понять, что она меряет"
                          % name)

    def test_compared_arms_share_one_window(self):
        rows = _rows()
        windows = {rows[n]["measurement_window"] for n in COMPARABLE}
        self.assertEqual(
            windows, {"dependency_inside"},
            "плечи, которые сравниваются по времени, меряют разное: %r. "
            "Кратность между ними сравнивала бы «компиляцию сервиса» с "
            "«получением зависимости плюс компиляцией»" % windows)

    def test_prebaked_arm_is_outside_the_compared_window(self):
        """Якорь R4 стоит отдельно и в кратности не попадает."""
        self.assertEqual(_rows()["system-prebaked"]["measurement_window"],
                         "dependency_outside")

    def test_system_pair_differs_only_by_place_of_installation(self):
        """Пара system / system-prebaked: различие ровно одно.

        Если совпадает не всё, различий между плечами больше одного, и
        разницу во времени нельзя приписать месту установки зависимости.
        """
        rows = _rows()
        a, b = rows["system"], rows["system-prebaked"]
        for field in ("fmt_version", "fmt_version_declared", "fmt_linkage",
                      "binary_bytes", "cxx_version", "cmake_version",
                      "distro_fmt_static_archives"):
            self.assertEqual(
                a[field], b[field],
                "пара system/system-prebaked разошлась по «%s» (%r против "
                "%r) — различается уже не только место установки "
                "зависимости" % (field, a[field], b[field]))
        self.assertNotEqual(a["measurement_window"], b["measurement_window"],
                            "у пары одинаковое окно замера — тогда это не "
                            "пара, а два одинаковых плеча")


class Controls(unittest.TestCase):
    """Без этих проверок остальная таблица нечитаема."""

    def test_prebaked_arm_builds_without_network(self):
        row = _rows()["system-offline"]
        self.assertTrue(
            row["build_ok"],
            "плечо с заранее полученной зависимостью обратилось в сеть во "
            "время сборки (код %r, шаг %r) — задача 5 не может мерить на "
            "нём воспроизводимость: кэш менеджера станет второй переменной"
            % (row["cold_returncode"], row.get("failure_stage")))

    def test_base_image_has_no_fmt(self):
        row = _rows()["no-fmt"]
        self.assertFalse(row["build_ok"],
                         "плечо без менеджера собралось — значит fmt лежит "
                         "в базовом образе, и успех vcpkg/conan ничего не "
                         "говорит про менеджер")
        self.assertEqual(
            row["failure_stage"], "find_package",
            "контроль обязан падать на find_package, а не где придётся; "
            "упал на %r: %s" % (row["failure_stage"], row["failure_tail"]))

    def test_unpinned_vcpkg_registry_breaks_the_source(self):
        row = _rows()["vcpkg-latest"]
        self.assertFalse(row["build_ok"],
                         "незакреплённый реестр vcpkg собрался — утверждение "
                         "о необходимости закреплять базовую линию больше не "
                         "подтверждается этим стендом")
        self.assertEqual(
            row["failure_stage"], "compile",
            "падение обязано быть на компиляции исходника; падение на сети "
            "или на клоне доказывало бы обрыв связи, а не свойство "
            "менеджера. Упал на %r: %s"
            % (row["failure_stage"], row["failure_tail"]))

    def test_unpinned_registry_version_is_measured_not_remembered(self):
        """Номер версии в утверждении обязан быть снят, а не вспомнен.

        Раньше у этого плеча fmt_version был null: сборка падала до
        записи версии, а число в утверждении приходило из доразведочной
        пробы. Теперь версия снимается ДО компиляции.
        """
        rows = _rows()
        latest = rows["vcpkg-latest"]["fmt_version"]
        self.assertTrue(
            latest,
            "у контроля vcpkg-latest не снята версия fmt — утверждение "
            "«незакреплённый реестр подсовывает такую-то версию» опирается "
            "не на фикстуру")
        self.assertNotEqual(
            latest, rows["vcpkg"]["fmt_version"],
            "закреплённый и незакреплённый реестр дали одну версию — "
            "контроль перестал показывать то, ради чего заведён")

    def test_managers_did_not_both_fail(self):
        """Если оба менеджера упали, отличить их поломку от отсутствия
        сети нечем, и вся таблица недействительна."""
        rows = _rows()
        self.assertTrue(
            rows["vcpkg"]["build_ok"] or rows["conan"]["build_ok"],
            "упали оба менеджерских плеча — это неотличимо от отсутствия "
            "сети, читать ось нельзя")


class ArmsAreComparable(unittest.TestCase):
    """Плечи сравнимы, только если различается ровно одно."""

    def test_compiler_and_cmake_identical_across_measured_arms(self):
        rows = _rows()
        cxx = {rows[n]["cxx_version"] for n in MEASURED}
        cmake = {rows[n]["cmake_version"] for n in MEASURED}
        self.assertEqual(len(cxx), 1,
                         "версии компилятора разошлись: %r" % cxx)
        self.assertEqual(len(cmake), 1,
                         "версии cmake разошлись: %r" % cmake)

    def test_vcpkg_and_conan_share_the_fmt_version(self):
        """Контролируемая пара оси.

        Раньше эта проверка сравнивала ИЗМЕРЕННУЮ версию у vcpkg с
        ОБЪЯВЛЕННОЙ в conanfile.txt у conan и была наполовину
        тавтологией: она прошла бы и при сломанном разрешении
        зависимости. Теперь оба числа снимает один измеритель из
        заголовков установленного пакета.
        """
        rows = _rows()
        self.assertEqual(rows["vcpkg"]["fmt_version"],
                         rows["conan"]["fmt_version"],
                         "пара vcpkg/conan различается версией fmt — "
                         "различается уже две вещи, сравнивать размеры "
                         "нельзя")

    def test_system_arm_has_its_own_fmt_version(self):
        """А вот системное плечо версию не выбирает — её выбрал дистрибутив.

        Это не дефект стенда, а свойство источника зависимости, и его
        надо держать записанным: пока версия отличается, точный размер
        бинарника системного плеча нельзя вычитать из менеджерских.
        """
        rows = _rows()
        self.assertNotEqual(rows["system"]["fmt_version"],
                            rows["conan"]["fmt_version"],
                            "версии совпали — оговорку про несравнимость "
                            "размеров надо пересмотреть, а не оставлять")


class EveryArmNamesItsSource(unittest.TestCase):
    def test_every_arm_says_where_the_dependency_came_from(self):
        for name, row in _rows().items():
            self.assertTrue(str(row.get("dependency_from", "")).strip(),
                            "%s: не записано, откуда взялась зависимость"
                            % name)


class ColdIsNotFasterThanWarm(unittest.TestCase):
    """Холодная сборка не может быть быстрее тёплой.

    Если оказалась — значит --no-cache не сработал и кэш померил сам
    себя. Это не находка про менеджеры, а недействительный прогон.
    """

    def test_cold_not_faster_than_warm(self):
        for name in MEASURED:
            row = _rows()[name]
            self.assertGreaterEqual(
                row["cold_seconds"], row["warm_seconds"],
                "%s: холодная сборка (%.1f s) быстрее тёплой (%.1f s) — "
                "кэш померил сам себя, прогон недействителен"
                % (name, row["cold_seconds"], row["warm_seconds"]))


class LinkageIsDecidedByTheSource(unittest.TestCase):
    """Главная практическая находка оси.

    Сменив источник зависимости, ты меняешь способ линковки, не написав
    об этом ни строки в своём CMakeLists.txt.
    """

    def test_system_links_fmt_dynamically(self):
        self.assertEqual(_rows()["system"]["fmt_linkage"], "dynamic")

    def test_both_managers_link_fmt_statically(self):
        rows = _rows()
        self.assertEqual(rows["vcpkg"]["fmt_linkage"], "static")
        self.assertEqual(rows["conan"]["fmt_linkage"], "static")

    def test_linkage_actually_differs_between_sources(self):
        rows = _rows()
        self.assertNotEqual(rows["system"]["fmt_linkage"],
                            rows["conan"]["fmt_linkage"],
                            "способ линковки одинаков у всех источников — "
                            "тогда главного утверждения оси нет")

    def test_static_arms_carry_the_library_in_the_binary(self):
        """Статическая линковка обязана быть видна в размере.

        Иначе «static» в строке фикстуры — это слово, за которым нет
        измерения: ldd мог не показать libfmt по любой причине.
        """
        rows = _rows()
        self.assertGreater(rows["conan"]["binary_bytes"],
                           rows["system"]["binary_bytes"],
                           "статический бинарник не крупнее динамического — "
                           "либо линковка определена неверно, либо fmt в "
                           "него не попал")

    def test_distro_package_ships_no_static_archive(self):
        """Причина находки, а не только её следствие.

        «Система линкует динамически» стенд меряет через ldd — это
        СЛЕДСТВИЕ. Причина в том, что пакет дистрибутива несёт только
        .so. Если дистрибутив однажды положит в пакет статический архив,
        причина станет неверной, а следствие останется прежним, и стенд
        промолчал бы. Эта проверка не даст ему промолчать.
        """
        rows = _rows()
        for name in ("system", "system-prebaked"):
            self.assertEqual(
                rows[name]["distro_fmt_static_archives"], 0,
                "%s: в пакете libfmt-dev появились статические архивы "
                "(%r штук). Объяснение «дистрибутив несёт только .so» "
                "больше не верно — его надо пересмотреть, а не оставить "
                "рядом с прежним следствием"
                % (name, rows[name]["distro_fmt_static_archives"]))


class WriteTimeContract(unittest.TestCase):
    """Контракт полей проверяется ПРИ ЗАПИСИ, а не только над фикстурой.

    Разница существенная: проверка над снятой фикстурой ловит пропуск
    поля через час после прогона, когда пересъёмка стоит ещё один
    прогон. Отчёт обещал контракт — вот он.
    """

    def setUp(self):
        self.run_build = _load("run_build_module", "run-build.py")

    def _good_row(self):
        return {
            "kind": "build", "variant": "system", "role": "measured",
            "measurement_window": "dependency_inside",
            "dependency_from": "репозиторий дистрибутива",
            "build_ok": True, "cold_returncode": 0, "cold_seconds": 9.0,
            "warm_seconds": 1.5, "rebuild_seconds": 3.0,
            "binary_bytes": 18680, "fmt_linkage": "dynamic",
            "fmt_version": "10.1.0", "cxx_version": "g++",
            "cmake_version": "cmake",
        }

    def test_good_row_passes(self):
        self.run_build.check_row_contract(self._good_row())

    def test_missing_measurement_window_is_refused(self):
        row = self._good_row()
        del row["measurement_window"]
        with self.assertRaises(SystemExit):
            self.run_build.check_row_contract(row)

    def test_null_cold_seconds_is_refused(self):
        row = self._good_row()
        row["cold_seconds"] = None
        with self.assertRaises(SystemExit):
            self.run_build.check_row_contract(row)

    def test_missing_role_is_refused(self):
        row = self._good_row()
        del row["role"]
        with self.assertRaises(SystemExit):
            self.run_build.check_row_contract(row)

    def test_empty_dependency_from_is_refused(self):
        row = self._good_row()
        row["dependency_from"] = "   "
        with self.assertRaises(SystemExit):
            self.run_build.check_row_contract(row)

    def test_failed_arm_may_omit_measurement_fields(self):
        """У упавшего плеча нечего мерить, и требовать этого нельзя."""
        row = self._good_row()
        row["build_ok"] = False
        row["cold_returncode"] = 1
        for field in ("warm_seconds", "rebuild_seconds", "binary_bytes",
                      "fmt_linkage", "cxx_version", "cmake_version"):
            row[field] = None
        self.run_build.check_row_contract(row)


class AnalysisGuards(unittest.TestCase):
    """Разбор обязан выдавать вердикт, а не трассировку."""

    def setUp(self):
        self.analyze = _load("analyze_build_module", "analyze-build.py")

    def test_failed_measured_arm_stops_the_analysis(self):
        """Раньше здесь был TypeError на форматировании None."""
        rows = _rows()
        rows["vcpkg"] = dict(rows["vcpkg"], build_ok=False,
                             warm_seconds=None, failure_stage="network")
        with self.assertRaises(SystemExit) as caught:
            self.analyze.check_measured_arms_built(rows)
        self.assertIn("ИЗМЕРЯЕМОЕ ПЛЕЧО НЕ СОБРАЛОСЬ", str(caught.exception))

    def test_missing_measured_arm_stops_the_analysis(self):
        rows = _rows()
        del rows["conan"]
        with self.assertRaises(SystemExit):
            self.analyze.check_measured_arms_built(rows)

    def test_healthy_run_passes_the_guard(self):
        self.analyze.check_measured_arms_built(_rows())


class PrintedTimesCarryTheirLimit(unittest.TestCase):
    """Печать разбора не отдаёт голый множитель времени сборки.

    README кратности убрал и отсылает за текущими числами сюда. Если
    печать отдаёт «дороже system в 2,1 раза», а через двадцать строк
    сообщает «в 1,02 раза» про устойчивость, то тот, кто пишет статью,
    возьмёт 2,1× отсюда — и воспроизведёт ровно тот дефект, который был
    снят: множитель не переживает смены сессии. Секунды печатать можно
    и нужно, множитель — нет.

    Проверка смотрит на ВЫВОД, а не на исходник: правка, вернувшая
    множитель другим оборотом речи, обошла бы структурную проверку.
    """

    @classmethod
    def setUpClass(cls):
        env = dict(os.environ)
        env["PYTHONIOENCODING"] = "utf-8"
        done = subprocess.run(
            [sys.executable, os.path.join(SCRIPTS, "analyze-build.py")],
            capture_output=True, text=True, encoding="utf-8",
            errors="replace", env=env, cwd=HERE)
        if done.returncode != 0:
            raise AssertionError("разбор не отработал: %s"
                                 % (done.stdout + done.stderr)[-800:])
        cls.text = done.stdout

    def _section(self, title, until):
        start = self.text.index(title)
        end = self.text.index(until, start)
        return self.text[start:end]

    def test_the_cold_build_block_prints_seconds_without_a_ratio(self):
        block = self._section("ХОЛОДНАЯ СБОРКА ВНУТРИ ОДНОГО ОКНА",
                              "ПАРА system / system-prebaked")
        found = re.findall(r"в \d+[.,]\d+ раза", block)
        self.assertEqual(
            found, [],
            "печать холодной сборки отдаёт множитель %r: он не переживает "
            "смены сессии, и унесённый отсюда в статью станет числом, "
            "которое не воспроизводится" % found)
        self.assertIn(" s", block, "секунды печатать надо: убран замер, а "
                                    "не только вывод из него")

    def test_the_cold_build_block_says_the_ratio_does_not_survive(self):
        block = self._section("ХОЛОДНАЯ СБОРКА ВНУТРИ ОДНОГО ОКНА",
                              "ПАРА system / system-prebaked")
        self.assertIn("не переживает", block,
                      "печать молчит о том, почему множителя здесь нет")
        self.assertIn("evidence", block,
                      "печать не отсылает к свидетельству межсессионного "
                      "разброса")

    def test_the_run_to_run_block_is_named_repeatability(self):
        """«Устойчивость между прогонами» читалась как «величина стабильна».

        Два прогона сняты подряд в одной сессии: это повторяемость. Так
        она и обязана называться, иначе соседство с множителем читается
        как разрешение множитель публиковать.
        """
        self.assertIn("ПОВТОРЯЕМОСТЬ ВНУТРИ СЕССИИ", self.text)
        start = self.text.index("ПОВТОРЯЕМОСТЬ ВНУТРИ СЕССИИ")
        block = self.text[start:]
        self.assertIn("не воспроизводимость", block)


class RecordedRuleIsTheOneApplied(unittest.TestCase):
    """Правило, записанное ДО прогона, обязано быть реализовано само.

    В expected.json записано: «если разница между плечами по
    warm_seconds окажется меньше секунды, разбор говорит вслух, что по
    этой величине вывода нет». В коде жило другое правило —
    межпрогонное. На тех данных срабатывали оба, поэтому расхождение
    ничего не испортило. Смысл предрегистрации в том, что применяется
    записанное правило, а не то, которое даёт тот же ответ.

    Переснятые фикстуры показали случай, ради которого правило и стоит
    применять как записано: на ПЕРВОМ прогоне сессии оно срабатывает, на
    ВТОРОМ молчит. Порог остался прежним; разбор обязан назвать
    расхождение прогонов вслух и не печатать вывода по величине —
    проверки на это ниже.
    """

    def setUp(self):
        self.analyze = _load("analyze_build_module", "analyze-build.py")

    def _verdict_text(self, values, run1, floor):
        import contextlib
        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            self.analyze._verdict("проба", values, run1, floor=floor)
        return buffer.getvalue()

    def test_recorded_floor_is_one_second(self):
        self.assertEqual(self.analyze.RECORDED_FLOOR_SECONDS, 1.0)

    def test_recorded_rule_fires_even_when_run_to_run_rule_does_not(self):
        """Разброс между прогонами нулевой, разница между плечами 0,2 с.

        Межпрогонное правило здесь молчит. Записанное — обязано
        сработать; если разбор промолчит, значит применяется не оно.
        """
        values = {"a": 1.5, "b": 1.7}
        text = self._verdict_text(values, dict(values),
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertIn("ВЫВОДА НЕТ", text)
        self.assertIn("записанное ДО прогона", text)

    def test_both_rules_are_reported_when_both_fire(self):
        values = {"a": 1.5, "b": 1.7}
        text = self._verdict_text(values, {"a": 1.9, "b": 1.2},
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertIn("Сработало правил: 2", text)

    def test_wide_difference_passes_both_rules(self):
        values = {"a": 4.0, "b": 40.0}
        text = self._verdict_text(values, {"a": 4.2, "b": 41.0},
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertNotIn("ВЫВОДА НЕТ", text)

    def test_cold_seconds_has_no_recorded_floor(self):
        """Абсолютного правила для холодной сборки записано не было.

        Приписать его задним числом значило бы сделать ровно то, за что
        ревью и зацепилось, — только в другую сторону.
        """
        text = self._verdict_text({"a": 9.0, "b": 9.5},
                                  {"a": 9.1, "b": 9.4}, None)
        self.assertNotIn("записанное ДО прогона", text)

    def test_runs_that_disagree_about_the_rule_leave_no_verdict(self):
        """Первый прогон снимает утверждение, второй — нет.

        Разница между плечами на первом прогоне 0,2 с (меньше порога),
        на втором 1,5 с (не меньше). Оба остальных правила здесь
        молчат: порог второй прогон проходит, а межпрогонное
        расхождение (1,3 с) уже разницы между плечами. Если бы разбор
        не умел говорить о расхождении прогонов, он напечатал бы вывод
        по величине, информативность которой переворачивается внутри
        одной сессии.
        """
        text = self._verdict_text({"a": 1.5, "b": 3.0},
                                  {"a": 1.5, "b": 1.7},
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertIn("ВЫВОДА НЕТ", text)
        self.assertIn("РАСХОДЯТСЯ", text)
        self.assertIn("вывода не даёт тем более", text)
        self.assertNotIn("плечи различимы", text)

    def test_disagreement_is_caught_in_the_other_direction_too(self):
        """Первый прогон правило проходит, второй — снимает.

        Порядок прогонов на вердикт не влияет: расходятся они и так, и
        так. Проверка нужна отдельно, потому что снятые данные
        показывают только одно из двух направлений, и проверка,
        привязанная к нему, была бы слепа ко второму.
        """
        text = self._verdict_text({"a": 1.5, "b": 1.7},
                                  {"a": 1.5, "b": 3.0},
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertIn("РАСХОДЯТСЯ", text)

    def test_agreeing_runs_do_not_trigger_the_disagreement_rule(self):
        """Оба прогона по одну сторону порога — расхождения нет.

        Правило обязано ловить переворот, а не срабатывать всегда,
        когда есть второй прогон.
        """
        wide = self._verdict_text({"a": 4.0, "b": 40.0},
                                  {"a": 4.2, "b": 41.0},
                                  self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertNotIn("РАСХОДЯТСЯ", wide)
        narrow = self._verdict_text({"a": 1.5, "b": 1.7},
                                    {"a": 1.6, "b": 1.9},
                                    self.analyze.RECORDED_FLOOR_SECONDS)
        self.assertNotIn("РАСХОДЯТСЯ", narrow)

    def test_disagreement_rule_needs_a_recorded_floor(self):
        """Без записанного порога переворачиваться нечему.

        У холодной сборки абсолютного правила записано не было, и
        выводить расхождение прогонов из порога, которого нет, значило
        бы завести правило задним числом.
        """
        text = self._verdict_text({"a": 9.0, "b": 9.5},
                                  {"a": 0.1, "b": 0.2}, None)
        self.assertNotIn("РАСХОДЯТСЯ", text)

    def _spreads(self, field):
        floor = self.analyze.RECORDED_FLOOR_SECONDS
        second = {r["variant"]: r for r in read_measurements(FIXTURE)}
        first = {r["variant"]: r
                 for r in read_measurements(self.analyze.FIXTURE_RUN1)}
        inside = [n for n in ("system", "vcpkg", "conan")
                  if second[n]["measurement_window"] == "dependency_inside"]
        spread_first = (max(first[n][field] for n in inside)
                        - min(first[n][field] for n in inside))
        spread_second = (max(second[n][field] for n in inside)
                         - min(second[n][field] for n in inside))
        run_to_run = max(abs(second[n][field] - first[n][field])
                         for n in inside)
        return floor, spread_first, spread_second, run_to_run

    def test_neither_warm_nor_rebuild_yields_a_verdict(self):
        """По обеим величинам обязано срабатывать хоть одно правило.

        Это то, что статья говорит о них обеих. Каким именно правилом
        снят вывод, от прогона к прогону меняется, и требовать здесь
        конкретное правило значило бы привязать текст к прогону, а не к
        утверждению.
        """
        for field in ("warm_seconds", "rebuild_seconds"):
            floor, first, second, run_to_run = self._spreads(field)
            fires = (second < floor
                     or run_to_run >= second
                     or (first < floor) != (second < floor))
            self.assertTrue(
                fires,
                "%s: не сработало ни одно правило неинформативности "
                "(плечи %.2f и %.2f, прогон к прогону %.2f, порог %.2f)"
                % (field, first, second, run_to_run, floor))

    def test_no_verdict_for_either_quantity(self):
        """Разбор не делает вывода ни по тёплой сборке, ни по пересборке.

        ПРОВЕРЯЕТСЯ ПЕЧАТЬ РАЗБОРА, А НЕ ВЫДУМАННЫЙ ИНВАРИАНТ. Прежние
        две редакции этой проверки требовали от ДАННЫХ определённого
        поведения, и обе оказались неверны:

          1. «Прогоны обязаны расходиться о применимости правила» —
             в следующей сессии они согласились.
          2. «В каждом прогоне обязано сработать хоть одно правило» —
             в следующей сессии у пересборки на первом прогоне разница
             между плечами вышла 3,14 s при пороге 1,0 и межпрогонном
             разбросе 1,98, и не сработало ни одно.

        Обе редакции требовали устойчивости от величины, которая
        неустойчива, — то есть повторяли ошибку кратностей холодной
        сборки этажом ниже. По восьми снятым сессиям видно: разница
        между плечами по этим величинам гуляет в разы, и какое правило
        сработает, от сессии к сессии меняется.

        Проверять надо не поведение шума, а утверждение статьи. Статья
        говорит: разбор по этим величинам вывода не делает. Это и
        читаем — из печати разбора, а не из собственного пересчёта:
        пересчёт разошёлся бы с разбором молча.
        """
        import contextlib
        analyze = _load("analyze_build_for_verdicts", "analyze-build.py")
        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            analyze.main()
        out = buffer.getvalue()
        for field, head in (("warm_seconds", "тёплая (простой повтор)"),
                            ("rebuild_seconds", "пересборка своего кода")):
            block = out.split(head, 1)
            self.assertEqual(
                len(block), 2,
                "в печати разбора нет блока «%s» — сверять нечего" % head)
            tail = block[1][:400]
            self.assertIn(
                "ВЫВОДА НЕТ", tail,
                "%s: разбор сделал вывод по величине, которую статья "
                "объявляет неиспользуемой. Либо величина стала "
                "информативной и её надо разбирать, либо статью надо "
                "править вслед за данными. Печать: %s" % (field, tail))


if __name__ == "__main__":
    unittest.main()
