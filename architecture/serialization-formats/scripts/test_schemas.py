"""Три описания одной записи обязаны описывать одну запись.

Стенд сравнивает форматы. Если схемы разъедутся хотя бы на одно поле,
сравнение потеряет смысл, а таблица останется правдоподобной.
"""

import hashlib
import io
import json
import os
import re
import unittest

# Круг правок 3, "мелочь": ROOT был жёстко "schemas/" — относительный
# путь, который резолвится от ТЕКУЩЕГО каталога запуска, а не от
# расположения этого файла. Тест проходил, только если его запускали
# из корня стенда; из каталога scripts/ (или из любого другого CWD,
# как это сделает оркестратор Задачи 8) он падал на первой же попытке
# открыть файл. os.path.dirname(__file__) не зависит от того, откуда
# вызван скрипт.
ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "schemas") + os.sep


def _read_text(path):
    """Читает файл целиком и ЗАКРЫВАЕТ его.

    Без закрытия набор тестов сыпал ResourceWarning: файлов много, а
    сборщик мусора закрывает их когда придётся. Для эталонного стенда
    это лишний шум, который прячет настоящие предупреждения.
    """
    with io.open(path, encoding="utf-8") as handle:
        return handle.read()


def _load_json(path):
    """То же для JSON: разбор поверх закрытого дескриптора."""
    return json.loads(_read_text(path))


def _read_bytes(path):
    """Читает файл двоично и ЗАКРЫВАЕТ его — как и текстовые собратья."""
    with io.open(path, "rb") as handle:
        return handle.read()



def avro_fields(name):
    d = _load_json(ROOT + name)
    return [f["name"] for f in d["fields"]]


def proto_fields(name):
    text = _read_text(ROOT + name)
    return re.findall(r"^\s*(?:optional\s+)?\w+\s+(\w+)\s*=\s*\d+;",
                      text, re.M)


def json_fields(name):
    d = _load_json(ROOT + name)
    return list(d["properties"].keys())


class SchemasAgree(unittest.TestCase):
    def test_v1_same_fields_in_three_formats(self):
        self.assertEqual(avro_fields("user_v1.avsc"), ["id", "name", "email"])
        self.assertEqual(proto_fields("user_v1.proto"), ["id", "name", "email"])
        self.assertEqual(json_fields("user_v1.json"), ["id", "name", "email"])

    def test_records_match_v1(self):
        # records.json — не плоский массив (круг правок 1, находка C2):
        # клетка newer_writer требует, чтобы ПИСАТЕЛЬ v2 получил запись
        # ФОРМЫ v2, а форма v2 у семи изменений — семь разных наборов
        # полей. Структура: {"v1": {"records": [...]}, "v2": {change:
        # {"records": [...]}}} — v1 и v2 симметричны (круг правок 2,
        # "мелочь": раньше v1 был плоским массивом, а v2 — словарём по
        # изменению, и эта асимметрия заставляла бы Java и оркестратор
        # ветвиться там, где ветвиться не нужно).
        recs = _load_json(ROOT + "records.json")
        v1 = recs["v1"]["records"]
        self.assertEqual(len(v1), 5)
        for r in v1:
            self.assertEqual(sorted(r.keys()), ["email", "id", "name"])


CHANGES = ["add_default", "add_nodefault", "remove", "rename",
           "retype", "reuse_tag", "unknown_field",
           "alias_conflict", "retype_message"]

# Ожидаемый набор ключей записи v2-формы для каждого изменения — форма
# выбирается по схеме ПИСАТЕЛЯ (см. .avsc/.json файлы этой же версии),
# а не по направлению пробы.
V2_RECORD_FIELDS = {
    "add_default": ["age", "email", "id", "name"],
    "add_nodefault": ["age", "email", "id", "name"],
    "remove": ["id", "name"],
    "rename": ["contact", "id", "name"],
    "retype": ["email", "id", "name"],
    "reuse_tag": ["id", "login_count", "name"],
    "unknown_field": ["email", "id", "name", "nickname"],
    # alias_conflict: email ушёл под псевдонимом на поле name (§6.2
    # spec.md) — у писателя второй версии в этой нотации остаётся
    # только id и name.
    "alias_conflict": ["id", "name"],
    # retype_message: email сменил тип со строки на вложенную запись,
    # имя поля не изменилось.
    "retype_message": ["email", "id", "name"],
}


def _leaf_values(value):
    """Разворачивает значение клетки expected.json (простой исход ЛИБО
    структура by_lang/by_record, круг 6ter, spec.md §3.6/§15) в МНОЖЕСТВО
    всех исходов, которые оно может дать. Для простого исхода — синглтон;
    для структуры — объединение по всем языкам/записям. Используется,
    чтобы сверить журнал поправок (expected-corrections.json) с
    ТЕКУЩИМ значением клетки без предположения, что клетка всегда плоская
    строка."""
    if isinstance(value, str):
        return {value}
    if isinstance(value, dict):
        out = set()
        if "by_lang" in value:
            for sub in value["by_lang"].values():
                out |= _leaf_values(sub)
        elif "by_record" in value:
            for sub in value["by_record"].values():
                out |= _leaf_values(sub)
        return out
    return set()


class V2RecordsMatchWriterSchema(unittest.TestCase):
    """Задачи 6 и 8 опираются на records.json["v2"][change] как на
    единственный источник "какую запись писать writer-схемой этой
    версии" — если поля здесь разъедутся со схемой, клетка
    newer_writer будет измерять не то, что заявлено в статье."""

    def test_every_change_has_five_v2_records(self):
        recs = _load_json(ROOT + "records.json")
        v2 = recs["v2"]
        self.assertEqual(sorted(v2.keys()), sorted(CHANGES))
        for change in CHANGES:
            rows = v2[change]["records"]
            self.assertEqual(len(rows), 5, change)
            for row in rows:
                self.assertEqual(sorted(row.keys()), V2_RECORD_FIELDS[change], change)

    def test_v2_record_fields_match_writer_schema(self):
        for change in CHANGES:
            record_names = V2_RECORD_FIELDS[change]
            if change == "reuse_tag":
                # reuse_tag существует только на уровне номеров полей —
                # это protobuf-специфичное понятие: avro-схема этого
                # изменения буквально совпадает с v1 (expected.json
                # честно помечает avro "n/a" для этой строки), а
                # переиспользованный номер 3 виден только в .proto.
                # Сверяем запись с proto-схемой, а не с avro.
                schema_names = sorted(proto_fields(f"user_v2_{change}.proto"))
            else:
                schema_names = sorted(avro_fields(f"user_v2_{change}.avsc"))
            self.assertEqual(schema_names, record_names, change)

    def test_reuse_tag_marks_itself_protobuf_only(self):
        # Круг правок 2, "третья правка": вырожденность reuse_tag для
        # Avro/JSON Schema должна быть видна В ДАННЫХ, а не только в
        # голове того, кто читал expected.json и Go-тесты. Оркестратор,
        # взявший записи "как есть" и погнавший их по всем плечам, иначе
        # получил бы для Avro/JSON Schema бессмысленную клетку без
        # единого сигнала о том, что так и задумано.
        # reuse_tag вырожден для Avro/JSON Schema (переиспользование
        # номера поля есть только в Protobuf); alias_conflict — зеркально
        # наоборот, вырожден для Protobuf/JSON Schema (псевдонимы поля
        # есть только в Avro, §6.2/§6.4 spec.md). У каждого — свой,
        # ЕДИНСТВЕННЫЙ подходящий only_formats; у всех прочих изменений
        # only_formats стоять не должно вовсе.
        recs = _load_json(ROOT + "records.json")
        v2 = recs["v2"]
        ONLY_FORMATS_BY_CHANGE = {
            "reuse_tag": ["protobuf"],
            "alias_conflict": ["avro"],
        }
        for change in CHANGES:
            expected_only = ONLY_FORMATS_BY_CHANGE.get(change)
            if expected_only is not None:
                self.assertEqual(v2[change].get("only_formats"), expected_only, change)
            else:
                self.assertNotIn("only_formats", v2[change],
                                  f"{change}: only_formats должен стоять только у "
                                  f"{sorted(ONLY_FORMATS_BY_CHANGE)}")


# Файлы, которые читает ВТОРАЯ реализация: спека, данные стенда и
# таблица. Ни в одном из них не должно быть имён сущностей первой
# реализации — иначе описание снова опирается на один язык, а не на
# понятия стенда и форматов.
LANGUAGE_NEUTRAL_FILES = ["spec.md", "records.json", "expected.json",
                          "expected-corrections.json", "manifest.json"]

FIRST_IMPLEMENTATION_MARKS = [
    "map[string]", "reflect.", "interface{", "func ",
    "codec.", "probe.", "stand.", "go run", "go/cmd", "Go-",
    "ErrDegenerateSchema", "ExpectedRecord", "RoundTripper",
    "jsonSchemaCodec", "avroCodec", "protobufCodec",
    "NewSchemaCompatibility",
]


class LanguageNeutral(unittest.TestCase):
    """Круг правок 5: проверка сторожила только spec.md, а имя Go-сущности
    успело просочиться в примечание records.json. Читает вторая
    реализация не только спеку."""

    def test_stand_files_do_not_name_first_implementation(self):
        for name in LANGUAGE_NEUTRAL_FILES:
            text = _read_text(ROOT + name)
            for mark in FIRST_IMPLEMENTATION_MARKS:
                self.assertNotIn(mark, text,
                                 "%s ссылается на сущность первой реализации %r"
                                 % (name, mark))


class ManifestIsRootOfTrust(unittest.TestCase):
    """Круг правок 5. Имя клетки выводилось из ИМЕНИ ФАЙЛА, и копия штатной
    схемы под чужим именем переименовывала клетку, не оставляя привычных
    следов подделки. Манифест связывает имя с содержимым: подделка стала
    правкой манифеста, то есть обычным дифом, который видно в ревью.

    Вторая польза может оказаться важнее первой: манифест ловит
    неумышленное — испорченную копию, недописанный файл, перепутанную
    версию."""

    def manifest(self):
        return _load_json(ROOT + "manifest.json")

    def test_manifest_covers_the_directory_exactly(self):
        listed = set(self.manifest()["files"].keys())
        present = {n for n in os.listdir(ROOT)
                   if os.path.isfile(os.path.join(ROOT, n)) and n != "manifest.json"}
        self.assertEqual(listed, present,
                         "манифест обязан перечислять каталог стенда целиком: "
                         "посторонний файл рядом со схемами виден сразу")

    def test_every_digest_matches_the_file(self):
        m = self.manifest()
        self.assertEqual(m["algorithm"], "sha256")
        for name, entry in m["files"].items():
            raw = _read_bytes(ROOT + name)
            if entry["content"] == "text":
                raw = raw.replace(b"\r\n", b"\n")
            self.assertEqual(hashlib.sha256(raw).hexdigest(), entry["digest"],
                             "%s: дайджест не совпадает — пересобери манифест "
                             "(python scripts/build_manifest.py) вместе с правкой" % name)

    def test_schema_entries_agree_with_their_names(self):
        # Запись манифеста задаёт имя клетки. Если она разойдётся с именем
        # файла, читатель дифа увидит одно, а проба посчитает другое.
        notations = {".avsc": "avro", ".desc": "protobuf", ".json": "json-schema"}
        for name, entry in self.manifest()["files"].items():
            if entry["role"] != "schema":
                continue
            m = re.match(r"^user_v(\d+)(?:_(.+))?\.(\w+)$", name)
            self.assertIsNotNone(m, name)
            self.assertEqual(entry["version"], int(m.group(1)), name)
            self.assertEqual(entry.get("change", ""), m.group(2) or "", name)
            self.assertEqual(entry["notation"], notations["." + m.group(3)], name)

    def test_every_change_and_version_has_three_schemas_in_manifest(self):
        schemas = {n: e for n, e in self.manifest()["files"].items()
                   if e["role"] == "schema"}
        self.assertEqual(len(schemas), 30, "10 версий (база + 9 изменений) x 3 нотации")
        for change in CHANGES:
            for notation in ("avro", "protobuf", "json-schema"):
                found = [n for n, e in schemas.items()
                         if e["version"] == 2 and e.get("change") == change
                         and e["notation"] == notation]
                self.assertEqual(len(found), 1, (change, notation))


class SpecPresent(unittest.TestCase):
    """Круг правок 4: правила стенда (какие бывают исходы, чем
    опознаётся «то же самое» поле, как считается ожидание, откуда
    берутся записи) жили только в Go-исходнике и были выражены через
    Go-сущности. Java обязана повторить их в точности — значит, они
    обязаны существовать отдельно от Go."""

    def test_spec_exists_and_is_language_neutral(self):
        path = ROOT + "spec.md"
        self.assertTrue(os.path.exists(path), path)
        text = _read_text(path)
        self.assertGreater(len(text), 4000, "спека подозрительно короткая")
        # Без контракта вызова вторая реализация невозможна: оркестратор
        # обязан звать обе одинаково.
        for required in ("--format", "--change", "--direction", "--op",
                         "manifest.json", "Равенство", "Что делает каждое плечо"):
            self.assertIn(required, text, "спека не описывает %r" % required)
        # Круг правок 6: путей у пробы нет вовсе. Если они снова
        # появятся в контракте вызова, вместе с ними вернутся подставной
        # каталог и плечо, не совпадающее с нотацией схем.
        for forbidden in ("--writer-schema", "--reader-schema", "--record", "--stand"):
            self.assertNotIn(forbidden + "=", text,
                             "спека снова описывает аргумент %r" % forbidden)
        # Проверка грубая, но по делу: спека описывает стенд понятиями
        # стенда и форматов. Появление Go-сущностей означает, что
        # описание снова опирается на одну реализацию.
        for go_word in ("map[string]any", "reflect.DeepEqual", "interface{",
                        "ErrDegenerateSchema", "ExpectedRecord", "func "):
            self.assertNotIn(go_word, text,
                             "спека ссылается на Go-сущность %r" % go_word)


class ChangeSetComplete(unittest.TestCase):
    def test_every_change_has_three_schemas(self):
        import os
        for c in CHANGES:
            for ext in ("avsc", "proto", "json"):
                p = f"{ROOT}user_v2_{c}.{ext}"
                self.assertTrue(os.path.exists(p), p)

    def test_expected_contains_nothing_but_cells(self):
        # Круг правок 4: журнал поправок лежал ключом "corrections"
        # ВНУТРИ expected.json, вперемешку с изменениями схемы. Наивный
        # обход по ключам (а именно так и поступит оркестратор, и Java)
        # полез бы искать схему user_v2_corrections.* и не нашёл. Журнал
        # переехал в expected-corrections.json, а здесь остались только
        # клетки — и этот тест сторожит, чтобы туда снова ничего не
        # подселили.
        exp = _load_json(ROOT + "expected.json")
        self.assertEqual(sorted(exp.keys()), sorted(CHANGES))

    def _assert_valid_cell_value(self, value, where):
        """Значение клетки — либо простой исход, либо структура
        by_lang/by_record (круг 6ter, spec.md §3.6/§15), развёрнутая
        рекурсивно до простых исходов; в обоих случаях обязан быть
        "reason" на структуре. Не путать с analyze-evolution.py
        resolve_expected — тот РАЗВОРАЧИВАЕТ значение для конкретной
        (lang, record_index), а эта проверка просто убеждается, что ВСЕ
        значения где-то на дне структуры — легальные исходы."""
        if isinstance(value, str):
            self.assertIn(value, ("ok", "refused", "wrong", "n/a"), where)
            return
        self.assertIsInstance(value, dict, where)
        self.assertIn("reason", value, f"{where}: структура без reason")
        if "by_lang" in value:
            for lang, sub in value["by_lang"].items():
                self._assert_valid_cell_value(sub, f"{where}.by_lang.{lang}")
        elif "by_record" in value:
            for idx, sub in value["by_record"].items():
                self._assert_valid_cell_value(sub, f"{where}.by_record.{idx}")
        else:
            self.fail(f"{where}: структура без by_lang и без by_record: {value!r}")

    def test_expected_covers_every_cell(self):
        exp = _load_json(ROOT + "expected.json")
        cells = exp
        self.assertEqual(sorted(cells.keys()), sorted(CHANGES))
        for c, per_format in cells.items():
            self.assertEqual(sorted(per_format.keys()),
                             ["avro", "json-schema", "protobuf"])
            for fmt, dirs in per_format.items():
                self.assertEqual(sorted(dirs.keys()),
                                 ["newer_reader", "newer_writer"])
                for direction, v in dirs.items():
                    self._assert_valid_cell_value(v, f"{c}.{fmt}.{direction}")

    def test_corrections_reference_real_cells(self):
        # Каждая запись журнала поправок (кроме служебных "_..."-ключей)
        # обязана называть реальную клетку "change.format.direction" —
        # иначе журнал причин расходится с самой таблицей.
        exp = _load_json(ROOT + "expected.json")
        corrections = _load_json(ROOT + "expected-corrections.json")
        for key, entry in corrections.items():
            if key.startswith("_"):
                continue
            # Круг 6ter/7: ключ может нести необязательную пометку языка
            # в скобках ("alias_conflict.avro.newer_reader (Java)") —
            # это отдельная, ЯЗЫКОВО-СПЕЦИФИЧНАЯ запись журнала, а не
            # четвёртая часть координаты клетки.
            base_key = re.sub(r"\s*\([^)]*\)\s*$", "", key)
            change, fmt, direction = base_key.split(".")
            self.assertIn(change, CHANGES, key)
            self.assertIn(fmt, ("avro", "json-schema", "protobuf"), key)
            self.assertIn(direction, ("newer_reader", "newer_writer"), key)
            stalo = entry.get("стало")
            if stalo is None:
                # Аннотация без собственного предсказанного значения —
                # например запись, которая только поясняет поведение
                # ОДНОГО языка на уже расщеплённой (by_lang) клетке и не
                # переопределяет само значение (см. "_причина"/
                # "наблюдение" рядом с ней). Сравнивать не с чем.
                continue
            leaves = _leaf_values(exp[change][fmt][direction])
            self.assertIn(stalo, leaves,
                          f"{key}: 'стало'={stalo!r} обязано быть одним из "
                          f"текущих значений клетки {leaves}")


if __name__ == "__main__":
    unittest.main()
