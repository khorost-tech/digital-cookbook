"""Ось 4: соседи по языкам. Сначала тождество сервиса, потом уже байты.

ПОЧЕМУ ПОРЯДОК ИМЕННО ТАКОЙ. Размер бинарника имеет смысл сравнивать
ровно до тех пор, пока сравниваются три реализации ОДНОГО сервиса.
Три программы, делающие разное, дадут три числа, между которыми нет
отношения; таблица при этом будет выглядеть ровно так же убедительно.
Поэтому первыми идут проверки того, что сервис один: полный ответ HTTP
на /healthz совпадает побайтно, тело /resolve совпадает после удаления
полей, которые сравнивать нельзя, и поле-исключение variant при этом
различается. Второе исключённое поле — detail: текст ошибки у трёх
реализаций свой, и сравнение его объявляло бы «это не один сервис» при
любом отказе разрешения имени. Исход /resolve сравнивается отдельно.

ОТРИЦАТЕЛЬНЫЕ КОНТРОЛИ ОБЯЗАТЕЛЬНЫ, И ИХ ДВА — ПО ЧИСЛУ РУЧЕК. Плечи
go-divergent и go-resolve-divergent собраны из того же исходника тем же
Dockerfile'ом и отличаются ровно значением одного аргумента сборки
каждое: первое роняет сравнение ответа /healthz, второе — сравнение
тела /resolve. Контроль, роняющий только первую ветку, оставляет вторую
уронённой лишь мутацией копии фикстуры, то есть не живым замером.

ПЛЕЧО ВНЕ ОСИ. cpp-nofmt — тот же сервис на C++ с форматированием через
snprintf. В таблицу и в кратности оно не входит (решение R2 фиксирует,
каким плечом C++ представлен в оси); оно превращает оговорку про
примесь fmt в величину, и его число называется справочным.

ЧЕГО ЗДЕСЬ НЕТ. Здесь нет проверки «rust обязан оказаться меньше go».
Ожидания записаны в expected.json ДО прогона, и превращать их в проверки
значило бы объявлять прогон сломанным всякий раз, когда действительность
разошлась с записанным. Расхождение — находка, её печатает разбор.

ПРО РАЗБРОС. Вторая проверка задания: время до первого ответа не имеет
права стоять в фикстуре без разброса рядом. Число без разброса нельзя ни
принять, ни отвергнуть, и решение о снятии утверждения принимать не по
чему.
"""

import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(HERE, "scripts")
# Путь фикстуры переопределяется окружением: scripts/sabotage.py
# натравливает проверки на КОПИЮ и не трогает файл в репозитории.
FIXTURE = os.environ.get(
    "SIBLINGS_FIXTURE", os.path.join(HERE, "fixtures", "siblings.jsonl"))
FIXTURE_RUN1 = os.environ.get(
    "SIBLINGS_FIXTURE_RUN1",
    os.path.join(HERE, "fixtures", "siblings.run-1.jsonl"))

from scripts.outcome import read_measurements
from scripts.siblings import treatment_field

MEASURED = ("cpp", "go", "rust")
CONTROLS = ("go-divergent", "go-resolve-divergent")
# Плечо вне оси: тот же сервис на C++ без fmt. Оно не входит ни в
# таблицу, ни в кратности — оно отвечает на вопрос «сколько из байтов
# плеча cpp приходится на статически слинкованный fmt», и его число
# называется справочным.
REFERENCE = ("cpp-nofmt",)
ARMS = tuple(sorted(MEASURED + CONTROLS + REFERENCE))
DIVERGENT_PAIR = ("go", "go-divergent")
RESOLVE_DIVERGENT_PAIR = ("go", "go-resolve-divergent")


def _rows(path=None):
    return {r["variant"]: r for r in read_measurements(path or FIXTURE)}


def _runs():
    """Все снятые прогоны оси: проверки идут по каждому, а не по последнему.

    Устройство то же, что на оси воспроизводимости: прогон 1 лежит в
    siblings.run-1.jsonl, прогон 2 — в siblings.jsonl. Проверка,
    читающая только последний, оставила бы первый вне всяких защит, а
    печатался бы он наравне со вторым.
    """
    runs = [("прогон 2", _rows(FIXTURE))]
    if os.path.exists(FIXTURE_RUN1):
        runs.insert(0, ("прогон 1", _rows(FIXTURE_RUN1)))
    return runs


def _load(name, filename):
    """Загружает модуль с дефисом в имени файла."""
    spec = importlib.util.spec_from_file_location(
        name, os.path.join(SCRIPTS, filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _expected():
    with io.open(os.path.join(HERE, "expected.json"), encoding="utf-8") as fh:
        return json.loads(fh.read())["siblings"]


class FixtureShape(unittest.TestCase):
    def test_fixture_exists(self):
        self.assertTrue(os.path.exists(FIXTURE),
                        "нет fixtures/siblings.jsonl — прогон ещё не снят")

    def test_all_arms_present(self):
        for label, rows in _runs():
            self.assertEqual(sorted(rows), sorted(ARMS), label)

    def test_kind_is_sibling(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["kind"], "sibling", "%s/%s" % (label, name))

    def test_no_outcome_field_for_the_measurements(self):
        """У размера исхода не бывает.

        Размер не «разрешился» и не «отказал» — он такой-то. Поле
        outcome в этой строке означало бы, что его выдумали. Исход
        пробы /resolve живёт в отдельном поле resolve_outcome и с
        контрактом строки исхода не смешивается.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotIn("outcome", row, "%s/%s" % (label, name))

    def test_role_matches_the_spec(self):
        for label, rows in _runs():
            for name in MEASURED:
                self.assertEqual(rows[name]["role"], "measured", label)
            for name in CONTROLS:
                self.assertEqual(rows[name]["role"], "control", label)
            for name in REFERENCE:
                self.assertEqual(rows[name]["role"], "reference", label)


class BothRunsAreCheckedTheSameWay(unittest.TestCase):
    """Второй прогон обязан проходить те же защиты, что первый.

    Ось снималась сперва один раз, и устойчивость точных байтов между
    прогонами не была проверена ничем, кроме совпадения одного плеча с
    числом другой оси. Второй прогон без общих защит был бы хуже, чем
    его отсутствие: печатался бы наравне с первым, не пройдя ни улик
    кэша, ни проверки тождества сервиса.
    """

    def test_the_other_run_is_present_and_read(self):
        self.assertTrue(os.path.exists(FIXTURE_RUN1),
                        "нет второго прогона — устойчивость не проверена")
        self.assertEqual(len(_runs()), 2,
                         "проверки читают только один прогон")

    def test_analysis_guards_accept_both_runs(self):
        mod = _load("analyze_siblings", "analyze-siblings.py")
        for label, rows in _runs():
            mod.check_cache_did_not_measure_itself(rows, label)
            mod.check_the_binary_is_the_same_file(rows, label)
            mod.check_every_arm_is_static(rows, label)
            mod.check_the_service_is_the_same(rows, label)
            mod.check_the_controls_diverge(rows, label)

    def test_the_two_runs_are_distinct_measurements(self):
        """Второй прогон обязан быть ВТОРЫМ, а не копией первого.

        Метки прогона у одного и того же плеча в двух фикстурах обязаны
        различаться: одинаковые означали бы, что файл скопирован, а не
        снят заново.
        """
        if len(_runs()) < 2:
            self.skipTest("второго прогона нет")
        (_, first), (_, second) = _runs()
        for name in ARMS:
            self.assertNotEqual(first[name]["nonce_read_back"],
                                second[name]["nonce_read_back"], name)


class TheServiceIsTheSame(unittest.TestCase):
    """Главная проверка оси: сравниваются три реализации одного сервиса."""

    def test_healthz_response_is_byte_identical_across_measured_arms(self):
        for label, rows in _runs():
            reference = rows[MEASURED[0]]["healthz_response"]
            for name in MEASURED[1:]:
                self.assertEqual(
                    rows[name]["healthz_response"], reference,
                    "%s: %s отвечает на /healthz не тем же, что %s — "
                    "сравнивались бы разные программы"
                    % (label, name, MEASURED[0]))

    def test_the_recorded_sum_follows_the_recorded_response(self):
        """Сумма обязана быть выводом из ответа, а не отдельным мнением."""
        import hashlib
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(
                    row["healthz_response_sha256"],
                    hashlib.sha256(
                        row["healthz_response"].encode("utf-8")).hexdigest(),
                    "%s/%s" % (label, name))

    def test_resolve_body_is_identical_apart_from_the_excluded_fields(self):
        for label, rows in _runs():
            reference = rows[MEASURED[0]]["resolve_body_compared"]
            for name in MEASURED[1:]:
                self.assertEqual(rows[name]["resolve_body_compared"],
                                 reference, "%s/%s" % (label, name))

    def test_the_compared_body_carries_no_implementation_specific_text(self):
        """Текст ошибки из сравнения ИСКЛЮЧЁН, и это записано в строке.

        Поле detail несёт текст, который каждая реализация пишет своими
        словами: musl отдаёт имя кода getaddrinfo, Go — текст ошибки
        net.LookupHost, Rust — текст std::io::Error. Сравнивая его,
        любой отказ разрешения имени объявлял бы «это не один сервис» по
        причине, к тождеству отношения не имеющей. Исход при этом
        сравнивается отдельной проверкой и по-прежнему обязан совпасть.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(sorted(row["resolve_excluded_fields"]),
                                 ["detail", "variant"],
                                 "%s/%s" % (label, name))
                self.assertNotIn('"detail"', row["resolve_body_compared"],
                                 "%s/%s" % (label, name))
                self.assertNotIn('"variant"', row["resolve_body_compared"],
                                 "%s/%s" % (label, name))

    def test_the_excluded_variant_field_really_differs(self):
        """Поле, объявленное исключением, обязано различать плечи.

        «Различается ровно одно» верно только тогда, когда это одно
        действительно различается. Проверка, где исключённое поле у всех
        плеч совпадает, зелена по недоразумению. Про detail этого
        требовать нельзя и не нужно: при успешном разрешении имени он
        пуст у всех, и исключён он по другой причине — он не сравним
        между реализациями.
        """
        for label, rows in _runs():
            reported = [rows[n]["resolve_reported_variant"] for n in MEASURED]
            self.assertEqual(len(set(reported)), len(reported),
                             "%s: %r" % (label, reported))

    def test_every_arm_reports_its_own_name(self):
        """Плечи go, go-divergent и go-resolve-divergent делят образ.

        Имя варианта запечено в образ через ENV и у них поэтому одно и
        то же. Если бы имя не передавалось при запуске явно, все три
        плеча назвали бы себя первым именем.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["resolve_reported_variant"], name, label)

    def test_resolve_succeeded_everywhere(self):
        """Разрешение имени обязано пройти у всех плеч.

        Отказ означал бы, что плечи делают РАЗНОЕ — одно резолвит, другое
        нет, — и сравнивать их размеры нельзя. Проверяется ИСХОД, а не
        текст: текст у трёх реализаций свой.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["resolve_outcome"], "resolved",
                                 "%s/%s" % (label, name))


class NameResolutionMechanismIsNamed(unittest.TestCase):
    """Тождество доказано совпадением ОТВЕТОВ, а не совпадением работы.

    C++ и Rust зовут getaddrinfo той же libc, с которой слинкованы. Go
    при CGO_ENABLED=0 разрешает имена СОБСТВЕННЫМ резолвером внутри
    бинарника: он не может позвать getaddrinfo, потому что не может
    позвать C вовсе. Это разница того же рода, что признанная примесь
    fmt, и она обязана быть названа в данных, а не только подразумеваться.
    """

    def test_every_arm_records_its_name_resolution_mechanism(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertTrue(str(row["name_resolution"]).strip(),
                                "%s/%s" % (label, name))

    def test_go_cannot_call_getaddrinfo(self):
        """Улика, а не объявление: CGO_ENABLED читается из образа.

        При выключенном cgo у бинарника нет способа позвать функцию
        libc, то есть механизм разрешения имён у Go заведомо не тот, что
        у соседей. Значение печатает сам шаг сборки в файл лечения,
        измеритель читает его оттуда.
        """
        for label, rows in _runs():
            for name in ("go", "go-divergent", "go-resolve-divergent"):
                self.assertEqual(
                    treatment_field(rows[name]["treatment"], "CGO_ENABLED"),
                    "0", "%s/%s" % (label, name))

    def test_go_is_not_described_as_using_the_libc_resolver(self):
        for label, rows in _runs():
            self.assertNotIn("getaddrinfo", rows["go"]["name_resolution"],
                             label)
            for name in ("cpp", "rust", "cpp-nofmt"):
                self.assertIn("getaddrinfo", rows[name]["name_resolution"],
                              "%s/%s" % (label, name))


class MeasurerCanSayDifferent(unittest.TestCase):
    """Проверка тождества обязана уметь ответить «различаются».

    Ручек две, и живой контроль нужен КАЖДОЙ: контроль, роняющий только
    /healthz, оставляет ветку сравнения тела /resolve уроненной лишь
    мутацией копии фикстуры.
    """

    def test_the_healthz_control_diverges_from_its_twin(self):
        for label, rows in _runs():
            base, control = DIVERGENT_PAIR
            self.assertNotEqual(
                rows[control]["healthz_response_sha256"],
                rows[base]["healthz_response_sha256"],
                "%s: отрицательный контроль совпал с плечом go — проверка "
                "тождества отвечает «совпало» при любых данных" % label)

    def test_the_resolve_control_diverges_from_its_twin(self):
        """Живой контроль ветки /resolve.

        Плечо go-resolve-divergent собрано с другим значением поля probe
        и обязано разойтись с go по СРАВНИВАЕМОМУ телу /resolve — при
        совпадающем ответе /healthz. Без него ветка сравнения тела
        зелена только потому, что тела совпадают.
        """
        for label, rows in _runs():
            base, control = RESOLVE_DIVERGENT_PAIR
            self.assertNotEqual(rows[control]["resolve_body_compared"],
                                rows[base]["resolve_body_compared"], label)
            self.assertEqual(rows[control]["healthz_response_sha256"],
                             rows[base]["healthz_response_sha256"],
                             "%s: контроль ветки /resolve разошёлся ещё и по "
                             "/healthz — он бы падал не на той ветке" % label)

    def _normalized_argv(self, row, varying):
        argv = list(row["docker_argv"])
        out = []
        skip = False
        for i, item in enumerate(argv):
            if skip:
                skip = False
                continue
            if item == "--build-arg" and i + 1 < len(argv) and any(
                    argv[i + 1].startswith(k + "=") for k in varying):
                skip = True
                continue
            out.append(item)
        # Имя тега несёт имя плеча — оно различается по устройству.
        return [x for x in out
                if not x.startswith("cpp-build-and-package-sibling")]

    def test_the_healthz_control_pair_differs_by_exactly_one_build_argument(
            self):
        """Пара контролируема, только если различается ровно одно.

        У go и go-divergent один исходник и один Dockerfile; в команде
        сборки различаться имеют право ровно два аргумента — метка
        прогона (она у каждого плеча своя по устройству) и
        HEALTHZ_BODY. Всё остальное обязано совпадать, иначе расхождение
        ответов объясняется не тем, чем задумано.
        """
        for label, rows in _runs():
            base, control = DIVERGENT_PAIR
            varying = ("STAND_NONCE", "HEALTHZ_BODY")
            self.assertEqual(self._normalized_argv(rows[control], varying),
                             self._normalized_argv(rows[base], varying), label)
            self.assertEqual(rows[control]["dockerfile"],
                             rows[base]["dockerfile"], label)
            self.assertNotEqual(
                treatment_field(rows[control]["treatment"], "HEALTHZ_BODY"),
                treatment_field(rows[base]["treatment"], "HEALTHZ_BODY"),
                label)
            self.assertEqual(
                treatment_field(rows[control]["treatment"], "RESOLVE_PROBE"),
                treatment_field(rows[base]["treatment"], "RESOLVE_PROBE"),
                "%s: контроль /healthz различается ещё и полем probe" % label)

    def test_the_resolve_control_pair_differs_by_exactly_one_build_argument(
            self):
        for label, rows in _runs():
            base, control = RESOLVE_DIVERGENT_PAIR
            varying = ("STAND_NONCE", "RESOLVE_PROBE")
            self.assertEqual(self._normalized_argv(rows[control], varying),
                             self._normalized_argv(rows[base], varying), label)
            self.assertEqual(rows[control]["dockerfile"],
                             rows[base]["dockerfile"], label)
            self.assertNotEqual(
                treatment_field(rows[control]["treatment"], "RESOLVE_PROBE"),
                treatment_field(rows[base]["treatment"], "RESOLVE_PROBE"),
                label)
            self.assertEqual(
                treatment_field(rows[control]["treatment"], "HEALTHZ_BODY"),
                treatment_field(rows[base]["treatment"], "HEALTHZ_BODY"),
                "%s: контроль /resolve различается ещё и телом /healthz"
                % label)


class CacheDidNotMeasureItself(unittest.TestCase):
    """Каждый образ обязан быть собран в этом прогоне."""

    def test_the_nonce_read_back_matches_the_one_passed(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["nonce_read_back"], row["nonce_passed"],
                                 "%s/%s" % (label, name))

    def test_the_nonce_is_present_in_the_command_that_ran(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertIn("STAND_NONCE=%s" % row["nonce_passed"],
                              row["docker_argv"], "%s/%s" % (label, name))

    def test_nonces_are_pairwise_distinct(self):
        for label, rows in _runs():
            values = [r["nonce_read_back"] for r in rows.values()]
            self.assertEqual(len(set(values)), len(values),
                             "%s: %r" % (label, values))

    def test_binary_mtimes_are_pairwise_distinct(self):
        for label, rows in _runs():
            values = [r["binary_mtime"] for r in rows.values()]
            self.assertEqual(len(set(values)), len(values),
                             "%s: %r" % (label, values))


class TheMeasuredBinaryIsTheOneInTheImage(unittest.TestCase):
    """Факты ELF снимаются в ОБРАЗЕ СБОРКИ, размер — в ФИНАЛЬНОМ.

    Что это один и тот же файл, до сих пор не показывалось ничем: между
    двумя стадиями стоит COPY, и любая правка Dockerfile'а могла бы
    развести их — записи NEEDED и метка относились бы к одному файлу, а
    байты к другому. Сумма снимается в обоих местах и обязана совпасть.
    """

    def test_both_sums_are_recorded(self):
        for label, rows in _runs():
            for name, row in rows.items():
                for field in ("binary_sha256_in_builder",
                              "binary_sha256_in_final"):
                    self.assertRegex(str(row[field]), r"^[0-9a-f]{64}$",
                                     "%s/%s/%s" % (label, name, field))

    def test_the_two_sums_agree(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["binary_sha256_in_builder"],
                                 row["binary_sha256_in_final"],
                                 "%s: у плеча %s замеряются два разных файла"
                                 % (label, name))


class StaticLinkageMakesSizesComparable(unittest.TestCase):
    """Самодостаточность меряется по бинарнику, а не по ответу ldd.

    Ответ ldd текстовый и зависит от libc и от того, PIE ли бинарник: у
    статического PIE (так собирает Rust на musl по умолчанию) ldd
    печатает путь загрузчика, и разбор его текста дал бы «одна
    зависимость» там, где зависимостей нет вовсе.
    """

    def test_no_arm_has_shared_dependencies(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["dynamic_dep_count"], 0,
                                 "%s/%s: %r" % (label, name,
                                                row["dynamic_deps"]))

    def test_the_count_follows_the_list(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["dynamic_dep_count"],
                                 len(row["dynamic_deps"]),
                                 "%s/%s" % (label, name))

    def test_no_arm_expects_a_program_interpreter(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertFalse(row["elf_has_interpreter"],
                                 "%s/%s" % (label, name))

    def test_elf_type_is_recorded(self):
        """Тип ELF записывается, потому что плечи по нему РАЗЛИЧАЮТСЯ.

        Не-PIE у одного и статический PIE у другого — это разные способы
        собрать самодостаточный бинарник, и разница в размере частично
        живёт здесь. Замолчать это поле значило бы приписать разницу
        языку.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertTrue(str(row["elf_type"]).strip(),
                                "%s/%s" % (label, name))


class StripIsMeasuredNotDeclared(unittest.TestCase):
    """Поле strip_step фиксирует ОБЪЯВЛЕНИЕ шага, а не его результат.

    У C++ и Rust отладочная информация убирается утилитой strip, у Go —
    ключами линковщика -s -w. Инструменты разные, и «убрано одинаково»
    было до сих пор рассуждением: строка лечения говорит, что шаг
    объявлен, и ничего не говорит про то, что осталось в бинарнике.
    Результат снимается тем же readelf'ом, которым сняты NEEDED и
    PT_INTERP: ни таблицы символов, ни секций .debug_*.
    """

    def test_no_arm_keeps_a_symbol_table(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertFalse(row["elf_has_symtab"],
                                 "%s: у плеча %s осталась .symtab"
                                 % (label, name))

    def test_no_arm_keeps_debug_sections(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertEqual(row["elf_debug_sections"], [],
                                 "%s/%s" % (label, name))

    def test_the_sections_were_actually_read(self):
        """Пустота обязана быть прочитанной, а не отсутствием чтения.

        «Секций .debug_* нет» проходит и на бинарнике, у которого не
        прочитано ни одной секции вовсе. Число секций записывается рядом
        и обязано быть больше нуля.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertGreater(row["elf_section_count"], 0,
                                   "%s/%s" % (label, name))


class ImageSizeHasTwoNamedScales(unittest.TestCase):
    """Безымянную шкалу читатель называет сам — и называет неверно.

    docker image inspect --format {{.Size}} на хранилище containerd
    отдаёт объём СЖАТОГО содержимого образа; отсюда и число меньше
    лежащего внутри бинарника, и расхождение кратностей по образам с
    кратностями по бинарникам. Сжимаемость у трёх языков разная, и
    сравнение по этой шкале — сравнение цены хранения в реестре, а не
    цены на диске. Обе величины нужны, и каждая обязана называться тем,
    что она есть.
    """

    def test_the_unnamed_field_is_gone(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertNotIn("image_bytes", row,
                                 "%s: безымянная шкала вернулась в строку %s"
                                 % (label, name))

    def test_both_scales_are_recorded(self):
        for label, rows in _runs():
            for name, row in rows.items():
                for field in ("image_registry_bytes", "image_unpacked_bytes"):
                    self.assertGreater(row[field], 0,
                                       "%s/%s/%s" % (label, name, field))

    def test_the_method_of_each_scale_is_recorded(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertTrue(str(row["image_registry_method"]).strip(),
                                "%s/%s" % (label, name))
                self.assertTrue(str(row["image_unpacked_method"]).strip(),
                                "%s/%s" % (label, name))

    def test_the_unpacked_scale_contains_the_binary(self):
        """Распакованный объём обязан быть не меньше лежащего внутри файла.

        Это и есть проверка того, что шкала названа верно: величина, у
        которой образ меньше своего единственного файла, распакованным
        объёмом не является.
        """
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertGreaterEqual(row["image_unpacked_bytes"],
                                        row["binary_bytes"],
                                        "%s/%s" % (label, name))


class TheFmtShareHasAReferenceMeasurement(unittest.TestCase):
    """Оговорка про fmt превращена в величину.

    Решение R2 фиксирует, каким плечом C++ представлен В ОСИ, и не
    запрещает справочный замер вне оси. Плечо cpp-nofmt — тот же сервис
    с форматированием через snprintf, тот же Dockerfile, то же
    окружение; в таблицу оси и в кратности оно не входит, и его число
    называется справочным.
    """

    def test_the_reference_arm_is_present_and_marked(self):
        for label, rows in _runs():
            self.assertIn("cpp-nofmt", rows, label)
            self.assertEqual(rows["cpp-nofmt"]["role"], "reference", label)

    def test_the_reference_arm_declares_no_external_library(self):
        for label, rows in _runs():
            self.assertEqual(
                rows["cpp-nofmt"]["external_libraries_declared"], [], label)

    def test_the_reference_arm_is_the_same_service(self):
        """Иначе разность байтов не про fmt, а про две разные программы."""
        for label, rows in _runs():
            self.assertEqual(rows["cpp-nofmt"]["healthz_response_sha256"],
                             rows["cpp"]["healthz_response_sha256"], label)
            self.assertEqual(rows["cpp-nofmt"]["resolve_body_compared"],
                             rows["cpp"]["resolve_body_compared"], label)

    def test_the_reference_arm_shares_the_toolchain_with_cpp(self):
        for label, rows in _runs():
            self.assertEqual(rows["cpp-nofmt"]["toolchain"],
                             rows["cpp"]["toolchain"], label)
            self.assertEqual(rows["cpp-nofmt"]["strip_step"],
                             rows["cpp"]["strip_step"], label)

    def test_the_reference_arm_is_smaller_than_the_axis_arm(self):
        """Справочное число обязано быть выводимо, а не переписано глазами."""
        for label, rows in _runs():
            self.assertLess(rows["cpp-nofmt"]["binary_bytes"],
                            rows["cpp"]["binary_bytes"], label)

    def test_the_reference_arm_is_excluded_from_the_measured_axis(self):
        """Добавление cpp-nofmt в MEASURED не уронило бы ни один тест.

        `_ratios` ходит по MEASURED, и все проверки кратностей на этой же
        фикстуре, вычисляющие ожидание тем же MEASURED, что и код,
        остались бы зелены, просто честно посчитав кратность по четырём
        плечам вместо трёх. Здесь MEASURED сверяется с независимо
        объявленной константой этого файла (строка 56), а не с самой
        собой — ровно поэтому подмена ловится.
        """
        mod = _load("analyze_siblings", "analyze-siblings.py")
        self.assertEqual(mod.MEASURED, MEASURED)
        self.assertNotIn("cpp-nofmt", mod.MEASURED)


class DependenciesAreMeasuredNotDeclared(unittest.TestCase):
    """Число зависимостей из манифеста, который я же и написал, — не замер.

    Уликой служит то, что сборка прошла при выключенном доступе к
    реестрам пакетов, и значения этих переменных читаются ИЗ ОБРАЗА, а
    не берутся из Dockerfile.
    """

    def test_go_was_built_with_the_module_proxy_off(self):
        for label, rows in _runs():
            treatment = rows["go"]["treatment"]
            self.assertEqual(treatment_field(treatment, "GOPROXY"), "off",
                             label)
            self.assertEqual(treatment_field(treatment, "GOTOOLCHAIN"),
                             "local", label)

    def test_rust_was_built_offline(self):
        for label, rows in _runs():
            treatment = rows["rust"]["treatment"]
            self.assertEqual(treatment_field(treatment, "CARGO_NET_OFFLINE"),
                             "true", label)
            self.assertIn("--offline",
                          treatment_field(treatment, "CARGO_FLAGS"), label)

    def test_the_siblings_pulled_in_nothing_external(self):
        for label, rows in _runs():
            self.assertEqual(
                treatment_field(rows["go"]["treatment"], "EXTERNAL_MODULES"),
                "0", label)
            self.assertEqual(
                treatment_field(rows["rust"]["treatment"], "EXTERNAL_CRATES"),
                "0", label)

    def test_the_cpp_arm_declares_its_external_library(self):
        """Оговорка про fmt обязана быть видна в данных, а не только в прозе.

        Плечо C++ несёт статически слинкованную библиотеку, у соседей
        аналога нет. Если это перестанет быть записано в строке, разбор
        напечатает разницу размеров без важнейшей оговорки к ней.
        """
        for label, rows in _runs():
            row = rows["cpp"]
            self.assertTrue(row["external_libraries_declared"], label)
            self.assertIn("fmt",
                          " ".join(row["external_libraries_declared"]), label)


class SpreadIsRecordedNextToTheNumber(unittest.TestCase):
    """Время без разброса рядом — число, которое нельзя ни принять, ни снять."""

    def test_every_arm_records_a_spread(self):
        for label, rows in _runs():
            for name, row in rows.items():
                for field in ("first_response_ms", "first_response_min_ms",
                              "first_response_max_ms", "spread_ms",
                              "first_response_samples"):
                    self.assertIn(field, row, "%s/%s" % (label, name))
                    self.assertIsNotNone(row[field],
                                         "%s/%s/%s" % (label, name, field))

    def test_spread_follows_the_min_and_max(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertAlmostEqual(
                    row["spread_ms"],
                    row["first_response_max_ms"]
                    - row["first_response_min_ms"],
                    places=1, msg="%s/%s" % (label, name))

    def test_median_lies_inside_the_spread(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertLessEqual(row["first_response_min_ms"],
                                     row["first_response_ms"],
                                     "%s/%s" % (label, name))
                self.assertLessEqual(row["first_response_ms"],
                                     row["first_response_max_ms"],
                                     "%s/%s" % (label, name))

    def test_more_than_one_sample_per_arm(self):
        for label, rows in _runs():
            for name, row in rows.items():
                self.assertGreater(row["first_response_samples"], 1,
                                   "%s/%s" % (label, name))


class ExpectationsWereRecordedBeforeTheRun(unittest.TestCase):
    def test_block_declares_itself_written_before_the_run(self):
        self.assertTrue(_expected()["written_before_run"])

    def test_no_probe_results_are_filed_as_predictions(self):
        """Блока с известным из проб быть не должно — проб не было.

        Ожидания записаны раньше, чем существовали исходники соседей.
        Если такой блок появится, значит запись правили задним числом, а
        предрегистрация этим уничтожается.
        """
        self.assertNotIn("known_from_pre_run_probes", _expected())

    def test_the_time_rule_is_recorded_with_its_literal_form(self):
        text = _expected()["hypothesis_before_run"][
            "decision_taken_before_the_run"]
        self.assertIn("больше или равен", text)
        self.assertIn("СНИМАЕТСЯ", text)

    def test_the_fmt_confounder_is_named_before_the_run(self):
        claims = _expected()["claims_that_will_not_be_made"]
        self.assertIn("fmt", claims["about_the_fmt_confounder"])

    def test_performance_claims_are_forbidden_before_the_run(self):
        claims = _expected()["claims_that_will_not_be_made"]
        self.assertIn("R19", claims["about_performance"])

    def test_prevalence_claims_are_forbidden_before_the_run(self):
        claims = _expected()["claims_that_will_not_be_made"]
        self.assertIn("большинство", claims["about_prevalence"])


class WriteTimeContract(unittest.TestCase):
    """Контракт строки проверяется ПРИ ЗАПИСИ, а не только над фикстурой."""

    def setUp(self):
        import hashlib
        self.mod = _load("run_siblings", "run-siblings.py")
        response = ("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n"
                    "Content-Length: 2\r\nConnection: close\r\n\r\nok")
        self.good = {
            "kind": "sibling", "variant": "go", "role": "measured",
            "language": "Go", "dockerfile": "Dockerfile.sibling-go",
            "docker_argv": ["docker", "build", "--build-arg",
                            "STAND_NONCE=n-go", "-t", "x", "."],
            "build_seconds": 3.0,
            "nonce_passed": "n-go", "nonce_read_back": "n-go",
            "binary_mtime": "2026-09-05 10:00:00.111111111 +0000",
            "treatment": "LANG=[go] TOOLCHAIN=[go version go1.24 linux/amd64] "
                         "GOPROXY=[off] CGO_ENABLED=[0] STRIP=[ldflags -s -w]",
            "toolchain": "go version go1.24 linux/amd64",
            "strip_step": "ldflags -s -w",
            "image_registry_bytes": 100,
            "image_registry_method": "docker image inspect --format {{.Size}}",
            "image_unpacked_bytes": 95,
            "image_unpacked_method": "docker export, сумма размеров файлов",
            "binary_bytes": 90,
            "binary_sha256_in_builder": "a" * 64,
            "binary_sha256_in_final": "a" * 64,
            "dynamic_deps": [], "dynamic_dep_count": 0,
            "elf_type": "EXEC (Executable file)",
            "elf_has_interpreter": False,
            "elf_has_symtab": False,
            "elf_debug_sections": [],
            "elf_section_count": 12,
            "name_resolution": "собственный резолвер Go внутри бинарника",
            "healthz_response": response,
            "healthz_response_sha256":
                hashlib.sha256(response.encode("utf-8")).hexdigest(),
            "healthz_response_bytes": len(response.encode("utf-8")),
            "resolve_outcome": "resolved", "resolve_detail": "",
            "resolve_reported_variant": "go",
            "resolve_excluded_fields": ["detail", "variant"],
            "resolve_body_compared":
                '{"kind": "package", "outcome": "resolved", '
                '"probe": "resolve"}',
            "first_response_ms": 450.0, "first_response_min_ms": 430.0,
            "first_response_max_ms": 490.0, "spread_ms": 60.0,
            "first_response_samples": 11,
            "external_libraries_declared": [],
        }

    def test_good_row_passes(self):
        self.mod.check_row_contract(dict(self.good))

    def test_missing_field_is_refused(self):
        row = dict(self.good)
        del row["binary_mtime"]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_nonce_that_did_not_come_back_is_refused(self):
        row = dict(self.good)
        row["nonce_read_back"] = "чужая-метка"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_nonce_absent_from_the_command_is_refused(self):
        row = dict(self.good)
        row["docker_argv"] = ["docker", "build", "-t", "x", "."]
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_dep_count_that_disagrees_with_the_list_is_refused(self):
        row = dict(self.good)
        row["dynamic_dep_count"] = 2
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_sum_that_does_not_follow_the_response_is_refused(self):
        row = dict(self.good)
        row["healthz_response_sha256"] = "0" * 64
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_service_reporting_someone_elses_name_is_refused(self):
        row = dict(self.good)
        row["resolve_reported_variant"] = "go-divergent"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_toolchain_not_named_by_the_treatment_is_refused(self):
        row = dict(self.good)
        row["toolchain"] = "go version go1.99 linux/amd64"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_spread_that_does_not_follow_min_and_max_is_refused(self):
        row = dict(self.good)
        row["spread_ms"] = 5.0
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_two_sums_of_the_binary_that_disagree_are_refused(self):
        row = dict(self.good)
        row["binary_sha256_in_final"] = "b" * 64
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_unpacked_image_smaller_than_its_binary_is_refused(self):
        """Шкала, у которой образ меньше своего файла, названа неверно."""
        row = dict(self.good)
        row["image_unpacked_bytes"] = row["binary_bytes"] - 1
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_compared_body_carrying_an_excluded_field_is_refused(self):
        row = dict(self.good)
        row["resolve_body_compared"] = (
            '{"detail": "", "kind": "package", "outcome": "resolved"}')
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def test_unknown_role_is_refused(self):
        row = dict(self.good)
        row["role"] = "прочее"
        with self.assertRaises(SystemExit):
            self.mod.check_row_contract(row)

    def _twin(self, **changes):
        row = dict(self.good)
        row["variant"] = "rust"
        row["resolve_reported_variant"] = "rust"
        row["nonce_passed"] = "n-rust"
        row["nonce_read_back"] = "n-rust"
        row["docker_argv"] = ["docker", "build", "--build-arg",
                              "STAND_NONCE=n-rust", "-t", "x", "."]
        row["binary_mtime"] = "2026-09-05 10:00:00.222222222 +0000"
        row.update(changes)
        return row

    def _full_run(self):
        rows = [dict(self.good)]
        for i, (name, role) in enumerate((
                ("rust", "measured"), ("go-divergent", "control"),
                ("go-resolve-divergent", "control"),
                ("cpp-nofmt", "reference"))):
            rows.append(self._twin(
                variant=name, role=role, resolve_reported_variant=name,
                nonce_passed="n-%d" % i, nonce_read_back="n-%d" % i,
                docker_argv=["docker", "build", "--build-arg",
                             "STAND_NONCE=n-%d" % i, "-t", "x", "."],
                binary_mtime="2026-09-05 10:00:00.%09d +0000" % (i + 3)))
        return rows

    def test_run_with_distinct_arms_passes(self):
        self.mod.check_run_contract(self._full_run())

    def test_shared_mtime_between_two_arms_is_refused(self):
        rows = self._full_run()
        rows[1]["binary_mtime"] = rows[0]["binary_mtime"]
        with self.assertRaises(SystemExit):
            self.mod.check_run_contract(rows)

    def test_shared_nonce_between_two_arms_is_refused(self):
        rows = self._full_run()
        rows[1]["nonce_passed"] = rows[0]["nonce_passed"]
        rows[1]["nonce_read_back"] = rows[0]["nonce_read_back"]
        with self.assertRaises(SystemExit):
            self.mod.check_run_contract(rows)

    def test_run_without_a_control_is_refused(self):
        rows = [r for r in self._full_run() if r["role"] != "control"]
        with self.assertRaises(SystemExit):
            self.mod.check_run_contract(rows)

    def test_run_with_only_one_of_the_two_controls_is_refused(self):
        """Ручек две, и живой контроль нужен каждой."""
        rows = [r for r in self._full_run()
                if r["variant"] != "go-resolve-divergent"]
        with self.assertRaises(SystemExit):
            self.mod.check_run_contract(rows)


class AnalysisGuards(unittest.TestCase):
    """Разбор обязан останавливаться до печати таблицы."""

    def setUp(self):
        self.mod = _load("analyze_siblings", "analyze-siblings.py")
        self.rows = _rows()

    def _copy(self):
        return {k: dict(v) for k, v in self.rows.items()}

    def test_healthy_run_passes_the_guards(self):
        self.mod.check_cache_did_not_measure_itself(self.rows)
        self.mod.check_the_binary_is_the_same_file(self.rows)
        self.mod.check_every_arm_is_static(self.rows)
        self.mod.check_the_service_is_the_same(self.rows)
        self.mod.check_the_controls_diverge(self.rows)

    def test_nonce_that_did_not_come_back_stops_the_analysis(self):
        rows = self._copy()
        rows["go"]["nonce_read_back"] = "старая-метка"
        with self.assertRaises(SystemExit):
            self.mod.check_cache_did_not_measure_itself(rows)

    def test_mtime_shared_between_arms_stops_the_analysis(self):
        rows = self._copy()
        rows["go"]["binary_mtime"] = rows["rust"]["binary_mtime"]
        with self.assertRaises(SystemExit):
            self.mod.check_cache_did_not_measure_itself(rows)

    def test_two_different_files_stop_the_analysis(self):
        rows = self._copy()
        rows["go"]["binary_sha256_in_final"] = "0" * 64
        with self.assertRaises(SystemExit):
            self.mod.check_the_binary_is_the_same_file(rows)

    def test_a_dynamically_linked_arm_stops_the_analysis(self):
        rows = self._copy()
        rows["go"]["dynamic_deps"] = ["libc.so.6"]
        rows["go"]["dynamic_dep_count"] = 1
        with self.assertRaises(SystemExit):
            self.mod.check_every_arm_is_static(rows)

    def test_an_arm_that_needs_a_loader_stops_the_analysis(self):
        rows = self._copy()
        rows["rust"]["elf_has_interpreter"] = True
        with self.assertRaises(SystemExit):
            self.mod.check_every_arm_is_static(rows)

    def test_a_debug_section_left_in_place_stops_the_analysis(self):
        rows = self._copy()
        rows["go"]["elf_debug_sections"] = [".debug_info"]
        with self.assertRaises(SystemExit):
            self.mod.check_every_arm_is_static(rows)

    def test_different_healthz_stops_the_analysis(self):
        rows = self._copy()
        rows["go"]["healthz_response_sha256"] = "1" * 64
        with self.assertRaises(SystemExit):
            self.mod.check_the_service_is_the_same(rows)

    def test_different_resolve_body_stops_the_analysis(self):
        rows = self._copy()
        rows["rust"]["resolve_body_compared"] = '{"probe": "other"}'
        with self.assertRaises(SystemExit):
            self.mod.check_the_service_is_the_same(rows)

    def test_excluded_field_that_does_not_differ_stops_the_analysis(self):
        rows = self._copy()
        for name in MEASURED:
            rows[name]["resolve_reported_variant"] = "cpp"
        with self.assertRaises(SystemExit):
            self.mod.check_the_service_is_the_same(rows)

    def test_a_failed_resolve_stops_the_analysis_with_its_own_message(self):
        """Отказ разрешения имени — не расхождение реализаций.

        Раньше он и не мог быть отличён: текст ошибки входил в
        сравниваемое тело, и любой отказ DNS печатал «ЭТО НЕ ОДИН
        СЕРВИС». Теперь у него своё сообщение.
        """
        rows = self._copy()
        rows["rust"]["resolve_outcome"] = "refused"
        rows["rust"]["resolve_detail"] = "EAI_AGAIN"
        with self.assertRaises(SystemExit) as caught:
            self.mod.check_the_service_is_the_same(rows)
        self.assertNotIn("ЭТО НЕ ОДИН СЕРВИС", str(caught.exception))

    def test_converged_healthz_control_stops_the_analysis(self):
        rows = self._copy()
        rows["go-divergent"]["healthz_response_sha256"] = \
            rows["go"]["healthz_response_sha256"]
        with self.assertRaises(SystemExit):
            self.mod.check_the_controls_diverge(rows)

    def test_converged_resolve_control_stops_the_analysis(self):
        rows = self._copy()
        rows["go-resolve-divergent"]["resolve_body_compared"] = \
            rows["go"]["resolve_body_compared"]
        with self.assertRaises(SystemExit):
            self.mod.check_the_controls_diverge(rows)


class TimeRuleIsAppliedLiterally(unittest.TestCase):
    """Правило про разброс — то самое, что записано до прогона.

    Проверяется на выдуманных числах, а не на снятых: снятые могут дать
    только один из двух ответов, и вторая ветка правила осталась бы
    непроверенной. Граница проверяется отдельно: без случая
    within == between замена записанного «больше или равен» на «больше»
    прошла бы незамеченной — оба прежних случая её переживали.
    """

    def setUp(self):
        self.mod = _load("analyze_siblings", "analyze-siblings.py")
        self.rows = {k: dict(v) for k, v in _rows().items()}
        self.expected = _expected()

    def _set_time(self, name, median, low, high):
        self.rows[name].update(
            first_response_ms=median, first_response_min_ms=low,
            first_response_max_ms=high, spread_ms=round(high - low, 1),
            first_response_samples=11)

    def test_wide_spread_withdraws_the_claim(self):
        self._set_time("cpp", 400.0, 350.0, 500.0)
        self._set_time("go", 410.0, 360.0, 510.0)
        self._set_time("rust", 405.0, 355.0, 505.0)
        self.assertFalse(self.mod.print_time(self.rows, self.expected))

    def test_narrow_spread_keeps_the_claim(self):
        self._set_time("cpp", 400.0, 399.0, 401.0)
        self._set_time("go", 800.0, 799.0, 801.0)
        self._set_time("rust", 405.0, 404.0, 406.0)
        self.assertTrue(self.mod.print_time(self.rows, self.expected))

    def test_equal_spread_and_gap_withdraws_the_claim(self):
        """Граница правила: разброс РАВЕН разнице.

        Записано «больше или равен» — значит на равенстве утверждение
        снимается. Правило «больше», дающее тот же ответ во всех
        остальных случаях, здесь расходится с записанным, и без этого
        случая подмена была бы невидима.
        """
        self._set_time("cpp", 400.0, 395.0, 405.0)
        self._set_time("go", 410.0, 405.0, 415.0)
        self._set_time("rust", 405.0, 400.0, 410.0)
        self.assertFalse(self.mod.print_time(self.rows, self.expected))

    def test_the_rule_in_the_code_is_checked_against_the_recorded_one(self):
        """Связь кода с предрегистрацией держится проверкой, а не совестью.

        Разбор читает записанную формулировку и останавливается, если
        она перестала быть той, которую он применяет.
        """
        expected = json.loads(json.dumps(self.expected))
        expected["hypothesis_before_run"]["decision_taken_before_the_run"] = \
            "если разброс СТРОГО больше разницы — утверждение СНИМАЕТСЯ"
        self._set_time("cpp", 400.0, 395.0, 405.0)
        with self.assertRaises(SystemExit):
            self.mod.print_time(self.rows, expected)


class AnalysisPrintsWhatTheReportKnows(unittest.TestCase):
    """До читателя стенда доезжает печать разбора, а не отчёт задачи.

    Раздел «что эти числа не значат» печатал четыре пункта из восьми:
    ни область прогонов, ни запрет на утверждения о распространённости
    (записанный в expected.json ЗАРАНЕЕ) в печать не попадали.
    """

    def setUp(self):
        self.mod = _load("analyze_siblings", "analyze-siblings.py")
        self.rows = _rows()
        self.expected = _expected()

    def _capture(self, func, *args):
        import contextlib
        import io as _io
        buffer = _io.StringIO()
        with contextlib.redirect_stdout(buffer):
            func(*args)
        return buffer.getvalue()

    def test_the_caveats_name_the_number_of_runs(self):
        """Слова «прогон» тут мало: оно есть в соседних строках.

        Печатается ровно та формулировка, которую разбор строит из
        наличия второй фикстуры, — иначе проверка зелена от любого
        упоминания прогона в тексте.
        """
        text = self._capture(self.mod.print_size_caveats, self.rows,
                             self.expected)
        self.assertIn(self.mod._runs_note(), text)

    def test_the_caveats_forbid_prevalence_claims(self):
        text = self._capture(self.mod.print_size_caveats, self.rows,
                             self.expected)
        self.assertIn("распространённост", text)

    def test_the_caveats_are_quoted_from_the_pre_registration(self):
        """Печать берёт формулировки из expected.json, а не пересказывает их."""
        text = self._capture(self.mod.print_size_caveats, self.rows,
                             self.expected)
        claims = self.expected["claims_that_will_not_be_made"]
        for key in ("about_prevalence", "about_performance",
                    "about_the_fmt_confounder"):
            self.assertIn(claims[key], text, key)

    def test_both_size_scales_are_named_in_print(self):
        text = self._capture(self.mod.print_table, self.rows)
        self.assertIn("сжат", text)
        self.assertIn("распакован", text)

    def test_image_ratios_are_printed_by_the_code(self):
        """Кратности по образам не имели права жить только в прозе отчёта.

        Проверяется не заголовок, а числа: заголовок можно переименовать,
        и «кратности по образам напечатаны» осталось бы верным по
        подстроке от соседнего раздела. Здесь каждая кратность
        пересчитывается из фикстуры и ищется в выводе дословно.
        """
        text = self._capture(self.mod.print_table, self.rows)
        for field in ("binary_bytes", "image_registry_bytes",
                      "image_unpacked_bytes"):
            smallest = min(MEASURED, key=lambda n: self.rows[n][field])
            for name in MEASURED:
                if name == smallest:
                    continue
                value = (float(self.rows[name][field])
                         / float(self.rows[smallest][field]))
                self.assertIn("%s / %s = %.1f (посчитано %.3f)"
                              % (name, smallest, value, value),
                              text, field)

    def test_ratios_are_printed_with_the_precision_the_prose_uses(self):
        import re
        text = self._capture(self.mod.print_table, self.rows)
        self.assertRegex(text, r"\d+,\d{3}|\d+\.\d{3}")

    def test_the_fmt_reference_number_is_printed_next_to_the_ratio(self):
        """Оговорка превращена в величину только если величина напечатана.

        Строки cpp-nofmt в таблице мало: она стоит там наравне с
        контролями, и «число справочного плеча напечатано» было бы верно
        и без разбора того, что это число значит рядом с кратностью.
        Проверяется сама разность и перевёрнутая кратность.
        """
        text = self._capture(self.mod.print_table, self.rows)
        cpp = self.rows["cpp"]["binary_bytes"]
        nofmt = self.rows["cpp-nofmt"]["binary_bytes"]
        smallest = min(MEASURED, key=lambda n: self.rows[n]["binary_bytes"])
        self.assertIn("разность %d Б" % (cpp - nofmt), text)
        self.assertIn(
            "%s / cpp-nofmt = %.1f"
            % (smallest, float(self.rows[smallest]["binary_bytes"])
               / float(nofmt)), text)

    def test_the_resolver_difference_is_printed(self):
        text = self._capture(self.mod.print_dependency_evidence, self.rows)
        self.assertIn("РАЗРЕШЕНИЕ ИМЁН", text)
        self.assertIn(self.rows["go"]["name_resolution"], text)

    def test_the_ldd_replacement_is_disclosed_next_to_the_verdict(self):
        """Печать цитирует ldd из записанного и не может остановиться там.

        Записанная строка называет ldd; измеритель годы назад переведён на
        записи NEEDED (коммит 4f6fffd) ровно потому, что ldd над
        статическим PIE Rust дал бы ложную зависимость. Причина расписана
        в коде и в отчёте, но до печати не доезжала — а печать, не отчёт,
        видит читатель стенда.
        """
        text = self._capture(self.mod.print_hypotheses, self.rows,
                             self.expected, True)
        self.assertIn("ИНСТРУМЕНТ ЗАМЕНЁН", text)
        self.assertIn("NEEDED", text)
        self.assertIn("статическим PIE", text)

    def test_the_binary_ratio_block_points_to_the_fmt_reference(self):
        """Кратность по бинарнику вырезают из статьи отдельно от fmt.

        Блок «КРАТНОСТИ ПО РАЗМЕРУ БИНАРНИКА» стоит НАД справочным числом
        fmt, а не рядом с ним: вырезанный отдельно, он читается без всякой
        оговорки, и читатель уносит «C++ вдвое больше Rust» без того, что
        переворачивает вывод. Указатель обязан ехать в самом этом блоке.
        """
        text = self._capture(self.mod.print_table, self.rows)
        ratio_block = text.split("КРАТНОСТИ ПО РАЗМЕРУ БИНАРНИКА")[1]
        ratio_block = ratio_block.split("КРАТНОСТИ ПО ОБРАЗАМ")[0]
        self.assertIn("Справочное число ниже", ratio_block)
        self.assertIn("меняет", ratio_block)


if __name__ == "__main__":
    unittest.main()
