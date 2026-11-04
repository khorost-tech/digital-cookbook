"""Каждое число README обязано находиться в фикстурах.

README пишется последним, по памяти о числах, и это последнее место, где
в текст может попасть величина, которой никто не мерил. Выглядеть она
будет ровно так же, как измеренная.

Поэтому сверка механическая: все числа README собираются и сравниваются
с числами, которые встречаются в фикстурах либо считаются из них
здесь же, в этом файле, — по именам, а не подбором округлений.

ЧТО ИСКЛЮЧЕНО ИЗ СБОРА И ПОЧЕМУ.

Поля с сырыми данными — суммы sha256, ответы сервиса, командные строки,
хвосты вывода сборки, метки прогона, имена разделяемых библиотек — в
сбор НЕ входят. На стенде TLS проверка была почти пустой ровно потому,
что дампы байт в неё входили: в тысячах шестнадцатеричных цифр находится
почти любое число, и первая же диверсия прошла незамеченной. Здесь та же
опасность: `sha256_first` дал бы разрешение почти на что угодно, а
`dynamic_deps` — на «10» и «6» из имён `libfmt.so.10` и `libc.so.6`.

ЧТО ЭТА ПРОВЕРКА ЛОВИТ, А ЧТО НЕТ.

Ловит число, которого нет ни в одной фикстуре и которое не посчитано из
фикстур кодом. НЕ ловит верное число, поставленное не туда: сверка по
множеству не знает, о чём число говорит. За смысл отвечают четыре
разбора и чтение глазами.

Числа, которые НЕ являются результатами замера, перечислены явным
списком с объяснением у каждого. Список — признание, а не лазейка.
"""

import io
import json
import os
import re
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
README = os.environ.get("README_PATH", os.path.join(HERE, "README.md"))
FIXTURES = os.path.join(HERE, "fixtures")
IMAGES = os.path.join(HERE, "images")

# Версии вида 10.1.0 берутся целиком: разбитые на «10.1» и «0» они
# превратились бы в числа, которых никто не мерил.
NUMBER = re.compile(r"\d+(?:[.,]\d+)*")

# Разряды больших чисел в тексте разделены пробелом (1 022 128). Для
# сбора они склеиваются обратно, иначе «1 022 128» дало бы три числа.
GROUPED = re.compile(r"(?<=\d)[   ](?=\d{3}\b)")

# Поля с сырыми данными. Числа из них не считаются доказательством.
RAW_FIELDS = (
    "sha256_first", "sha256_second",
    "binary_sha256_in_builder", "binary_sha256_in_final",
    "healthz_response", "healthz_response_sha256",
    "docker_argv", "failure_tail", "detail", "resolve_detail",
    "resolve_body_compared", "resolve_excluded_fields",
    "treatment", "treatment_first", "treatment_second",
    "nonce_passed", "nonce_read_back", "nonce_first", "nonce_second",
    "binary_mtime", "binary_mtime_first", "binary_mtime_second",
    "build_dir_first", "build_dir_second",
    "dependency_from", "image_registry_method", "image_unpacked_method",
    "name_resolution", "external_libraries_declared",
    "elf_debug_sections", "dynamic_deps", "dockerfile", "elf_type",
    # Фикстура линковки: перехваченные потоки компоновщика и строки
    # предупреждения. В них есть смещения в секции (0x444), номера версий
    # и имена файлов — числа оттуда доказательством не являются, а
    # разрешили бы в README почти что угодно.
    "link_stdout", "link_stderr", "link_warning_lines",
    "link_warning_marker", "link_warning_normalization", "builder",
)

# Числа, которые не являются результатами замера. У каждого — откуда оно.
#
# ТРЁХ ЧИСЕЛ ЗДЕСЬ БОЛЬШЕ НЕТ, И ЭТО ГЛАВНЫЙ УРОК СПИСКА. Число проверок,
# число диверсий и число величин сводки были объявлены не-замерами с
# объяснением «считается запуском unittest / sabotage.py / длиной values».
# Объяснение прямо называло способ пересчёта, которым никто ни разу не
# воспользовался: в README стояли 235 и 106 при 250 и 124 на деле, причём
# 106 было подписано под командой, дающей 124. Список-признание оказался
# единственным местом README, где числа брались по памяти, — и оба
# неверными. Всё, что вычислимо, считается в derived(); список остаётся
# только для того, что не вычислимо в принципе.
NOT_MEASUREMENTS = {
    "0": "порядковые, ноль зависимостей, нулевая доля",
    "1": "порядковые, номера осей и прогонов",
    "2": "порядковые, номера осей, число прогонов",
    "3": "порядковые, номер оси",
    "4": "число осей стенда",
    "5": "порядковые",
    "6": "число плеч и вариантов",
    "7": "число плеч оси сборки",
    "8": "порядковые",
    "9": "порядковые",
    "11": "число холодных запусков на вариант — параметр прогона, не результат",
    "12": "число сборок оси воспроизводимости за один прогон — параметр прогона",
    "24": "число сборок оси воспроизводимости за оба прогона — параметр прогона",
    "3.8": "нижняя граница версии Python — объявленная зависимость, не замер",
    "2025": "год в строке версии rustc, приведённой из фикстуры целиком",
    "2026": "год снятия фикстур",
    "256": "часть имени алгоритма sha256, а не число",
    "1251": "часть имени кодовой страницы cp1251",
    "0755": "режим файла в слое образа, снятый пробой tar -tvf вне фикстур: "
            "назван как объяснение расхождения упакованных размеров, а не "
            "как результат оси",
    "0777": "то же, второй режим той же пробы",
}


def _numbers(value):
    if isinstance(value, bool):
        return set()
    if isinstance(value, (int, float)):
        return {str(value)}
    if isinstance(value, str):
        return set(NUMBER.findall(value))
    if isinstance(value, list):
        found = set()
        for item in value:
            found |= _numbers(item)
        return found
    return set()


def _rows(name):
    with io.open(os.path.join(FIXTURES, name), encoding="utf-8") as handle:
        text = handle.read()
    return [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]


def fixture_numbers():
    """Числа, записанные в фикстурах, кроме полей с сырыми данными."""
    found = set()
    for name in sorted(os.listdir(FIXTURES)):
        if not name.endswith(".jsonl"):
            continue
        rows = _rows(name)
        for row in rows:
            for key, value in row.items():
                if key in RAW_FIELDS:
                    continue
                found |= _numbers(value)
        found.add(str(len(rows)))
        found.add(str(len({r["variant"] for r in rows})))
    return found


# Дайджест образа из строки FROM. Вырезается ДО сбора чисел — см.
# image_tag_numbers().
FROM_DIGEST = re.compile(r"@sha256:[0-9a-f]{64}")


def image_tag_numbers():
    """Теги базовых образов из самих Dockerfile'ов.

    Это не замеры, а закрепление: README обязан назвать теги, а
    единственный допустимый источник для них — строки FROM. Взятые
    отсюда, они не могут разойтись с тем, что стенд собирает.

    ДАЙДЖЕСТ ИЗ СТРОКИ ВЫРЕЗАЕТСЯ, И ЭТО НЕ МЕЛОЧЬ. После закрепления
    образов по суммам в каждой строке FROM появилось по шестьдесят
    четыре шестнадцатеричные цифры. Собери их — и в разрешённом
    множестве оказались бы десятки случайных числовых обрывков, а сверка
    README превратилась бы в ту самую почти пустую проверку, о которой
    предупреждает заголовок этого файла: в дампе байт находится почти
    любое число. Тег из строки при этом остаётся и собирается как
    прежде.
    """
    found = set()
    for name in sorted(os.listdir(IMAGES)):
        if not name.startswith("Dockerfile."):
            continue
        with io.open(os.path.join(IMAGES, name), encoding="utf-8") as handle:
            for line in handle:
                if line.strip().upper().startswith("FROM "):
                    found |= set(NUMBER.findall(FROM_DIGEST.sub("", line)))
    return found


def check_count():
    """Число проверок в наборе — загрузкой набора, а не рукой."""
    import unittest as _unittest
    loader = _unittest.TestLoader()
    suite = loader.discover(os.path.join(HERE, "scripts"),
                            pattern="test_*.py", top_level_dir=HERE)
    if loader.errors:
        raise RuntimeError("набор проверок не загрузился: %r" % loader.errors)
    return suite.countTestCases()


def sabotage_counts():
    """Диверсий всего и диверсий без Docker — длиной списка в sabotage.py.

    Второе число нужно потому, что README называет обе команды: `run`
    без ключей пропускает группу machinery, а `run --all --with-docker`
    берёт всё. Раньше под первой командой стояло число, не отвечавшее ни
    одной из них.
    """
    import importlib.util
    path = os.path.join(HERE, "scripts", "sabotage.py")
    spec = importlib.util.spec_from_file_location("sabotage_module", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    total = len(module.SABOTAGES)
    without_docker = len([s for s in module.SABOTAGES if s[1] != "machinery"])
    return total, without_docker


def manifest_sizes():
    """Групп в сводке и скаляров под ними — из самих сводок."""
    manifests = os.path.join(FIXTURES, "manifests")
    groups = set()
    scalars = 0
    for name in sorted(os.listdir(manifests)):
        if not name.endswith(".json"):
            continue
        with io.open(os.path.join(manifests, name), encoding="utf-8") as handle:
            values = json.loads(handle.read())["values"]
        if groups and groups != set(values):
            raise RuntimeError("сводки описывают разные наборы величин")
        groups = set(values)
        scalars = 0
        for value in values.values():
            if isinstance(value, dict):
                scalars += sum(len(v) if isinstance(v, list) else 1
                               for v in value.values())
            elif isinstance(value, list):
                scalars += len(value)
            else:
                scalars += 1
    return len(groups), scalars


def tolerance_bytes():
    """Допуск на упакованный размер образа — импортом run-manifest.py.

    Раньше `25` в README проходило сверку случайно: число встречалось
    среди чисел фикстур, но ни в вычисляемых, ни в списке не-замеров его
    не было — сменить `TOLERANCE_BYTES` в скрипте, и README промолчал бы.
    """
    import importlib.util
    path = os.path.join(HERE, "scripts", "run-manifest.py")
    spec = importlib.util.spec_from_file_location("run_manifest_module", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.TOLERANCE_BYTES


def session_drift(arm, base="system"):
    """Разброс кратности холодной сборки внутри сессии и между сессиями.

    Возвращает проценты: наибольшее расхождение двух прогонов ОДНОЙ
    сессии и расхождение сессий между собой. Считается по свидетельству
    `fixtures/evidence/cold-seconds-by-session.json`, где сессии
    сравнимого окна замера лежат рядом.
    """
    path = os.path.join(FIXTURES, "evidence", "cold-seconds-by-session.json")
    with io.open(path, encoding="utf-8") as handle:
        evidence = json.loads(handle.read())
    sessions = [s for s in evidence["sessions"]
                if set(s["measurement_window"].values())
                == {"dependency_inside"}]
    within = 0.0
    middles = []
    for session in sessions:
        values = [runs[arm] / runs[base]
                  for _, runs in sorted(session["cold_seconds"].items())]
        within = max(within, 100.0 * (max(values) - min(values)) / min(values))
        middles.append(sum(values) / len(values))
    between = 100.0 * (max(middles) - min(middles)) / min(middles)
    return within, between


def derived():
    """Числа, посчитанные из фикстур ЗДЕСЬ — по именам, а не подбором.

    Каждое имя отвечает утверждению README. Округления перечислены
    явно: набор всех правдоподобных округлений разрешил бы почти любое
    число и обесценил бы сверку.
    """
    sib = {r["variant"]: r for r in _rows("siblings.jsonl")}
    pack = {}
    for row in _rows("package.jsonl"):
        pack.setdefault(row["variant"], {})[row["probe"]] = row

    out = {}

    def put(name, value, digits):
        out[name] = ("%%.%df" % digits % value).replace(".", ",")

    # Кратностей по времени сборки здесь НЕТ намеренно: пара прогонов не
    # оценивает их разброс, а сам разброс от сессии к сессии не постоянен
    # (сама пара то сходится до долей процента, то расходится на единицы
    # процентов). Пока кратности считались, README ломался после каждого
    # полного прогона — и это было верное поведение сверки, а не её
    # дефект.

    for field in ("binary_bytes", "image_unpacked_bytes"):
        for arm in ("cpp", "go"):
            ratio = sib[arm][field] / sib["rust"][field]
            put("%s_%s_over_rust_1" % (field, arm), ratio, 1)
            put("%s_%s_over_rust_3" % (field, arm), ratio, 3)

    # Упакованный размер образа между платформами расходится на единицы
    # байт (измерено: в слой попадает режим файлов контекста). Поэтому
    # он идёт в README округлённым до килобайта, а его кратности — с
    # одним знаком: третий знак у них платформозависим и в README
    # ронял бы проверку после прогона на другом хосте.
    for arm in ("cpp", "go"):
        put("image_registry_%s_over_rust_1" % arm,
            sib[arm]["image_registry_bytes"] / sib["rust"]["image_registry_bytes"],
            1)
    for arm in sorted(sib):
        out["image_registry_kib_%s" % arm] = str(
            int(round(sib[arm]["image_registry_bytes"] / 1024.0)))
    for name in sorted(pack):
        out["package_image_kib_%s" % name] = str(
            int(round(pack[name]["healthz"]["image_bytes"] / 1024.0)))

    fmt_bytes = sib["cpp"]["binary_bytes"] - sib["cpp-nofmt"]["binary_bytes"]
    out["fmt_bytes"] = str(fmt_bytes)
    put("fmt_share_percent", 100.0 * fmt_bytes / sib["cpp"]["binary_bytes"], 0)
    put("rust_over_cpp_nofmt_1",
        sib["rust"]["binary_bytes"] / sib["cpp-nofmt"]["binary_bytes"], 1)

    # Два края оси упаковки: вариант с самым большим образом и вариант с
    # самым маленьким. Кратности считаются по этой паре, а не по всем
    # шести, — именно она несёт утверждение о перевороте.
    biggest = max(pack, key=lambda v: pack[v]["healthz"]["image_bytes"])
    smallest = min(pack, key=lambda v: pack[v]["healthz"]["image_bytes"])
    # Кратность краёв считается по округлённым до килобайта числам: в
    # байтах она платформозависима в последнем знаке.
    put("image_spread_1",
        round(pack[biggest]["healthz"]["image_bytes"] / 1024.0)
        / round(pack[smallest]["healthz"]["image_bytes"] / 1024.0), 1)
    put("binary_of_image_extremes_1",
        pack[smallest]["healthz"]["binary_bytes"]
        / pack[biggest]["healthz"]["binary_bytes"], 1)

    # ЧИСЛА, КОТОРЫЕ РАНЬШЕ БРАЛИСЬ ПО ПАМЯТИ.
    #
    # Набор проверок и набор диверсий считаются здесь тем же способом,
    # каким их считает читатель: загрузкой набора и длиной списка
    # диверсий. Пока они были объявлены не-замерами, README называл 235
    # и 106 при 250 и 124 на деле.
    out["checks_total"] = str(check_count())
    total, without_docker = sabotage_counts()
    out["sabotages_total"] = str(total)
    out["sabotages_without_docker"] = str(without_docker)

    # «54 величины» — это число ИМЕНОВАННЫХ ГРУПП сводки, а под ними
    # лежат отдельные скаляры по плечам. Оба числа считаются из самих
    # сводок: приписанное руками, оно протухло бы молча при первом же
    # добавлении величины.
    groups, scalars = manifest_sizes()
    out["manifest_value_groups"] = str(groups)
    out["manifest_scalars"] = str(scalars)

    # Допуск на упакованный размер образа — тем же способом: чтением
    # TOLERANCE_BYTES из run-manifest.py, а не переписыванием числа сюда.
    out["tolerance_bytes"] = str(tolerance_bytes())

    # МЕЖСЕССИОННЫЙ РАЗБРОС КРАТНОСТИ ХОЛОДНОЙ СБОРКИ.
    #
    # Сама кратность в README не идёт — пара прогонов не оценивает её
    # разброс. А вот НАСКОЛЬКО расходятся сессии и пары внутри них, README
    # называть обязан: без этих чисел утверждение держалось бы на памяти
    # автора. Считаются они из свидетельства по сессиям, а не из фикстур:
    # в фикстурах лежит одна сессия — одна пара, по которой о разбросе
    # между сессиями сказать нечего.
    for arm in ("conan", "vcpkg"):
        within, between = session_drift(arm)
        put("cold_ratio_%s_within_percent" % arm, within, 1)
        put("cold_ratio_%s_between_percent" % arm, between, 1)

    out["package_variants"] = str(len(pack))
    out["refused_variants"] = str(
        sum(1 for v in pack.values() if v["resolve"]["outcome"] != "resolved"))
    return out


def readme_numbers():
    with io.open(README, encoding="utf-8") as handle:
        text = handle.read()
    # Числа внутри блоков кода — это команды, пути и теги образов, а не
    # утверждения о результатах.
    text = re.sub(r"```.*?```", " ", text, flags=re.S)
    text = GROUPED.sub("", text)
    return NUMBER.findall(text)


class ReadmeNumbers(unittest.TestCase):
    def test_readme_exists(self):
        self.assertTrue(os.path.exists(README), "README не написан")

    def test_every_number_is_backed_by_a_fixture(self):
        allowed = (fixture_numbers() | image_tag_numbers()
                   | set(derived().values()) | set(NOT_MEASUREMENTS))
        unknown = sorted({n for n in readme_numbers() if n not in allowed})
        self.assertEqual(
            unknown, [],
            "числа README не найдены ни в фикстурах, ни среди посчитанных "
            "из них, ни среди объявленных не-замерами: %r. Либо число "
            "измерено и обязано лежать в фикстуре, либо посчитано и обязано "
            "считаться в derived(), либо оно не результат и обязано быть "
            "названо в списке с объяснением." % unknown)
        # ПРИНАДЛЕЖНОСТЬ МНОЖЕСТВУ НЕ ЛОВИТ ПОДМЕНУ ВЕРНЫМ НА ВИД ЧИСЛОМ.
        #
        # «25» проходило проверку выше и без derived(): оно случайно
        # встречалось среди чисел фикстур, никак не связанных с допуском.
        # Смени TOLERANCE_BYTES в run-manifest.py — и «25» в README
        # осталось бы разрешённым тем же самым совпадением, а допуск
        # разошёлся бы с кодом молча. Здесь число рядом с «|Δ| ≤» читается
        # и сравнивается с константой напрямую, а не через множество.
        with io.open(README, encoding="utf-8") as handle:
            text = handle.read()
        match = re.search(r"\|Δ\|\s*≤\s*(\d+)\s*Б", text)
        self.assertIsNotNone(
            match, "в README не нашлось формулировки допуска «|Δ| ≤ N Б»")
        stated = int(match.group(1))
        self.assertEqual(
            stated, tolerance_bytes(),
            "README называет допуск %d Б, а TOLERANCE_BYTES в "
            "run-manifest.py — %d" % (stated, tolerance_bytes()))

    def test_the_exception_list_explains_itself(self):
        for number, why in NOT_MEASUREMENTS.items():
            self.assertTrue(why.strip(),
                            "%s внесён в список без объяснения" % number)

    def test_derived_numbers_are_named_not_guessed(self):
        """Посчитанные числа именованы, и имён немного.

        Набор из всех правдоподобных округлений разрешил бы почти любое
        число. Здесь их считанное количество, и каждое отвечает
        конкретному утверждению README.
        """
        values = derived()
        self.assertTrue(values, "derived() ничего не считает")
        self.assertLess(len(values), 50,
                        "посчитанных чисел стало слишком много: сверка "
                        "перестаёт что-либо запрещать")


class ReadmeSaysWhatCannotBeClaimed(unittest.TestCase):
    """Ограничения метода обязаны быть названы, а не подразумеваться."""

    # Третье число — сколько раз маркер обязан встретиться СЕЙЧАС.
    # Просто «marker in text» не поймало бы удаление ОДНОГО из нескольких
    # предложений, несущих один и тот же маркер: у «намеренно», «одной
    # машине» и «не мерит» несколько мест-носителей, и текст остался бы
    # зелёным, потеряв одно из них. Число — не круглое и не произвольное:
    # оно снято подсчётом по нынешнему README и снизится, если носителей
    # станет меньше.
    REQUIRED = [
        ("закреплены дайджестами",
         "образы закреплены дайджестами, а не подвижными тегами", 1),
        ("намеренно",
         "старый glibc оставлен намеренно", 2),
        ("границу версий стенд не ищет",
         "стенд не ищет, в какой версии glibc правило перестало работать", 1),
        ("пропускная способность",
         "пропускная способность не мерилась вовсе", 1),
        ("распространённост",
         "ничего не утверждается о распространённости", 1),
        ("одной машине",
         "область — одна машина, а не «вообще»", 3),
        ("распакованное",
         "у размера образа две шкалы, и обе названы", 1),
        ("не мерит",
         "прямо сказано, чего стенд не мерит", 2),
    ]

    def test_limits_section_names_the_limits(self):
        with io.open(README, encoding="utf-8") as handle:
            text = handle.read().lower()
        for marker, what, minimum in self.REQUIRED:
            # assertIn печатал бы весь README целиком.
            found = text.count(marker.lower())
            self.assertGreaterEqual(
                found, minimum,
                "в README %d вхождений маркера %r (нужно не меньше %d): %s"
                % (found, marker, minimum, what))


class ReadmeCountsAreCurrent(unittest.TestCase):
    """Число проверок и число диверсий в README обязаны совпадать с кодом.

    Они стоят ВНУТРИ блока кода, а сбор чисел README блоки кода вырезает —
    иначе в разрешённое множество попали бы пути, команды и теги образов.
    Из-за этого сверка «каждое число подтверждено фикстурой» их не видела
    вовсе: в README стояло 286 при 297 проверках на деле, и не покраснело
    ничто. Рядом при этом стоял абзац, обещающий, что оба числа считаются,
    а не помнятся, — и это была ровно та стухшая отметка, против которой
    заведён маркер прогона. Вот место, где обещание наконец исполняется.
    """

    def _number(self, pattern, text):
        match = re.search(pattern, text)
        self.assertIsNotNone(
            match, "в README не нашлось строки по образцу %r" % pattern)
        return int(match.group(1))

    def test_the_check_and_sabotage_counts_match_the_code(self):
        with io.open(README, encoding="utf-8") as handle:
            text = handle.read()
        total, without_docker = sabotage_counts()
        stated_checks = self._number(
            r"unittest discover[^\n#]*#\s*(\d+)\s+провер", text)
        self.assertEqual(
            stated_checks, check_count(),
            "README называет %d проверок, набор загружается в %d"
            % (stated_checks, check_count()))
        stated_plain = self._number(
            r"sabotage\.py run\s+#\s*(\d+)\s+диверси", text)
        self.assertEqual(
            stated_plain, without_docker,
            "README называет %d диверсий без Docker, в списке их %d"
            % (stated_plain, without_docker))
        stated_all = self._number(
            r"sabotage\.py run --all --with-docker\s+#\s*(\d+)\s+диверси",
            text)
        self.assertEqual(
            stated_all, total,
            "README называет %d диверсий всего, в списке их %d"
            % (stated_all, total))


if __name__ == "__main__":
    unittest.main()
