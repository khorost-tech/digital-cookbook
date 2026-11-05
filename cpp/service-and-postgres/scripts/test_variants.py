"""Три варианта одного сервиса — и различается у них ТОЛЬКО слой HTTP.

ЗАЧЕМ ЭТИ ПРОВЕРКИ СУЩЕСТВУЮТ. В волне A пара вариантов различалась
образом сборки целиком — вместе с компилятором, — и причинную трактовку
пришлось отзывать уже после того, как статьи были написаны. Там это
удерживалось именем образа в JSON, то есть договорённостью.

ПОЧЕМУ ОНИ ПЕРЕПИСАНЫ. Первая редакция держала ФОРМУ записей: из
семнадцати чужих диверсий девять прошли зелёными. Мимо проходило всё, что
сохраняло текст и меняло смысл: `env=` поверх фиксированных `-e`, ручка,
запечённая в ENTRYPOINT мимо инструкции `ENV`, `workers + 1` рядом с
ожидаемым словом, три разных тега, каждый из которых содержал объявленную
строку как подстроку.

Отсюда три яруса, и ни одного из них не хватает поодиночке:

1. ТЕКСТ — симметрия трёх файлов слоя, точное (не подстрокой) равенство
   базового образа, запрет общего инструмента и расходящихся флагов;
2. ПОВЕДЕНИЕ — `scripts/test_dispatch_behaviour.py` собирает логику и
   спрашивает у неё ответы на конкретные входы; здесь же `RunCommand`
   спрашивает у сборщика команды, что именно уедет в докер;
3. ЗАМЕР — `fixtures/variants.jsonl`: версии инструментов, флаги сборки
   общей библиотеки, содержимое точки входа образа, поведение при
   испорченной настройке и число одновременно обслуженных проб. Числа
   существуют как замер, а не как проза: прозу можно заменить неверной, и
   в волне A ровно так и вышло.

ЧЕГО ЗДЕСЬ НЕТ. Поведения под нагрузкой как предмета: его меряют оси.
Здесь удерживаются свойства постановки, которые ось не увидит.
"""

import io
import json
import os
import re
import shutil
import unittest

from scripts import dbwait
from scripts import docker as dockerlib
from scripts import outcome
from scripts.cppsource import function_body, strip_comments

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES = os.path.join(HERE, "images")
SERVICE = os.path.join(HERE, "service")
FIXTURE = os.path.join(HERE, "fixtures", "variants.jsonl")
DB_WAIT_FIXTURE = os.path.join(HERE, "fixtures", "db-wait.jsonl")
LAYERS = ("beast", "crow", "drogon")

# Ручки запуска. Ни одна из них не имеет права оказаться внутри образа:
# образ общий, и запечённое значение склеило бы два варианта в одну
# строку фикстуры — причём прогон выглядел бы успешным.
# Ручек запуска стало три: размер пула соединений — такая же величина,
# как имя варианта и число рабочих единиц, и запечённая в образ она
# склеила бы две настройки оси 2 в одну строку фикстуры.
KNOBS = ("STAND_VARIANT", "STAND_WORKERS", "STAND_POOL")

# Всё, что делает сервис сервисом, а не слоем: компилятор, система
# сборки, клиент базы. Появление любого из этих имён в файле слоя
# означает, что варианты различаются не только слоем.
COMMON_TOOLING = ("g++", "gcc", "cmake", "libpqxx", "build-essential",
                  "clang", "make")

# Команды CMake, которым в ЭТОМ файле делать нечего вовсе: они меняют то,
# ЧЕМ и КАК собирается код, а не то, с какой библиотекой он линкуется.
# Стандарт и режим сборки в список не входят: они задаются здесь один раз
# и одинаково для трёх вариантов, а условная их правка ловится разрешённым
# списком внутри ветки слоя.
FORBIDDEN_ANYWHERE_IN_CMAKE = (
    "target_compile_options", "add_compile_options",
    "target_compile_definitions", "add_definitions",
    "target_compile_features", "set_target_properties",
    "target_link_options", "add_link_options",
    "set_source_files_properties", "CMAKE_CXX_FLAGS",
    "CMAKE_INTERPROCEDURAL_OPTIMIZATION")

# Что ветке слоя ПОЗВОЛЕНО. Список разрешённого, а не запрещённого:
# перечень запретов неизбежно неполон — так и уехало target_link_options.
ALLOWED_IN_LAYER_BRANCH = ("find_package", "target_link_libraries",
                           "message", "if", "elseif", "else", "endif")


def _read(path):
    with io.open(path, encoding="utf-8") as handle:
        return handle.read()


def instructions(text):
    """Инструкции Dockerfile: без комментариев, со склеенными переносами."""
    out = []
    current = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.endswith("\\"):
            current.append(line[:-1].strip())
            continue
        current.append(line)
        out.append(" ".join(part for part in current if part))
        current = []
    if current:
        out.append(" ".join(part for part in current if part))
    return out


def strip_comments_cmake(text):
    """Текст CMake без комментариев: объяснение не сходит за код и не
    подсовывает проверке скобки из прозы."""
    return "\n".join(line.split("#", 1)[0] for line in text.splitlines())


def strip_comments_sh(text):
    """Строки сценария без комментариев: объяснение не сходит за код."""
    return "\n".join(line for line in text.splitlines()
                     if not line.strip().startswith("#"))


def dockerfiles():
    return sorted(name for name in os.listdir(IMAGES)
                  if name.startswith("Dockerfile."))


def layer_dockerfile(layer):
    return os.path.join(IMAGES, "Dockerfile.%s" % layer)


def _mask(line, layer):
    """Заменяет имя слоя на метку — то, чем файлам различаться позволено."""
    return re.sub(r"\b%s\b" % re.escape(layer), "СЛОЙ", line)


def _from_reference(line):
    """Образ, названный в инструкции FROM, — ровно один токен.

    Сравнение подстрокой здесь не годится: три разных тега, каждый из
    которых СОДЕРЖИТ объявленную строку, прошли бы как один образ.
    """
    parts = line.split()
    if len(parts) < 2 or parts[0].upper() != "FROM":
        raise AssertionError("не инструкция FROM: %s" % line)
    return parts[1]


def entry_main(layer):
    code = strip_comments(_read(os.path.join(SERVICE, "%s_main.cpp" % layer)))
    return code, function_body(code, r"int\s+main\s*\(")


def fixture_rows():
    """Строки замера, по одной на вариант."""
    return {row["variant"]: row for row in outcome.read_measurements(FIXTURE)}


class Variants(unittest.TestCase):
    """Объявление вариантов — и то, что в нём обязано быть проверяемым."""

    def test_three_variants_share_the_base_image(self):
        spec = dockerlib.variants()
        self.assertEqual(sorted(spec), ["beast", "crow", "drogon"])
        bases = {v["base_image"] for v in spec.values()}
        self.assertEqual(len(bases), 1,
                         "варианты собираются разными базовыми образами — "
                         "различается не только слой HTTP")

    def test_the_variant_name_is_the_layer_it_carries(self):
        for name, v in dockerlib.variants().items():
            self.assertEqual(name, v["layer"])

    def test_worker_knob_names_the_call_the_entry_point_makes(self):
        # Свободная проза про ручку ничего не держит: её можно заменить
        # неверной, и читатель сравнил бы не то, что сравнивалось. Поэтому
        # объявление называет ВЫРАЖЕНИЕ, которое обязано стоять в точке
        # входа дословно, — и здесь оно там ищется.
        for name, v in dockerlib.variants().items():
            knob = v["worker_knob"]
            self.assertTrue(str(knob.get("unit", "")).strip(),
                            "%s: не названа рабочая единица" % name)
            expression = knob["count_expression"]
            self.assertIn("workers", expression,
                          "%s: выражение ручки не упоминает величину" % name)
            self.assertNotRegex(
                expression, r"[-+*/]",
                "%s: в выражении ручки арифметика — заданное число и "
                "применённое разошлись бы" % name)
            _, main = entry_main(name)
            # Дословное вхождение И запрет арифметики сразу после него:
            # `index < workers + 1` содержит `index < workers` как
            # подстроку и прошёл бы проверку на вхождение.
            self.assertRegex(
                main,
                re.escape(expression) + r"(?![ \t]*[-+*/])",
                "%s: точка входа не применяет ручку так, как объявлено: %r"
                % (name, expression))

    def test_every_variant_names_the_default_it_overrides(self):
        # Дефолты числа рабочих единиц у трёх библиотек разные. Величина,
        # которую стенд задаёт явно, обязана быть названа вместе с той,
        # которую она вытесняет.
        for name, v in dockerlib.variants().items():
            self.assertTrue(str(v.get("worker_default", "")).strip(),
                            "%s: не назван дефолт, который вытесняется "
                            "явной настройкой" % name)

    def test_observed_is_a_measurement_and_not_a_mechanism(self):
        # Объявление говорит, ЧТО наблюдалось, и не говорит, ПОЧЕМУ: «один
        # поток пула занят приёмом» — механизм, выданный за факт, а
        # замерено было число одновременно обслуженных проб.
        for name, v in dockerlib.variants().items():
            seen = v["observed"]
            for field in ("workers", "probes", "served_concurrently"):
                self.assertIsInstance(
                    seen[field], int,
                    "%s: %s не число — наблюдение подменено прозой"
                    % (name, field))
            self.assertIn(seen["kind"], ("measured", "existential"))
            self.assertLessEqual(seen["served_concurrently"], seen["probes"])
            self.assertTrue(str(seen.get("where", "")).strip(),
                            "%s: не сказано, чем снято наблюдение" % name)


class DockerfileSymmetry(unittest.TestCase):
    """Три файла слоёв — одинаковые шаги, одинаковый порядок, разный слой."""

    def test_layer_dockerfiles_are_line_by_line_symmetric(self):
        listings = {layer: instructions(_read(layer_dockerfile(layer)))
                    for layer in LAYERS}
        masked = {layer: [_mask(line, layer) for line in lines]
                  for layer, lines in listings.items()}
        first = LAYERS[0]
        for other in LAYERS[1:]:
            self.assertEqual(
                masked[first], masked[other],
                "Dockerfile.%s и Dockerfile.%s различаются не только слоем"
                % (first, other))

    def test_the_layer_name_actually_occurs_in_each_file(self):
        for layer in LAYERS:
            lines = instructions(_read(layer_dockerfile(layer)))
            self.assertTrue(any(re.search(r"\b%s\b" % layer, line)
                                for line in lines),
                            "Dockerfile.%s нигде не называет свой слой"
                            % layer)

    def test_layer_files_differ_from_each_other_before_masking(self):
        listings = [tuple(instructions(_read(layer_dockerfile(layer))))
                    for layer in LAYERS]
        self.assertEqual(len(set(listings)), len(LAYERS),
                         "два Dockerfile'а слоёв совпадают дословно — "
                         "один из вариантов собирает чужой слой")


class BaseImage(unittest.TestCase):
    """Базовый образ один, закреплён дайджестом, и в нём весь инструмент."""

    def test_external_base_is_pinned_by_digest(self):
        text = _read(os.path.join(IMAGES, "Dockerfile.base"))
        froms = [line for line in instructions(text)
                 if line.upper().startswith("FROM ")]
        self.assertTrue(froms, "в Dockerfile.base нет инструкции FROM")
        for line in froms:
            self.assertRegex(
                line, r"@sha256:[0-9a-f]{64}\b",
                "внешний образ закреплён тегом, а не дайджестом: %s" % line)

    def test_layer_files_build_on_exactly_the_declared_base(self):
        declared = {v["base_image"] for v in dockerlib.variants().values()}
        self.assertEqual(len(declared), 1)
        base = declared.pop()
        seen = set()
        for layer in LAYERS:
            froms = [line
                     for line in instructions(_read(layer_dockerfile(layer)))
                     if line.upper().startswith("FROM ")]
            self.assertEqual(len(froms), 1,
                             "Dockerfile.%s собирается не одной стадией"
                             % layer)
            reference = _from_reference(froms[0])
            seen.add(reference)
            # РАВЕНСТВО, а не вхождение: `…-base:latest-beast`,
            # `…-base:latest2` и `…-base:latest` — три разных образа, и
            # каждый содержит объявленную строку.
            self.assertEqual(
                reference, base,
                "Dockerfile.%s наследуется не от объявленного базового "
                "образа: %r вместо %r" % (layer, reference, base))
        self.assertEqual(len(seen), 1)

    def test_common_tooling_lives_only_in_the_base(self):
        base = _read(os.path.join(IMAGES, "Dockerfile.base")).lower()
        for token in ("g++", "cmake", "libpqxx"):
            self.assertIn(token, base, "базовый образ не ставит %s" % token)
        sources = [layer_dockerfile(layer) for layer in LAYERS]
        sources.append(os.path.join(IMAGES, "install-layer.sh"))
        for path in sources:
            body = strip_comments_sh(_read(path)).lower()
            for token in COMMON_TOOLING:
                self.assertNotIn(
                    token, body,
                    "%s ставит общий инструмент %r — различается не только "
                    "слой" % (os.path.basename(path), token))

    def test_the_crow_header_is_pinned_and_checked_before_use(self):
        text = _read(os.path.join(IMAGES, "install-layer.sh"))
        self.assertRegex(text, r"\b[0-9a-f]{64}\b",
                         "заголовок Crow не закреплён суммой")
        self.assertNotRegex(
            text, r"download/(latest|main|master)/",
            "версия заголовка Crow — подвижный указатель")
        self.assertLess(
            text.index("sha256sum -c"), text.index("install -m 0644"),
            "сумма сверяется после установки заголовка — сверка не решает "
            "ничего")

    def test_no_dockerfile_mentions_a_startup_knob_at_all(self):
        # Прежняя редакция смотрела только на инструкции, начинающиеся с
        # `ENV `, и пропускала `ENTRYPOINT ["/bin/sh","-c","STAND_WORKERS=2
        # exec …"]`: симметрия трёх файлов при этом сохранялась, а ручка
        # оказывалась в образе. Ручке нечего делать в файле образа ни в
        # каком виде.
        for name in dockerfiles():
            for line in instructions(_read(os.path.join(IMAGES, name))):
                for knob in KNOBS:
                    self.assertNotIn(
                        knob, line,
                        "%s несёт ручку запуска в образе: %s" % (name, line))

    def test_no_script_inside_the_images_mentions_a_startup_knob(self):
        # Сценарии установки слоя и сборки исполняются ВНУТРИ образа, и
        # запечь ручку они могут не хуже инструкции: обёртка на месте
        # бинарника выставила бы её и мимо ENV, и мимо ENTRYPOINT. То, что
        # обёртки нет, снято ещё и замером (entry_binary в фикстуре); здесь
        # запрет держится текстом.
        for name in ("install-layer.sh", "build-service.sh"):
            body = strip_comments_sh(_read(os.path.join(IMAGES, name)))
            for knob in KNOBS:
                self.assertNotIn(knob, body,
                                 "%s несёт ручку запуска внутрь образа"
                                 % name)


class BuildIsCommon(unittest.TestCase):
    """Сборка общая: вторая переменная не живёт в CMake и в сценарии."""

    def test_nothing_in_cmake_touches_how_the_common_code_is_built(self):
        # ЧИТАЕТСЯ ВЕСЬ ФАЙЛ, а не хвост от первого STREQUAL: правка выше
        # ветвления меняет сборку ровно так же, а прежняя проверка на неё
        # даже не смотрела. Этим командам в этом файле делать нечего
        # вообще — ни в ветке слоя, ни до неё.
        text = strip_comments_cmake(
            _read(os.path.join(SERVICE, "CMakeLists.txt")))
        for token in FORBIDDEN_ANYWHERE_IN_CMAKE:
            self.assertNotIn(
                token, text,
                "CMakeLists меняет то, ЧЕМ и КАК собирается общий код: %s"
                % token)

    def test_the_layer_branch_only_links_a_library(self):
        # Перечень запрещённых команд неизбежно неполон (так и уехало
        # target_link_options). Поэтому ветка слоя проверяется наоборот —
        # СПИСКОМ РАЗРЕШЁННОГО: найти библиотеку, слинковать её, объяснить
        # отказ.
        text = strip_comments_cmake(
            _read(os.path.join(SERVICE, "CMakeLists.txt")))
        # Ищется ЛЮБОЕ ветвление по слою, а не только STREQUAL: условие,
        # записанное иначе (MATCHES) и стоящее выше, осталось бы
        # непрочитанным.
        start = text.find("if(STAND_LAYER")
        self.assertGreater(start, 0, "в CMakeLists нет ветвления по слою")
        allowed = ALLOWED_IN_LAYER_BRANCH
        for line in text[start:].splitlines():
            line = line.strip()
            if not line or "(" not in line:
                continue
            command = line.split("(", 1)[0].strip()
            if not command:
                continue
            self.assertIn(
                command, allowed,
                "ветка слоя делает не то, ради чего она есть: %s" % line)

    def test_the_build_script_does_not_branch_on_the_layer(self):
        # Сценарий сборки получает имя слоя РОВНО для того, чтобы передать
        # его в -DSTAND_LAYER. Любое ветвление здесь — расхождение флагов
        # по слоям, которого не покажет ни одна версия инструмента.
        text = strip_comments_sh(
            _read(os.path.join(IMAGES, "build-service.sh")))
        for token in ("case", "elif", "&&", "||"):
            self.assertNotIn(token, text,
                             "сценарий сборки ветвится: %s" % token)
        for layer in LAYERS:
            self.assertNotIn(layer, text,
                             "сценарий сборки называет слой по имени: %s"
                             % layer)
        cmake_lines = [line for line in text.splitlines()
                       if line.strip().startswith("cmake ")]
        self.assertEqual(len(cmake_lines), 2,
                         "настройка и сборка обязаны быть по одной команде")


class EntryPointSources(unittest.TestCase):
    """Точки входа: имя и число рабочих единиц читаются при СТАРТЕ."""

    def test_every_layer_has_its_entry_point(self):
        for layer in LAYERS:
            self.assertTrue(
                os.path.exists(os.path.join(SERVICE, "%s_main.cpp" % layer)),
                "нет точки входа слоя %s" % layer)

    def test_entry_points_read_both_knobs_before_opening_the_port(self):
        openers = re.compile(r"\.run\(|listen\(|addListener\(|bind\(")
        for layer in LAYERS:
            _, main = entry_main(layer)
            for call in ("stand_variant()", "stand_workers()",
                         "stand_pool_size()"):
                self.assertIn(call, main,
                              "%s не читает %s при старте" % (layer, call))
                opener = openers.search(main)
                self.assertIsNotNone(
                    opener, "%s: не найдено открытие порта" % layer)
                self.assertLess(
                    main.index(call), opener.start(),
                    "%s читает %s после открытия порта" % (layer, call))

    def test_entry_points_do_not_swallow_a_broken_setting(self):
        # Чтение, обёрнутое в try/catch с запасным значением, сохраняет и
        # вызов, и его место, а порт при испорченной настройке всё равно
        # открывается. Здесь это ловится текстом; то, что отказ ДОЕЗЖАЕТ
        # до кода возврата и порт не открывается, снято замером
        # (фикстура, startup_refusals).
        for layer in LAYERS:
            _, main = entry_main(layer)
            head = main[:main.index("stand_workers()")]
            self.assertNotIn(
                "catch", head,
                "%s ловит отказ настройки ДО чтения ручек — запасное "
                "значение открыло бы порт при испорченной настройке" % layer)

    def test_entry_points_do_not_default_the_knobs(self):
        for layer in LAYERS:
            code, main = entry_main(layer)
            self.assertNotIn("getenv", code,
                             "%s читает окружение сам — имя и число рабочих "
                             "единиц обязаны приходить из общего места"
                             % layer)
            self.assertIsNone(
                re.search(r'variant\s*=\s*"', main),
                "%s подставляет литерал вместо имени варианта" % layer)
            for forbidden in ('"beast"', '"drogon"', '"crow"'):
                self.assertNotIn(forbidden, code,
                                 "%s: имя варианта запечено в исходник: %s"
                                 % (layer, forbidden))

    def test_one_name_reaches_the_handlers(self):
        dispatch = strip_comments(_read(os.path.join(SERVICE, "dispatch.cpp")))
        body = function_body(dispatch, r"Reply\s+dispatch\s*\(")
        # Имя варианта и ПРОЧИТАННАЯ занятость уходят в ручку вместе:
        # занятость берётся у сторожа входа, а не сочиняется ручкой.
        self.assertRegex(
            body,
            r"query\(\s*\w+\s*,\s*variant\s*,\s*guard\.entered_with\(\)\s*\)")
        self.assertRegex(
            body,
            r"block\(\s*\w+\s*,\s*variant\s*,\s*guard\.entered_with\(\)\s*\)")
        self.assertIn("healthz()", body)
        # Сторож стоит в ОБЩЕМ разборе, по одному на каждую ручку
        # ожидания: перенесённый в слой, он считал бы три раза по-своему,
        # и варианты различались бы ещё и способом счёта.
        self.assertEqual(
            len(re.findall(r"InFlight\s+guard\s*;", body)), 2,
            "сторож входа стоит не у обеих ручек ожидания")
        for layer in LAYERS:
            code, _ = entry_main(layer)
            self.assertNotIn(
                "InFlight", code,
                "%s считает занятость сам — счёт обязан быть один на три "
                "слоя" % layer)
        for layer in LAYERS:
            code, _ = entry_main(layer)
            self.assertIn("dispatch(", code,
                          "%s не отдаёт запрос общему разбору" % layer)
            for path in ('"/healthz"', '"/q"', '"/block"', '"sleep_ms"',
                         '"ms"'):
                self.assertNotIn(path, code,
                                 "%s маршрутизирует сам: %s" % (layer, path))

    def test_beast_serves_inside_a_coroutine(self):
        # Слой beast представляет в серии модель «корутины C++20 поверх
        # Asio», и статья 2 волны — про неё. Синхронная обработка в потоке
        # дала бы ту же таблицу чисел, но про другую модель.
        code, _ = entry_main("beast")
        for token in ("awaitable<", "co_spawn", "co_await", "use_awaitable"):
            self.assertIn(token, code,
                          "beast собран не корутинами: нет %s" % token)
        for operation in ("async_read(", "async_write("):
            self.assertIn(
                operation, code,
                "beast делает ввод-вывод синхронно (%s нет) — корутина, в "
                "которой нечего ждать, модели не меняет" % operation)


class CountingRule(unittest.TestCase):
    """Правило счёта — без докера, на придуманных залпах.

    Это единственная часть замера, которую можно испортить, не тронув ни
    одного образа: читай она, скажем, число ответов вместо наибольшего
    прочитанного, и любая ручка выглядела бы подключённой, а фикстура
    сообщала бы заданное число.
    """

    def test_the_peak_is_the_largest_reading(self):
        self.assertEqual(
            dockerlib.peak_of({"inflight_by_probe": [1, 1, 2],
                               "healthz_calls": []}), 2)
        self.assertEqual(
            dockerlib.peak_of({"inflight_by_probe": [1],
                               "healthz_calls": [{"value": 0}]}), 1)
        with self.assertRaises(ValueError):
            dockerlib.peak_of({"inflight_by_probe": [],
                               "healthz_calls": []})

    def test_the_peak_reads_the_liveness_numbers_too(self):
        # Ответы проб приходят ПОСЛЕ того, как часть из них уже вышла, и
        # вошедший последним мог застать в полёте одного себя. Маршрут
        # живости спрашивают ВО ВРЕМЯ залпа, и его число бывает больше.
        # Счёт, читающий только ответы проб, занизил бы занятость — и
        # занизил бы в сторону, подтверждающую тезис волны.
        self.assertEqual(
            dockerlib.peak_of({"inflight_by_probe": [1, 1, 1],
                               "healthz_calls": [{"value": 0},
                                                 {"value": 3}]}), 3)
        # Молчание маршрута живости (value=None) не годится в наблюдение —
        # ни в max, ни куда-либо ещё.
        self.assertEqual(
            dockerlib.peak_of({"inflight_by_probe": [1],
                               "healthz_calls": [{"value": None}]}), 1)

    def test_a_reply_without_the_count_is_not_a_measurement(self):
        line = '{"kind":"service","variant":"beast","probe":"block",' \
               '"outcome":"served","inflight":2,"detail":"ms=1000"}'
        self.assertEqual(dockerlib.parse_reply(line, least=1)["inflight"], 2)
        with self.assertRaises(RuntimeError):
            dockerlib.parse_reply('{"outcome":"served"}', least=0)
        with self.assertRaises(RuntimeError):
            dockerlib.parse_reply('{"inflight":true}', least=0)
        with self.assertRaises(RuntimeError):
            dockerlib.parse_reply('{"inflight":"2"}', least=0)
        # Ответ ручки ожидания обязан считать хотя бы себя: ноль означал
        # бы, что счётчик к обработчику не подключён вовсе.
        with self.assertRaises(RuntimeError):
            dockerlib.parse_reply('{"inflight":0}', least=1)
        # А маршрут живости на пустом сервисе обязан читаться нулём —
        # значит нулю здесь место.
        self.assertEqual(
            dockerlib.parse_reply('{"inflight":0}', least=0)["inflight"], 0)

    def test_the_liveness_summary_follows_the_volleys(self):
        # ЗДЕСЬ НЕТ ГОТОВОГО ВЫВОДА «ОТВЕЧАЛ». Круг 5 нашёл: маршрут
        # живости встаёт в очередь при полной занятости и отвечает только
        # когда одна из единиц освободилась — прежнее поле
        # answered_every_time утверждало больше, чем данные лицензируют.
        # Здесь проверяется только сырое сведение: сколько раз спрошено,
        # сколько раз получен ответ, какая была наибольшая задержка.
        volleys = [
            {"workers": 3, "healthz_calls": [
                {"value": 0, "delay_ms": 1},
                {"value": 2, "delay_ms": 900},
                {"value": 2, "delay_ms": 1}]},
            {"workers": 3, "healthz_calls": [
                {"value": 1, "delay_ms": 2},
                {"value": None, "delay_ms": 2000}]},
        ]
        seen = dockerlib.liveness_of(volleys)
        self.assertEqual(seen["workers"], 3)
        self.assertEqual(seen["asks"], 5)
        self.assertEqual(seen["answers"], 4)
        self.assertFalse(seen["every_call_got_a_value"])
        self.assertEqual(seen["max_delay_ms"], 2000)
        self.assertEqual(seen["asked_while_busy"], 3)
        self.assertEqual(seen["max_read_while_busy"], 2)
        whole = dockerlib.liveness_of([volleys[0]])
        self.assertTrue(whole["every_call_got_a_value"])
        with self.assertRaises(ValueError):
            dockerlib.liveness_of([])
        # Залпы, снятые при РАЗНЫХ настройках, не имеют права молча
        # смешаться в один вывод: печать оси иначе могла бы читать
        # живость с ДРУГОЙ настройки, а поле "workers" фикстуры осталось
        # бы прежним.
        with self.assertRaises(ValueError):
            dockerlib.liveness_of(
                [{"workers": 1, "healthz_calls": []},
                 {"workers": 3, "healthz_calls": []}])


class PureRules(unittest.TestCase):
    """Правила, которые можно испортить, не тронув ни одного образа."""

    def test_the_positive_control_needs_both_ends(self):
        # Счётчик — измеряющий код внутри предмета. Одна настройка не
        # отличает подключённую ручку от отключённой, а «при трёх больше»
        # без «при одной ровно одна» не отличает счётчик занятости от
        # счётчика чего угодно растущего.
        self.assertTrue(
            dockerlib.knob_evidence([1, 1], [2, 3])["follows_workers"])
        self.assertFalse(
            dockerlib.knob_evidence([2, 1], [3, 3])["follows_workers"],
            "при одной единице прочитано двое — счётчик считает не то")
        self.assertFalse(
            dockerlib.knob_evidence([1, 1], [1, 1])["follows_workers"],
            "число не тронулось с настройкой — ручка не подключена")
        with self.assertRaises(ValueError):
            dockerlib.knob_evidence([1], [2, 2])
        with self.assertRaises(ValueError):
            dockerlib.knob_evidence([], [])

    def test_the_published_number_is_the_smallest_run(self):
        self.assertEqual(dockerlib.published_concurrency([3, 1, 2]), 1)
        self.assertEqual(dockerlib.published_concurrency([2, 2]), 2)
        with self.assertRaises(ValueError):
            dockerlib.published_concurrency([])

    def test_retries_are_bounded_by_a_constant(self):
        self.assertLessEqual(
            dockerlib.MAX_ATTEMPTS, 3,
            "повтор существует, чтобы пережить отказ инструмента, а не "
            "чтобы выпрашивать удачный залп")
        with self.assertRaises(ValueError):
            dockerlib.measure_run("beast", workers=1, probes=1,
                                  attempts=dockerlib.MAX_ATTEMPTS + 1)

    def test_the_wait_is_short_because_nothing_is_timed(self):
        # Ожидание перестало быть ценой деления инструмента: занятость
        # читается счётчиком. Длинное ожидание теперь означало бы, что
        # время где-то всё ещё меряется, — а вместе с ним вернулся бы и
        # растянутый прогон, и чужая нагрузка в окне замера.
        self.assertLessEqual(
            dockerlib.WAIT_MS, 2000,
            "ожидание снова длинное: занятость где-то выводится из "
            "времени, а не читается")
        self.assertGreaterEqual(
            dockerlib.PROBE_COUNT, dockerlib.HIGH_WORKERS + 1,
            "проб в залпе не больше, чем единиц: «в полёте все» не "
            "отличалось бы от «в полёте сколько задано»")

    def test_occupancy_needs_every_run_not_some_run(self):
        # Главный вердикт волны: ожидание в базе занимает рабочую единицу.
        # «Хотя бы в одном залпе» — другое утверждение, и оно держится
        # даже на сервисе, который единицу не занимает.
        self.assertTrue(
            dbwait.verdict([1, 1, 1], [2, 2, 2], probes=2)["occupies_unit"])
        self.assertFalse(
            dbwait.verdict([1, 2, 1], [2, 2, 2], probes=2)["occupies_unit"],
            "занятость объявлена по одному залпу из трёх")
        self.assertFalse(
            dbwait.verdict([2, 2, 2], [2, 2, 2], probes=2)["occupies_unit"])
        control = dbwait.verdict([1, 1], [2, 1], probes=2)
        self.assertFalse(control["control_overlaps_always"])
        self.assertTrue(control["control_overlaps_ever"])



class RunCommand(unittest.TestCase):
    """Ручки передаются при ЗАПУСКЕ — и никем не перебиваются."""

    def test_run_command_passes_every_knob(self):
        cmd = dockerlib.run_variant_command("crow", workers=4, pool=8)
        joined = " ".join(cmd)
        self.assertIn("STAND_VARIANT=crow", joined)
        self.assertIn("STAND_WORKERS=4", joined)
        self.assertIn("STAND_POOL=8", joined)
        self.assertIn(dockerlib.image_tag("crow"), cmd)

    def test_extra_environment_cannot_override_a_knob(self):
        # docker берёт ПОСЛЕДНЕЕ значение повторённой переменной. Словарь,
        # дописанный после фиксированных -e, поднимал бы контейнер чужим
        # вариантом с чужим числом рабочих единиц, а run_variant отдал бы
        # оси прежние значения: фикстура сообщила бы не то, что мерилось.
        for knob in KNOBS:
            with self.assertRaises(ValueError):
                dockerlib.run_variant_command("crow", workers=4, pool=8,
                                              env={knob: "подмена"})

    def test_run_command_refuses_to_guess_the_pool_size(self):
        # До оси 2 эта проверка требовала обратного: довод pool
        # ОТВЕРГАЛСЯ, потому что пула не держал ни один слой и ручка ни на
        # что не влияла. Теперь пул есть, и отвергается противоположное —
        # умолчание: вызов, не назвавший размер, сравнивал бы пул сам с
        # собой, а контроль оси 2 и есть пара по этому размеру.
        with self.assertRaises(ValueError):
            dockerlib.run_variant_command("crow", workers=4)

    def test_run_command_refuses_a_worker_count_that_is_not_a_count(self):
        for bad in (0, -1, "восемь", None):
            with self.assertRaises((ValueError, TypeError)):
                dockerlib.run_variant_command("crow", workers=bad)


class Fixture(unittest.TestCase):
    """Замер трёх образов — тот, на который ссылается отчёт.

    Числа здесь не проза: они сняты `scripts/run-variants.py` с собранных
    образов. Проза в JSON и в отчёте расходится с делом молча — в волне A
    именно так и вышло.
    """

    def setUp(self):
        if not os.path.exists(FIXTURE):
            self.fail("нет замера вариантов: %s" % FIXTURE)
        self.rows = fixture_rows()

    def test_the_fixture_covers_exactly_the_declared_variants(self):
        self.assertEqual(sorted(self.rows), sorted(dockerlib.variants()))

    def test_tool_versions_agree_across_the_three_images(self):
        distinct = {json.dumps(row["tools"], sort_keys=True,
                               ensure_ascii=False)
                    for row in self.rows.values()}
        self.assertEqual(
            len(distinct), 1,
            "версии инструментов в образах вариантов разошлись: %s"
            % sorted(distinct))
        for name, row in self.rows.items():
            for field in ("cxx", "cmake", "libpqxx"):
                self.assertTrue(
                    any(ch.isdigit() for ch in row["tools"][field]),
                    "%s: %s измерился без единой цифры — версией это не "
                    "является" % (name, field))

    def test_the_common_code_is_built_with_the_same_flags_everywhere(self):
        # Расхождение флагов — вторая переменная, которую не видно ни по
        # версии компилятора, ни по тексту Dockerfile'а: завести её можно
        # и в CMakeLists, и в сценарии сборки. Здесь она видна: флаги
        # сняты у ОБЩЕЙ библиотеки, код которой у трёх вариантов один.
        for field in ("common_build_flags", "common_build_defines",
                      "common_build_includes"):
            distinct = {row[field] for row in self.rows.values()}
            self.assertEqual(
                len(distinct), 1,
                "%s расходятся по вариантам: %s" % (field, sorted(distinct)))

    def test_each_image_carries_its_own_layer_library(self):
        versions = {}
        for name, row in self.rows.items():
            library = row["layer_library"]
            self.assertTrue(str(library["name"]).strip())
            self.assertTrue(
                any(ch.isdigit() for ch in str(library["version"])),
                "%s: версия библиотеки слоя снята без единой цифры" % name)
            versions[name] = "%s %s" % (library["name"], library["version"])
        self.assertEqual(len(set(versions.values())), len(versions),
                         "два варианта несут одну и ту же библиотеку слоя: "
                         "%s" % versions)

    def test_the_installed_crow_header_is_the_pinned_one(self):
        pinned = re.search(r'crow_sha256="([0-9a-f]{64})"',
                           _read(os.path.join(IMAGES, "install-layer.sh")))
        self.assertIsNotNone(pinned, "в сценарии слоя нет закреплённой суммы")
        self.assertEqual(
            self.rows["crow"]["layer_library"]["version"],
            "sha256:" + pinned.group(1),
            "в образе лежит не тот заголовок Crow, который закреплён")

    def test_no_image_carries_a_startup_knob(self):
        for name, row in self.rows.items():
            self.assertEqual(row["baked_env"], [],
                             "%s: ручка запуска запечена в окружение образа"
                             % name)
            carrier = " ".join(row["entrypoint"] or [])
            carrier += " " + " ".join(row["cmd"] or [])
            for knob in KNOBS:
                self.assertNotIn(
                    knob, carrier,
                    "%s: ручка запуска запечена в точку входа образа: %s"
                    % (name, carrier))

    def test_a_broken_setting_kills_the_service_before_the_port_opens(self):
        # Ось 1 меряет различие «сервер занят / сервер умер». Отказ,
        # доехавший до обработчика, она прочла бы как отсутствие живости.
        # Здесь снято, что порт не открывается вовсе.
        for name, row in self.rows.items():
            refusals = {item["missing"]: item
                        for item in row["startup_refusals"]}
            self.assertEqual(sorted(refusals), sorted(KNOBS),
                             "%s: проверено не по каждой ручке" % name)
            for knob, item in refusals.items():
                self.assertNotEqual(
                    item["exit_code"], 0,
                    "%s без %s завершился успешно" % (name, knob))
                self.assertFalse(
                    item["port_open"],
                    "%s без %s открыл порт" % (name, knob))
            self.assertTrue(
                row["healthy_port_open"],
                "%s не отвечает и с годной настройкой — проба на закрытый "
                "порт проходила бы всегда" % name)

    def test_the_entry_point_is_the_binary_that_was_built(self):
        # Ручку можно запечь мимо ENV и мимо ENTRYPOINT: положить на место
        # бинарника сценарий, который выставляет её и запускает настоящий
        # сервис. Снаружи образ выглядел бы нетронутым, а текст файлов —
        # симметричным. Здесь снят формат файла и его сумма.
        for name, row in self.rows.items():
            entry = row["entry_binary"]
            self.assertTrue(entry["is_elf"],
                            "%s: в точке входа не бинарник, а %s"
                            % (name, entry["magic"]))
            self.assertTrue(
                entry["same_as_build_output"],
                "%s: в точке входа лежит не то, что вышло из сборки" % name)

    def test_every_peak_follows_the_numbers_it_was_read_from(self):
        # Вердикт пересчитывается честно, но из ПРОИЗВОДНОЙ величины, если
        # рядом не лежат сырые числа. Здесь они лежат: прочитанная
        # занятость обязана следовать из показаний, которыми она получена,
        # ТЕМ ЖЕ правилом.
        for name, row in self.rows.items():
            for setting in row["occupancy"]["at"]:
                pairs = zip(setting["runs"], setting["volleys"])
                for peak, item in pairs:
                    recount = dockerlib.peak_of(item)
                    self.assertEqual(
                        peak, recount,
                        "%s (%d единиц): записана занятость %d, а из "
                        "показаний %s и %s следует %d"
                        % (name, setting["workers"], peak,
                           item["inflight_by_probe"],
                           item["healthz_calls"], recount))
                    self.assertEqual(
                        item["counter_peak"], recount,
                        "%s: counter_peak в залпе не следует из показаний"
                        % name)

    def test_the_readings_are_possible_numbers(self):
        # Показание больше числа посланных проб невозможно: в полёте не
        # может быть больше обработчиков, чем запросов ушло. Такое число
        # означало бы, что счётчик не убывает или считает не запросы.
        for name, row in self.rows.items():
            for setting in row["occupancy"]["at"]:
                for item in setting["volleys"]:
                    probes = item["probes"]
                    for value in item["inflight_by_probe"]:
                        self.assertTrue(
                            1 <= value <= probes,
                            "%s: ответ пробы читает занятость %d при %d "
                            "посланных пробах" % (name, value, probes))
                    for call in item["healthz_calls"]:
                        value = call["value"]
                        if value is None:
                            continue
                        self.assertTrue(
                            0 <= value <= probes,
                            "%s: маршрут живости читает занятость %d при %d "
                            "посланных пробах" % (name, value, probes))
                    self.assertEqual(
                        len(item["inflight_by_probe"]), probes,
                        "%s: ответов проб меньше, чем проб послано — залп "
                        "неполон" % name)

    def test_the_client_bound_is_never_exceeded(self):
        # ГРАНИЦА КЛИЕНТА — независимая от счётчика верхняя граница:
        # число проб, которые клиент ещё считал отправленными и не
        # завершёнными, снятое ДО обращения к живости. Прочитанная
        # занятость не имеет права её превысить.
        for name, row in self.rows.items():
            for setting in row["occupancy"]["at"]:
                for item in setting["volleys"]:
                    recomputed = all(
                        call["value"] is None
                        or call["value"] <= call["client_pending"]
                        for call in item["healthz_calls"])
                    self.assertEqual(
                        item["client_bound_holds"], recomputed,
                        "%s: client_bound_holds не следует из показаний"
                        % name)
                    self.assertTrue(
                        item["client_bound_holds"],
                        "%s (%d единиц): счётчик прочитался выше того, что "
                        "клиент считал ещё отправленным и не завершённым"
                        % (name, setting["workers"]))
            self.assertTrue(
                row["client_bound_holds"],
                "%s: сведённая граница клиента не держится" % name)

    def test_the_volleys_were_taken_with_the_constants_of_the_code(self):
        # Число, оставшееся в фикстуре само по себе, разъезжается с
        # обвязкой молча: замер говорил бы про одно, а мерили бы другое.
        for name, row in self.rows.items():
            occupancy = row["occupancy"]
            self.assertEqual(occupancy["wait_ms"], dockerlib.WAIT_MS)
            self.assertEqual(occupancy["probes"], dockerlib.PROBE_COUNT)
            self.assertEqual(occupancy["endpoint"], "block")
            self.assertEqual(
                [setting["workers"] for setting in occupancy["at"]],
                list(dockerlib.WORKER_SETTINGS),
                "%s: замер снят не при тех настройках, что в коде" % name)
            for setting in occupancy["at"]:
                self.assertEqual(len(setting["runs"]),
                                 occupancy["runs_per_setting"])
                for item in setting["volleys"]:
                    self.assertEqual(item["wait_ms"], dockerlib.WAIT_MS)
                    self.assertEqual(item["probes"], dockerlib.PROBE_COUNT)
                    self.assertEqual(item["workers"], setting["workers"])
                    self.assertFalse(
                        item["sequential"],
                        "%s: залп занятости снят непересекающимися пробами "
                        "— так счётчик читается единицей у кого угодно"
                        % name)

    def test_stalls_reconcile_with_the_attempts_behind_them(self):
        # Число заминок лежало в фикстуре само по себе, и его обнуление не
        # противоречило ничему. Теперь рядом лежат попытки ПО ПРОГОНАМ, и
        # счётчик обязан из них следовать.
        for name, row in self.rows.items():
            for setting in row["occupancy"]["at"]:
                attempts = setting["attempts_used"]
                self.assertEqual(
                    len(attempts), len(setting["runs"]),
                    "%s: попыток записано не по числу прогонов" % name)
                self.assertTrue(all(value >= 1 for value in attempts))
                self.assertEqual(
                    setting["stalls"], sum(value - 1 for value in attempts),
                    "%s (%d единиц): заминки не следуют из попыток"
                    % (name, setting["workers"]))

    def test_the_published_number_is_the_worst_run_not_the_best(self):
        # Публикуемое число обязано выдерживать каждый прогон. Наибольшее
        # было бы отчётом об удаче: у слоя, чьё число плавает, оно росло бы
        # с числом попыток.
        for name, row in self.rows.items():
            for setting in row["occupancy"]["at"]:
                self.assertEqual(
                    setting["served_concurrently"],
                    dockerlib.published_concurrency(setting["runs"]),
                    "%s (%d единиц): опубликовано не худшее наблюдение"
                    % (name, setting["workers"]))

    def test_the_negative_control_reads_one_or_the_axis_is_void(self):
        # ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ: постановка, где счётчик ОБЯЗАН читаться
        # единицей. Те же пробы, то же число единиц, разница одна — они не
        # перекрываются. Счётчик, докладывающий настройку, число проб или
        # невозвращённый остаток, здесь прочитается не единицей.
        for name, row in self.rows.items():
            control = row["negative_control"]
            self.assertEqual(control["workers"], dockerlib.HIGH_WORKERS,
                             "%s: отрицательный контроль снят не при том "
                             "числе единиц, при котором меряют" % name)
            self.assertEqual(control["probes"], dockerlib.PROBE_COUNT)
            self.assertTrue(control["volleys"],
                            "%s: отрицательный контроль не снимался" % name)
            for item in control["volleys"]:
                self.assertTrue(
                    item["sequential"],
                    "%s: пробы контроля ушли залпом — контроль перестал "
                    "быть контролем" % name)
            peaks = [dockerlib.peak_of(item) for item in control["volleys"]]
            self.assertEqual(
                control["runs"], peaks,
                "%s: числа контроля не следуют из его показаний" % name)
            self.assertEqual(control["reads_one"],
                             all(value == 1 for value in peaks))

    def test_the_counter_reads_zero_before_every_volley(self):
        # Счётчик, не убывающий на выходе, растёт сам по себе, и занятость
        # читалась бы всё больше. После разогрева в полёте не должно быть
        # никого, и это снимается перед КАЖДЫМ залпом.
        for name, row in self.rows.items():
            idle = [item["idle_before"]
                    for setting in row["occupancy"]["at"]
                    for item in setting["volleys"]]
            self.assertEqual(
                row["idle_reads_zero"], all(value == 0 for value in idle),
                "%s: запись о нулевой занятости до залпа не следует из "
                "самих залпов: %s" % (name, idle))

    def test_the_decision_to_publish_follows_from_the_controls(self):
        # Печатать строку оси можно только при ВСЕХ контролях. Иначе ось
        # недействительна — и это законный исход, записанный, а не
        # спрятанный.
        for name, row in self.rows.items():
            expected = bool(row["knob_evidence"]["follows_workers"]
                            and row["negative_control"]["reads_one"]
                            and row["idle_reads_zero"]
                            and row["client_bound_holds"])
            self.assertEqual(
                row["axis_1_publishable"], expected,
                "%s: решение о печати строки оси не следует из контролей"
                % name)

    def test_the_healthz_timeout_is_pinned(self):
        # Круг 5 нашёл дыру: окно ожидания ответа живости подняли с 2 до
        # 60 секунд, и ни одна проверка не покраснела. Раздутое окно
        # маскирует то самое, что этот замер обязан увидеть, — очередь за
        # занятыми единицами превращается в тишину, которую опрос молча
        # переживает.
        self.assertEqual(dockerlib.HEALTHZ_TIMEOUT_S, 2.0)

    def test_the_liveness_follows_from_the_volleys_it_summarises(self):
        # Ось 1 спрашивает ровно то, что просила спека: пока залп в
        # полёте, отвечает ли маршрут живости. Ответ «не отвечает» — это
        # наблюдение (слой встал целиком), а не поломка, и записывается
        # оно как есть.
        for name, row in self.rows.items():
            high = [setting for setting in row["occupancy"]["at"]
                    if setting["workers"] == dockerlib.HIGH_WORKERS][0]
            self.assertEqual(
                row["liveness"], dockerlib.liveness_of(high["volleys"]),
                "%s: сводка живости не следует из залпов" % name)
            # ПРИВЯЗКА К НАСТРОЙКЕ. `liveness_of` сама называет, при какой
            # настройке она снята (`workers`), и эта настройка обязана
            # быть верхней: живость, снятая при меньшем числе единиц,
            # ничего не говорит о занятости на верхней настройке, которую
            # печатает таблица.
            self.assertEqual(
                row["liveness"]["workers"], dockerlib.HIGH_WORKERS,
                "%s: живость снята не при той настройке, о которой "
                "печатается строка оси" % name)
            self.assertGreater(
                row["liveness"]["asks"], 0,
                "%s: маршрут живости не спрашивали ни разу — «занят или "
                "мёртв» не выяснялось вовсе" % name)
            if row["liveness"]["answers"] > 0:
                # Ответ, полученный на ПУСТОМ сервисе, ничего не говорит о
                # занятости: проверка обязана требовать, чтобы в полёте
                # кто-то был, иначе она зелена на пустом месте.
                self.assertGreater(
                    row["liveness"]["asked_while_busy"], 0,
                    "%s: живость отвечала, но ни разу не заставала в "
                    "полёте никого — залп до неё не доехал" % name)
            # ЗДЕСЬ НЕ ПЕЧАТАЕТСЯ ВЫВОД «ОТВЕЧАЛА». Единственное, что
            # проверяется прямым текстом, — что сырая задержка ЛЕЖИТ в
            # фикстуре как число, а не подменена булевым «успел / не
            # успел».
            self.assertIsInstance(row["liveness"]["max_delay_ms"], int)
            self.assertGreaterEqual(row["liveness"]["max_delay_ms"], 0)

    def test_the_knob_leaves_evidence_that_it_is_connected(self):
        # «В полёте не больше заданного» выполняется и при полностью
        # отключённой ручке: у слоя с дефолтом в одну единицу отключённая
        # ручка даёт ровно то же число, что и настроенная в единицу.
        # Подключённость доказывает РАЗНИЦА между настройками.
        for name, row in self.rows.items():
            settings = {item["workers"]: item
                        for item in row["occupancy"]["at"]}
            low = settings[dockerlib.LOW_WORKERS]
            high = settings[dockerlib.HIGH_WORKERS]
            self.assertEqual(
                low["runs"], [1] * len(low["runs"]),
                "%s: при одной рабочей единице в полёте оказалась не одна "
                "проба: %s" % (name, low["runs"]))
            evidence = row["knob_evidence"]
            self.assertTrue(
                evidence["follows_workers"],
                "%s: в фикстуре нет свидетельства, что ручка вообще "
                "подключена — при большем числе единиц в полёте разом не "
                "больше, чем при одной" % name)
            self.assertEqual(
                evidence,
                dockerlib.knob_evidence(low["runs"], high["runs"]),
                "%s: свидетельство в фикстуре не следует из её же залпов"
                % name)

    def test_measured_versions_look_like_versions(self):
        # Замер в фикстуре — всё ещё строка, и «g++ 0.0» проходит проверку
        # «есть цифра». Форма величины проверяется отдельно от её
        # согласия: подделку ловит сверка со свежим замером, а бессмыслицу
        # — этот образец.
        shapes = {
            "cxx": r"^g\+\+ \(.+\) \d+\.\d+\.\d+$",
            "cmake": r"^cmake version \d+\.\d+\.\d+$",
            "libpqxx": r'PQXX_VERSION "\d+\.\d+\.\d+"',
        }
        for name, row in fixture_rows().items():
            for field, shape in shapes.items():
                self.assertRegex(row["tools"][field], shape,
                                 "%s: %s не похож на версию" % (name, field))

    def test_the_worker_knob_moves_what_the_service_holds(self):
        # Ручка, не влияющая на число обработчиков в полёте, — это
        # настройка, которой нет: оси мерили бы дефолт библиотеки, а
        # фикстура сообщала бы заданное число. Арифметика в точке входа
        # (`workers + 1`) не видна тексту и видна здесь.
        #
        # ПОЧЕМУ ДВЕ ВЕТКИ. Круг 5: перезамер на тех же бинарниках дал у
        # drogon другое число, чем лежит в `declared`. Точное равенство —
        # верное требование для СТАБИЛЬНОЙ ёмкости и заведомо ложное для
        # НЕСТАБИЛЬНОЙ: у слоя, чьё число плавает, любое одно число,
        # записанное как «объявленное», обязано быть отмечено как
        # `existential`, а не как истина, которую обязан воспроизвести
        # любой прогон. Здесь это связано с `capacity_stable`, а не
        # держится на честном слове поля `kind`: `kind` обязан совпасть с
        # тем, что фикстура САМА говорит про устойчивость.
        for name, row in self.rows.items():
            declared = dockerlib.variants()[name]["observed"]
            measured = row["occupancy"]
            self.assertEqual(measured["workers"], declared["workers"])
            self.assertEqual(measured["probes"], declared["probes"])
            if declared["kind"] == "measured":
                self.assertTrue(
                    row["capacity_stable"],
                    "%s: объявлено kind=measured, а ёмкость в фикстуре "
                    "нестабильна — measured обещает воспроизводимость, "
                    "которой нет" % name)
                self.assertEqual(
                    measured["served_concurrently"],
                    declared["served_concurrently"],
                    "%s: объявлено %d разом в полёте, замерено %d"
                    % (name, declared["served_concurrently"],
                       measured["served_concurrently"]))
            else:
                self.assertEqual(declared["kind"], "existential")
                self.assertFalse(
                    row["capacity_stable"],
                    "%s: объявлено kind=existential, а ёмкость в фикстуре "
                    "устойчива — existential здесь декоративен" % name)
                self.assertLessEqual(declared["served_concurrently"],
                                     declared["probes"])
                # Объявленное число обязано быть тем, что этот прогон
                # ДЕЙСТВИТЕЛЬНО когда-то наблюдал, а не выдумкой: оно
                # обязано входить в диапазон [минимум, максимум]
                # наблюдённых значений при верхней настройке.
                self.assertTrue(
                    min(measured["runs"]) <= declared["served_concurrently"]
                    <= max(measured["runs"]),
                    "%s: объявленное значение %d вне диапазона "
                    "наблюдённых %s" % (name, declared["served_concurrently"],
                                        measured["runs"]))


class LiveMeasurement(unittest.TestCase):
    """Тот же замер, но сейчас и на живых образах.

    Пропуск здесь — не зелёный: без докера проверка объявляет себя
    пропущенной. Запускать: STAND_WITH_DOCKER=1.
    """

    def setUp(self):
        if os.environ.get("STAND_WITH_DOCKER") != "1":
            self.skipTest("замер требует докера: STAND_WITH_DOCKER=1")
        if shutil.which("docker") is None:
            self.skipTest("docker не найден")

    def test_a_fresh_measurement_agrees_with_the_fixture(self):
        fresh = {}
        for name in sorted(dockerlib.variants()):
            dockerlib.build_variant(name)
            fresh[name] = dockerlib.measure_variant(name)
        committed = fixture_rows()
        for name, row in fresh.items():
            self.assertEqual(row["baked_env"], committed[name]["baked_env"])
            self.assertEqual(row["startup_refusals"],
                             committed[name]["startup_refusals"])
            # ОБА КОНТРОЛЯ ОБЯЗАНЫ ДЕРЖАТЬСЯ И СЕЙЧАС. Счётчик — код
            # внутри предмета, и его слепота не лечится тем, что однажды
            # он прочитал верно.
            self.assertTrue(
                row["negative_control"]["reads_one"],
                "%s: свежий отрицательный контроль прочитал не единицу — "
                "счётчик докладывает не занятость" % name)
            self.assertTrue(row["idle_reads_zero"],
                            "%s: свежий замер застал занятость до залпа"
                            % name)
            self.assertTrue(
                row["client_bound_holds"],
                "%s: свежий замер прочитал занятость выше того, что "
                "клиент считал ещё отправленным" % name)
            self.assertEqual(
                row["knob_evidence"]["one_unit_reads_one"], True,
                "%s: при одной единице свежий замер прочитал больше одного"
                % name)
            declared = dockerlib.variants()[name]["observed"]
            measured = row["occupancy"]["served_concurrently"]
            if declared["kind"] == "measured":
                self.assertEqual(
                    measured, declared["served_concurrently"],
                    "%s: свежий замер ручки разошёлся с объявленным" % name)
            else:
                # Наблюдение экзистенциальное: «такой залп был». Требовать
                # его повторения значило бы требовать воспроизводимости,
                # которой у этого слоя как раз и НЕТ, — проверка краснела
                # бы от того же свойства, которое записана удержать.
                self.assertLessEqual(measured, row["occupancy"]["probes"])
                self.assertLess(
                    min(committed[name]["occupancy"]["runs"]),
                    committed[name]["occupancy"]["probes"],
                    "%s: объявлено экзистенциальное наблюдение, а в "
                    "фикстуре нет ни одного залпа, где в полёте было "
                    "меньше посланного" % name)
        distinct = {json.dumps(row["tools"], sort_keys=True,
                               ensure_ascii=False) for row in fresh.values()}
        self.assertEqual(len(distinct), 1,
                         "в свежем замере версии инструментов разошлись")
        # Сверка с ЗАКОММИЧЕННЫМ, а не только согласия свежих строк между
        # собой: подделанные версии («g++ 0.0» во всех трёх) согласуются
        # друг с другом прекрасно.
        for name, row in fresh.items():
            self.assertEqual(
                row["tools"], committed[name]["tools"],
                "%s: версии инструментов разошлись с фикстурой. Если это "
                "обновление Debian — перезамерьте (scripts/run-variants.py) "
                "и закоммитьте фикстуру; если нет — в фикстуре не то, что "
                "в образе" % name)
            self.assertEqual(row["entry_binary"]["is_elf"], True)
            self.assertEqual(
                row["entry_binary"]["same_as_build_output"], True)



class DatabaseWaitFixture(unittest.TestCase):
    """Занимает ли ожидание В БАЗЕ рабочую единицу — по замеру.

    Главное утверждение волны («корутины не спасают, если драйвер
    блокирующий») держалось честным словом: сценарий пробы ничего не
    писал, и ни одна проверка его не касалась.

    Здесь же сводятся ДВА ИСТОЧНИКА: счётчик внутри сервиса и счёт
    активных запросов в самой базе. Счётчик слеп заодно с предметом;
    второй источник от предмета не зависит.
    """

    def setUp(self):
        if not os.path.exists(DB_WAIT_FIXTURE):
            self.fail("нет замера ожидания в базе: %s" % DB_WAIT_FIXTURE)
        self.rows = {row["variant"]: row
                     for row in outcome.read_measurements(DB_WAIT_FIXTURE)}

    def volleys(self):
        for name, row in sorted(self.rows.items()):
            for setting in row["at"]:
                for item in setting["volleys"]:
                    yield name, setting, item

    def test_every_variant_was_probed(self):
        self.assertEqual(sorted(self.rows), sorted(dockerlib.variants()))

    def test_waiting_in_the_database_occupies_a_unit(self):
        for name, row in self.rows.items():
            at_one = [item for item in row["at"] if item["workers"] == 1]
            self.assertEqual(len(at_one), 1,
                             "%s: нет залпов при одной единице" % name)
            self.assertTrue(
                all(value < row["probes"] for value in at_one[0]["runs"]),
                "%s: при одной рабочей единице в полёте оказались обе "
                "пробы — ожидание в базе единицу не занимает: %s"
                % (name, at_one[0]["runs"]))
            self.assertTrue(row["occupies_unit"])

    def test_the_control_is_recorded_and_not_generalised(self):
        # Контроль при трёх единицах — единственное, что отличает «занята
        # единица» от «занят весь сервис». Фикстура обязана говорить это
        # про КАЖДЫЙ вариант отдельно, а не переносить вывод с одного на
        # три.
        for name, row in self.rows.items():
            control = [item for item in row["at"] if item["workers"] > 1]
            self.assertEqual(len(control), 1,
                             "%s: нет контрольных залпов" % name)
            self.assertEqual(
                row["control_overlaps_always"],
                all(value == row["probes"] for value in control[0]["runs"]),
                "%s: вывод про контроль не следует из его залпов" % name)
            self.assertEqual(
                row["control_overlaps_ever"],
                any(value == row["probes"] for value in control[0]["runs"]))

    def test_every_peak_follows_its_readings(self):
        for name, setting, item in self.volleys():
            index = setting["volleys"].index(item)
            self.assertEqual(
                setting["runs"][index], dockerlib.peak_of(item),
                "%s (%d единиц): записанная занятость не следует из "
                "показаний %s и %s"
                % (name, setting["workers"], item["inflight_by_probe"],
                   item["healthz_calls"]))

    def test_the_two_sources_are_recorded_and_reconciled(self):
        # ТРИ ИСХОДА, А НЕ ДВА. «Сошлись», «разошлись» (оба что-то
        # увидели, числа разные) и «источник не поймал момент» (не увидел
        # ничего, хотя счётчик утверждает, что внутри было больше одного)
        # — путать второе с третьим значило бы прятать одну находку под
        # видом статистического шума другой.
        for name, setting, item in self.volleys():
            self.assertTrue(
                item["db_samples"],
                "%s: второй источник не снимался вовсе — сводить нечего"
                % name)
            self.assertEqual(item["db_peak"], max(item["db_samples"]),
                             "%s: число второго источника не следует из "
                             "его же замеров" % name)
            observed = bool(item["db_samples"]) and max(
                item["db_samples"]) >= 1
            self.assertEqual(
                item["db_observed_activity"], observed,
                "%s: db_observed_activity не следует из db_samples" % name)
            if not observed and item["counter_peak"] >= 2:
                self.assertFalse(item["sources_agree"])
                self.assertTrue(
                    item["sources_missed"],
                    "%s: источник не увидел ничего при занятости %d, а "
                    "промах не отмечен" % (name, item["counter_peak"]))
            else:
                self.assertEqual(
                    item["sources_agree"],
                    item["db_peak"] == item["counter_peak"],
                    "%s: согласие источников объявлено, а не пересчитано"
                    % name)
                self.assertFalse(item["sources_missed"])
        for name, row in self.rows.items():
            items = [item for setting in row["at"]
                     for item in setting["volleys"]]
            disagreed = len([item for item in items
                             if item["sources_agree"] is False
                             and not item["sources_missed"]])
            missed = len([item for item in items if item["sources_missed"]])
            self.assertEqual(row["sources"]["volleys"], len(items))
            self.assertEqual(row["sources"]["disagreed"], disagreed)
            self.assertEqual(row["sources"]["missed"], missed)
            self.assertEqual(row["sources"]["agree_everywhere"],
                             disagreed == 0 and missed == 0 and all(
                                 item["sources_agree"] is not None
                                 for item in items))
            self.assertEqual(
                row["publishable"],
                bool(row["control_overlaps_always"]
                     and row["sources"]["agree_everywhere"]
                     and row["control_is_valid"]
                     and row["entry_binary_matches_variants_fixture"]),
                "%s: решение о печати не следует из контроля и источников"
                % name)

    def test_the_control_validity_distinguishes_zero_from_negative(self):
        # ЗАПАС НОЛЬ И ЗАПАС МЕНЬШЕ НУЛЯ — разные состояния. При нуле
        # контроль ещё способен перекрыться (обе пробы влезают ровно),
        # при отрицательном — уже нет физически: ёмкость варианта ниже
        # числа проб. Смешение в одно булево поле читало бы отрицательный
        # запас как «какой-то есть».
        for name, row in self.rows.items():
            self.assertEqual(row["control_is_valid"],
                             row["control_headroom"] >= 0,
                             "%s: control_is_valid не следует из знака "
                             "запаса" % name)
            if not row["control_is_valid"]:
                self.assertFalse(
                    row["publishable"],
                    "%s: печать разрешена при отрицательном запасе "
                    "контроля" % name)

    def test_the_entry_binary_links_the_two_fixtures(self):
        # СВЯЗЬ ФИКСТУР. db-wait.jsonl читает запас контроля из
        # fixtures/variants.jsonl; без сверки суммы точки входа две
        # фикстуры из разных сборок дали бы согласованный на вид, но
        # неверный запас.
        variants_by_name = fixture_rows()
        for name, row in self.rows.items():
            self.assertEqual(
                row["entry_binary_matches_variants_fixture"],
                row["entry_binary_sha256"]
                == variants_by_name[name]["entry_binary"]["sha256"],
                "%s: сверка сумм точки входа не пересчитана" % name)
            self.assertTrue(
                row["entry_binary_matches_variants_fixture"],
                "%s: db-wait.jsonl и variants.jsonl сняты с разных сборок"
                % name)

    def test_the_second_source_actually_saw_something(self):
        # Согласие источников на нулях — согласие ни о чём: база, в
        # которой никто не числится активным, «сходится» со счётчиком,
        # который тоже сломан в ноль. У проверки должно быть, чем упасть.
        seen = [item["db_peak"] for _, _, item in self.volleys()]
        self.assertTrue(
            any(value >= 1 for value in seen),
            "второй источник ни разу не увидел активного запроса: сводить "
            "было нечего, и согласие источников ничего не значит")
        self.assertTrue(
            any(value >= 2 for value in seen),
            "второй источник ни разу не увидел двух запросов разом: он не "
            "различает занятость и потому контролем счётчика не является")

    def test_the_settings_are_the_ones_the_code_uses(self):
        for name, row in self.rows.items():
            self.assertEqual(
                [item["workers"] for item in row["at"]],
                [1, dbwait.CONTROL_WORKERS],
                "%s: замер снят не при тех настройках, что в коде" % name)
            self.assertEqual(row["control_workers"], dbwait.CONTROL_WORKERS)
            self.assertEqual(
                row["sleep_ms"], dbwait.SLEEP_MS,
                "%s: ожидание в фикстуре разъехалось с константой кода"
                % name)
            self.assertEqual(row["endpoint"], "q",
                             "%s: контроль ушёл с ручки базы — это уже не "
                             "контроль того же предмета" % name)
            for setting in row["at"]:
                for item in setting["volleys"]:
                    self.assertEqual(item["endpoint"], "q")
                    self.assertEqual(item["wait_ms"], row["sleep_ms"])
                    self.assertFalse(item["sequential"])
            sizes = {len(item["runs"]) for item in row["at"]}
            self.assertEqual(len(sizes), 1,
                             "%s: залпов при двух настройках разное число"
                             % name)

    def test_stalls_and_runs_reconcile_with_the_attempts_behind_them(self):
        for name, row in self.rows.items():
            self.assertEqual(
                row["runs_per_setting"], dbwait.RUNS,
                "%s: число залпов в фикстуре разъехалось с константой"
                % name)
            for setting in row["at"]:
                self.assertEqual(
                    len(setting["runs"]), row["runs_per_setting"],
                    "%s (%d единиц): залпов не столько, сколько заявлено"
                    % (name, setting["workers"]))
                attempts = setting["attempts_used"]
                self.assertEqual(len(attempts), len(setting["runs"]))
                self.assertTrue(all(value >= 1 for value in attempts))
                self.assertEqual(
                    setting["stalls"], sum(value - 1 for value in attempts),
                    "%s (%d единиц): отказы инструмента не следуют из "
                    "попыток" % (name, setting["workers"]))
                self.assertEqual(setting["probes"], row["probes"])

    def test_the_capacity_stability_is_recorded(self):
        for name, row in fixture_rows().items():
            high = [item for item in row["occupancy"]["at"]
                    if item["workers"] == dockerlib.HIGH_WORKERS][0]
            self.assertEqual(
                row["capacity_stable"],
                min(high["runs"]) == max(high["runs"]),
                "%s: устойчивость ёмкости не следует из залпов" % name)

    def test_the_control_headroom_is_recorded_and_follows_the_capacity(self):
        # У контроля запас может быть нулевым: слой при трёх единицах
        # держит ровно две пробы. Это записано числом, а не оставлено
        # читателю на догадку, и следует из замера ёмкости.
        capacity = {}
        for name, row in fixture_rows().items():
            for setting in row["occupancy"]["at"]:
                if setting["workers"] == dbwait.CONTROL_WORKERS:
                    capacity[name] = setting["served_concurrently"]
        for name, row in self.rows.items():
            self.assertEqual(
                row["control_headroom"], capacity[name] - row["probes"],
                "%s: запас контроля не следует из замера ёмкости" % name)
            # И сам код обязан считать то же самое: сверка поля с полем не
            # видит правки, которая объявит запас вместо расчёта.
            self.assertEqual(
                dbwait.control_headroom(name, row["probes"]),
                capacity[name] - row["probes"],
                "%s: код считает запас контроля не из замера ёмкости"
                % name)

    def test_the_probe_has_enough_probes_and_runs_to_mean_anything(self):
        self.assertGreaterEqual(
            dbwait.PROBES, 2,
            "одной пробе перекрываться не с чем: контроль перестаёт быть "
            "контролем")
        self.assertGreaterEqual(
            dbwait.RUNS, 5,
            "вывод про каждый залп на одном-двух залпах ничего не стоит: "
            "у утверждения должно быть, чем упасть")
        for row in self.rows.values():
            self.assertEqual(row["probes"], dbwait.PROBES)

    def test_at_least_one_variant_carries_the_control(self):
        # Если контроль не перекрылся НИ У ОДНОГО варианта, утверждение
        # «занята рабочая единица, а не сервис целиком» не подтверждено
        # вовсе, и фикстура обязана этим покраснеть.
        self.assertTrue(
            any(row["control_overlaps_always"] for row in self.rows.values()),
            "контроль не перекрылся ни у одного варианта: различить "
            "«занята единица» и «занят весь сервис» нечем")


class LiveDatabaseWait(unittest.TestCase):
    """Живой двойник замера ожидания в базе.

    У замера вариантов такой двойник был, а у этого — нет, и целая группа
    подмен жила в слепой зоне: контроль, ушедший с /q на /block,
    перестаёт быть контролем ТОГО ЖЕ предмета; вердикт, переписанный на
    единогласный, стирает честную оговорку бесследно; настройка в
    фикстуре разъезжается с константой кода.

    Пропуск здесь — не зелёный: без докера проверка объявляет себя
    пропущенной. Запускать: STAND_WITH_DOCKER=1.
    """

    def setUp(self):
        if os.environ.get("STAND_WITH_DOCKER") != "1":
            self.skipTest("замер требует докера: STAND_WITH_DOCKER=1")
        if shutil.which("docker") is None:
            self.skipTest("docker не найден")
        if not os.path.exists(DB_WAIT_FIXTURE):
            self.fail("нет замера ожидания в базе: %s" % DB_WAIT_FIXTURE)

    def test_a_fresh_probe_agrees_with_the_committed_verdict(self):
        committed = {row["variant"]: row
                     for row in outcome.read_measurements(DB_WAIT_FIXTURE)}
        # Образы собираются ЗАНОВО по той же причине, по какой это делает
        # run-db-wait.py: тег внутри демона несёт то, что собралось
        # последним, — в том числе из чужого рабочего дерева.
        for name in sorted(dockerlib.variants()):
            dockerlib.build_variant(name)
        dbwait.start_postgres()
        try:
            fresh = {name: dbwait.measure(name, runs=1)
                     for name in sorted(dockerlib.variants())}
        finally:
            dbwait.stop_postgres()
        for name, row in fresh.items():
            self.assertEqual(row["endpoint"], committed[name]["endpoint"])
            self.assertEqual([item["workers"] for item in row["at"]],
                             [item["workers"]
                              for item in committed[name]["at"]])
            self.assertTrue(
                row["occupies_unit"],
                "%s: свежая проба не показала занятости единицы, а фикстура "
                "её утверждает" % name)
            # Два источника обязаны сходиться и СЕЙЧАС: согласие,
            # случившееся однажды, свойством не является.
            self.assertEqual(
                row["sources"]["disagreed"], 0,
                "%s: в свежей пробе счётчик и база разошлись — это находка, "
                "и её надо назвать, а не усреднить" % name)
            # Сумма точки входа обязана совпасть и в свежей пробе:
            # committed[name] и fresh сняты в одной сессии докера (тот же
            # образ), и расхождение здесь означало бы, что образ
            # пересобрался между двумя замерами этого же прогона.
            self.assertTrue(
                row["entry_binary_matches_variants_fixture"],
                "%s: свежая проба сверилась с variants.jsonl от другого "
                "бинарника" % name)
            # Утверждение «контроль перекрывается ВСЕГДА» проверяется на
            # объединённой выборке: свежий залп, не перекрывшийся, делает
            # записанное «всегда» неверным.
            pooled = (committed[name]["at"][1]["runs"] + row["at"][1]["runs"])
            self.assertEqual(
                committed[name]["control_overlaps_always"],
                all(value == row["probes"] for value in pooled),
                "%s: записанный вывод про контроль не выдержал свежих "
                "залпов: %s" % (name, pooled))


if __name__ == "__main__":
    unittest.main()
