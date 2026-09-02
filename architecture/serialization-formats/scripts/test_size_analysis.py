# -*- coding: utf-8 -*-
"""Предохранители разбора оси размера (Задача 5, включая круг ревью 2).

Ось размера отвечает на вопрос «сколько весит запись» — и ровно
поэтому у неё есть точка отсчёта: контрольное плечо `json`, которое
схему не читает вовсе (schemas/spec.md, §7.1). Несколько заявлений
будущей статьи держатся на этой точке и на межъязыковом равенстве:

  1. `json` и `json-schema` дают ПОБАЙТОВО РАВНЫЙ `bytes` — разница
     между ними только в проверках, не в кодировании.
  2. `schema_bytes`/`schema_file_bytes` контрольного плеча — РОВНО ноль:
     читателю схема не нужна вовсе, и это содержательный ноль, а не
     пропуск.
  3. Поля, описывающие формат и данные (bytes, schema_bytes,
     schema_file_bytes), а также содержимое пачки (batch_hash) обязаны
     совпасть МЕЖДУ РЕАЛИЗАЦИЯМИ — а не только по длине (круг ревью 2,
     находка M1: длина не поймала бы недетерминированную сборку
     protobuf, найденную в находке C3).
  4. Отсутствие данных для проверки — само по себе находка (круг ревью
     2, находка I1), и любая находка — ненулевой код возврата (I2).

Все заявления обязаны быть ПРОВЕРКОЙ, а не совпадением: разбор ловит
расхождение как «находку», а не падает молча и не проходит мимо. Ниже —
защитные проверки самого разбора, а не данных: они работают на
СКОНСТРУИРОВАННЫХ фикстурах, а не на снятой (снятая фикстура —
fixtures/size.txt — либо есть, либо нет, и разбор одинаково обязан вести
себя правильно в обоих случаях).
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_size", ROOT / "scripts" / "analyze-size.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)


def size_row(lang, fmt, record_index, bytes_, zstd_=None, schema_bytes=0,
             schema_file_bytes=None, batch_bytes=None, batch_zstd=None, batch_hash=None,
             batch_content_hash=None):
    """Строит одну строку фикстуры kind=size. schema_file_bytes по
    умолчанию равен schema_bytes (типичный случай для сконструированных
    тестов, где разница между каноническим и файловым весом не при чём
    к проверяемому поведению) — но его можно задать отдельно, когда
    тест именно её и проверяет.

    batch_hash — БАЙТЫ пачки (сравнимо между языками только для
    BYTE_CANONICAL_FORMATS); batch_content_hash — её СОДЕРЖИМОЕ после
    расшифровки (сравнимо всегда, круг ревью 3)."""
    if zstd_ is None:
        zstd_ = bytes_ + 10
    if schema_file_bytes is None:
        schema_file_bytes = schema_bytes
    extra = ""
    if batch_bytes is not None:
        extra += ',"batch_bytes":%d' % batch_bytes
    if batch_zstd is not None:
        extra += ',"batch_zstd":%d' % batch_zstd
    if batch_hash is not None:
        extra += ',"batch_hash":"%s"' % batch_hash
    if batch_content_hash is not None:
        extra += ',"batch_content_hash":"%s"' % batch_content_hash
    return (
        '{"cell":"%s/size/%s/base/same/%d","kind":"size","format":"%s",'
        '"change":"base","direction":"same","record_index":%d,"lang":"%s",'
        '"bytes":%d,"zstd":%d,"schema_bytes":%d,"schema_file_bytes":%d%s,'
        '"record":{"id":1}}'
        % (lang, fmt, record_index, fmt, record_index, lang, bytes_, zstd_,
           schema_bytes, schema_file_bytes, extra)
    )


# Обратная совместимость со старыми тестами круга 1: та же сигнатура
# вызова, что была до появления batch/hash-полей.
def size_row_with_batch(lang, fmt, record_index, bytes_, batch_bytes, batch_zstd, schema_bytes=0):
    return size_row(lang, fmt, record_index, bytes_, schema_bytes=schema_bytes,
                     batch_bytes=batch_bytes, batch_zstd=batch_zstd)


ENV_LINE = '{"kind":"env","zstd_level":3,"go":{"go_version":"go1.25"},"java":{"image":"x"}}'


def fixture(lines, complete=True):
    body = ["# комментарий об условиях съёмки", ENV_LINE] + lines
    if complete:
        body.append("COMPLETE")
    return "\n".join(body) + "\n"


# Пять записей, json и json-schema совпадают побайтово, схема контроля
# нулевая — то, что должно получаться на исправном стенде (см. review
# Задачи 4: все 70 пар строк совпали).
CLEAN_ROWS = (
    [size_row("go", "json", i, 50 + i, schema_bytes=0) for i in range(5)]
    + [size_row("go", "json-schema", i, 50 + i, schema_bytes=169, schema_file_bytes=293) for i in range(5)]
    + [size_row("go", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221) for i in range(5)]
)


class ParsingRequiresCompleteMarker(unittest.TestCase):
    """(а) Разбор обязан падать на фикстуре без COMPLETE — недописанный
    прогон (сеть легла посреди docker run) не должен читаться как валидный,
    пусть и неполный, результат."""

    def test_missing_complete_marker_raises(self):
        text = fixture(CLEAN_ROWS, complete=False)
        with self.assertRaises(an.TruncatedFixture):
            an.parse_fixture(text)

    def test_present_complete_marker_parses(self):
        text = fixture(CLEAN_ROWS, complete=True)
        rows, env = an.parse_fixture(text)
        self.assertEqual(len(rows), len(CLEAN_ROWS))
        self.assertEqual(env["zstd_level"], 3)


class ControlArmByteEquality(unittest.TestCase):
    """(б) + пункт 4 решения контроллера: json и json-schema обязаны дать
    равное число байт. Расхождение — НАХОДКА (объект в списке findings),
    а не необработанное исключение и не молчание."""

    def test_matching_control_arm_produces_no_finding(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        findings = an.find_findings(rows)
        mismatches = [f for f in findings if f["kind"] == "control_mismatch"]
        self.assertEqual(mismatches, [], "на чистых данных расхождений быть не должно")

    def test_control_arm_mismatch_is_reported_as_a_finding(self):
        broken = [size_row("go", "json", i, 50 + i, schema_bytes=0) for i in range(5)]
        # json-schema отличается от контроля на записи 2 — порча, а не
        # свойство формата (см. schemas/spec.md §7.1: байты обязаны
        # совпасть, разница только в проверках).
        broken += [
            size_row("go", "json-schema", i, 50 + i if i != 2 else 999,
                     schema_bytes=169, schema_file_bytes=293)
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(broken))
        # Разбор не имеет права упасть необработанным исключением —
        # находка печатается строкой отчёта, а не рушит скрипт.
        findings = an.find_findings(rows)
        mismatches = [f for f in findings if f["kind"] == "control_mismatch"]
        self.assertEqual(len(mismatches), 1, "ожидали ровно одну находку — по записи 2")
        self.assertEqual(mismatches[0]["record_index"], 2)
        self.assertEqual(mismatches[0]["json_bytes"], 52)
        self.assertEqual(mismatches[0]["json_schema_bytes"], 999)


class ControlArmSchemaWeightIsZero(unittest.TestCase):
    """schema_bytes И schema_file_bytes контрольного плеча обязаны быть
    нулём. Если хоть один ненулевой — плечо где-то тянет схему, и
    утверждение «читателю схема не нужна» перестаёт быть верным; это
    тоже находка, а не тихий проход."""

    def test_nonzero_control_schema_bytes_is_a_finding(self):
        broken = [size_row("go", "json", i, 50 + i, schema_bytes=0) for i in range(5)]
        broken[0] = size_row("go", "json", 0, 50, schema_bytes=42)  # порча на записи 0
        broken += [size_row("go", "json-schema", i, 50 + i, schema_bytes=169, schema_file_bytes=293)
                   for i in range(5)]
        rows, _ = an.parse_fixture(fixture(broken))
        findings = an.find_findings(rows)
        weight = [f for f in findings if f["kind"] == "control_schema_weight"]
        self.assertEqual(len(weight), 1)
        self.assertEqual(weight[0]["record_index"], 0)
        self.assertEqual(weight[0]["schema_bytes"], 42)

    def test_nonzero_control_schema_file_bytes_is_a_finding(self):
        broken = [size_row("go", "json", i, 50 + i, schema_bytes=0) for i in range(5)]
        broken[0] = size_row("go", "json", 0, 50, schema_bytes=0, schema_file_bytes=17)
        broken += [size_row("go", "json-schema", i, 50 + i, schema_bytes=169, schema_file_bytes=293)
                   for i in range(5)]
        rows, _ = an.parse_fixture(fixture(broken))
        findings = an.find_findings(rows)
        weight = [f for f in findings if f["kind"] == "control_schema_file_weight"]
        self.assertEqual(len(weight), 1)
        self.assertEqual(weight[0]["schema_file_bytes"], 17)

    def test_zero_control_schema_bytes_produces_no_finding(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        findings = an.find_findings(rows)
        self.assertEqual([f for f in findings
                           if f["kind"] in ("control_schema_weight", "control_schema_file_weight")], [])


class CompactnessClaimRequiresControlArm(unittest.TestCase):
    """(в) Нельзя вывести «формат N компактнее», если контрольного плеча
    нет вовсе — сравнивать не с чем, и отсутствие контроля не должно
    молча подставлять вместо точки отсчёта что-то другое."""

    def test_comparison_without_control_arm_refuses(self):
        # Ни одной строки json — только json-schema/avro.
        rows_text = [size_row("go", "json-schema", i, 50 + i, schema_bytes=169, schema_file_bytes=293)
                     for i in range(5)]
        rows_text += [size_row("go", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221)
                      for i in range(5)]
        rows, _ = an.parse_fixture(fixture(rows_text))
        with self.assertRaises(an.ControlArmMissing):
            an.compare_formats(rows)

    def test_comparison_with_control_arm_succeeds(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        comparison = an.compare_formats(rows)
        self.assertIn("go", comparison)
        self.assertIn("avro", comparison["go"])
        # avro компактнее контроля на этих данных — отношение < 1.
        self.assertLess(comparison["go"]["avro"]["bytes_ratio_to_control"], 1.0)


class MissingControlArmIsItselfAFinding(unittest.TestCase):
    """Круг ревью 2, находка I1: раньше отсутствие контроля обходилось
    молчанием в find_findings (просто continue) — отчёт мог одновременно
    сказать «сравнение недоступно» И «находок нет», хотя второе
    утверждалось о данных, которых не было. Теперь отсутствие контроля —
    полноценная находка."""

    def test_lang_without_control_arm_gets_no_control_arm_finding(self):
        rows_text = [size_row("go", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221)
                     for i in range(5)]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows)
        no_control = [f for f in findings if f["kind"] == "no_control_arm"]
        self.assertEqual(len(no_control), 1)
        self.assertEqual(no_control[0]["lang"], "go")

    def test_clean_data_has_no_such_finding(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        findings = an.find_findings(rows)
        self.assertEqual([f for f in findings if f["kind"] == "no_control_arm"], [])


class FindingsGiveNonzeroExitCode(unittest.TestCase):
    """Круг ревью 2, находка I2: любая находка обязана давать ненулевой
    код возврата у main() — иначе автоматический прогон не отличит
    исправный стенд от сломанного."""

    def test_main_returns_zero_on_clean_fixture(self):
        code = self._run_main(CLEAN_ROWS)
        self.assertEqual(code, 0)

    def test_main_returns_nonzero_when_a_finding_exists(self):
        broken = [size_row("go", "json", i, 50 + i, schema_bytes=0) for i in range(5)]
        broken[0] = size_row("go", "json", 0, 50, schema_bytes=42)  # порча
        broken += [size_row("go", "json-schema", i, 50 + i, schema_bytes=169, schema_file_bytes=293)
                   for i in range(5)]
        code = self._run_main(broken)
        self.assertNotEqual(code, 0)

    @staticmethod
    def _run_main(rows_text):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False, encoding="utf-8") as f:
            f.write(fixture(rows_text))
            path = f.name
        try:
            import os
            old = os.environ.get("FIXTURE")
            os.environ["FIXTURE"] = path
            try:
                an.FIXTURE = an.Path(path)
                return an.main()
            finally:
                if old is None:
                    os.environ.pop("FIXTURE", None)
                else:
                    os.environ["FIXTURE"] = old
        finally:
            import os
            os.unlink(path)


class BatchSizeIsACellPropertyNotARecordProperty(unittest.TestCase):
    """Задача 5, находка координатора №2 (круг 1): одна запись меряет
    только заголовок кадра zstd, а не сжатие; batch_bytes/batch_zstd
    отвечают на настоящий вопрос оси — остаётся ли разница между
    форматами на входе побольше одной записи. Оба поля — свойства
    КЛЕТКИ, а не отдельной записи, и разбор обязан доставать их как одно
    число, а не как среднее по пяти."""

    def test_batch_fields_surface_as_single_value_per_format(self):
        rows_text = [
            size_row_with_batch("go", "json", i, 50 + i, batch_bytes=276, batch_zstd=163)
            for i in range(5)
        ] + [
            size_row_with_batch("go", "avro", i, 27 + i, batch_bytes=146, batch_zstd=129,
                                 schema_bytes=221)
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        comparison = an.compare_formats(rows)
        self.assertEqual(comparison["go"]["avro"]["batch_bytes"], 146)
        self.assertEqual(comparison["go"]["avro"]["batch_zstd"], 129)
        # avro тоже компактнее контроля под пачечным сжатием на этих
        # данных — то же отношение, что и по bytes, но для batch_zstd.
        self.assertLess(comparison["go"]["avro"]["batch_zstd_ratio_to_control"], 1.0)

    def test_missing_batch_fields_degrade_to_none_not_a_crash(self):
        # Старые данные без пачки (до фикса координатора) — разбор не
        # должен падать с KeyError на отсутствующем поле.
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        comparison = an.compare_formats(rows)
        self.assertIsNone(comparison["go"]["avro"]["batch_bytes"])
        self.assertIsNone(comparison["go"]["avro"]["batch_zstd"])

    def test_batch_value_disagreeing_across_records_of_the_cell_is_not_silently_picked(self):
        # Порча: одна из пяти строк клетки несёт другой batch_bytes —
        # разбор обязан вернуть None (расхождение), а не тихо взять
        # первое попавшееся значение.
        broken = [
            size_row_with_batch("go", "json", i, 50 + i, batch_bytes=276, batch_zstd=163)
            for i in range(5)
        ] + [size_row_with_batch("go", "avro", i, 27 + i, batch_bytes=146, batch_zstd=129,
                                  schema_bytes=221) for i in range(5)]
        broken[6] = size_row_with_batch("go", "avro", 1, 28, batch_bytes=999, batch_zstd=129,
                                         schema_bytes=221)
        rows, _ = an.parse_fixture(fixture(broken))
        comparison = an.compare_formats(rows)
        self.assertIsNone(comparison["go"]["avro"]["batch_bytes"])


class CrossLanguageEqualityIsChecked(unittest.TestCase):
    """Круг ревью 2, находка M1: раньше в строке лежала ДЛИНА пачки, а
    не её содержимое, поэтому «побайтовое совпадение между языками» было
    на самом деле «совпадение длин» — и именно поэтому недетерминированная
    сборка protobuf (находка C3) не была поймана раньше. Разбор сравнивает
    СОДЕРЖИМОЕ, а не только длины отдельных полей.

    Круг ревью 3: побайтовое равенство между реализациями — не
    универсальное требование, а свойство ФОРМАТА (BYTE_CANONICAL_FORMATS
    в analyze-size.py). Avro и Protobuf его гарантируют (первый —
    спецификацией, второй — эмпирически при детерминированном
    кодировании) и сравниваются по batch_hash (байты). JSON и JSON
    Schema не специфицируют порядок ключей объекта вовсе — там
    настоящее равенство проверяется через batch_content_hash
    (расшифрованное содержимое), а разные batch_hash при одинаковом
    batch_content_hash — известное и ожидаемое свойство, не находка."""

    def test_matching_languages_produce_no_cross_lang_finding(self):
        rows_text = [
            size_row("go", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221,
                     batch_bytes=146, batch_hash="deadbeef")
            for i in range(5)
        ] + [
            size_row("java", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221,
                     batch_bytes=146, batch_hash="deadbeef")
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows)
        self.assertEqual([f for f in findings
                           if f["kind"] in ("cross_lang_mismatch", "batch_hash_mismatch",
                                             "batch_content_mismatch")], [])

    def test_differing_bytes_between_languages_is_a_cross_lang_finding(self):
        rows_text = [size_row("go", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221)
                     for i in range(5)]
        # Java на записи 3 внезапно даёт другое число байт — порча.
        java_rows = [size_row("java", "avro", i, 27 + i, schema_bytes=162, schema_file_bytes=221)
                     for i in range(5)]
        java_rows[3] = size_row("java", "avro", 3, 999, schema_bytes=162, schema_file_bytes=221)
        rows, _ = an.parse_fixture(fixture(rows_text + java_rows))
        findings = an.find_findings(rows)
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "bytes"]
        self.assertEqual(len(mismatches), 1)
        self.assertEqual(mismatches[0]["record_index"], 3)

    def test_differing_batch_hash_between_languages_is_a_finding_for_byte_canonical_formats(self):
        # Это ровно находка C3: длины (batch_bytes) совпадают, а БАЙТЫ
        # (batch_hash) — нет, из-за недетерминированного порядка полей
        # при сборке. Protobuf гарантирует побайтовое равенство
        # (эмпирически, при детерминированном кодировании) — поэтому
        # расхождение здесь красная находка, а не ожидаемое свойство.
        rows_text = [
            size_row("go", "protobuf", i, 30, schema_bytes=119, schema_file_bytes=119,
                     batch_bytes=161, batch_hash="aaaa", batch_content_hash="same")
            for i in range(5)
        ] + [
            size_row("java", "protobuf", i, 30, schema_bytes=119, schema_file_bytes=119,
                     batch_bytes=161, batch_hash="bbbb", batch_content_hash="same")
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows)
        mismatches = [f for f in findings if f["kind"] == "batch_hash_mismatch"]
        self.assertEqual(len(mismatches), 1)
        self.assertEqual(mismatches[0]["format"], "protobuf")
        # А batch_content_mismatch тут быть не должно: content_hash совпал,
        # и для protobuf он даже не единственный источник истины.
        self.assertEqual([f for f in findings if f["kind"] == "batch_content_mismatch"], [])

    def test_differing_batch_hash_for_json_with_matching_content_is_not_a_finding(self):
        # ГЛАВНЫЙ случай круга 3: json/json-schema не гарантируют
        # порядок ключей — Go сортирует по алфавиту, Java сохраняет
        # порядок вставки. batch_hash поэтому РАЗНЫЙ у корректного
        # стенда, и это ОЖИДАЕМО, а не находка — только
        # batch_content_hash (расшифрованное содержимое) обязан
        # совпасть, и здесь он совпадает.
        rows_text = [
            size_row("go", "json", i, 51, schema_bytes=0, schema_file_bytes=0,
                     batch_bytes=276, batch_hash="go-order-hash", batch_content_hash="same-content")
            for i in range(5)
        ] + [
            size_row("java", "json", i, 51, schema_bytes=0, schema_file_bytes=0,
                     batch_bytes=276, batch_hash="java-order-hash", batch_content_hash="same-content")
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows)
        self.assertEqual([f for f in findings
                           if f["kind"] in ("batch_hash_mismatch", "batch_content_mismatch")], [])

    def test_differing_batch_content_hash_for_json_is_still_a_finding(self):
        # Настоящая порча содержимого (не порядка ключей — реального
        # значения) обязана по-прежнему краснеть у json/json-schema.
        rows_text = [
            size_row("go", "json-schema", i, 51, schema_bytes=226, schema_file_bytes=293,
                     batch_bytes=276, batch_hash="go-order-hash", batch_content_hash="real-content")
            for i in range(5)
        ] + [
            size_row("java", "json-schema", i, 51, schema_bytes=226, schema_file_bytes=293,
                     batch_bytes=276, batch_hash="java-order-hash", batch_content_hash="corrupted-content")
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows)
        mismatches = [f for f in findings if f["kind"] == "batch_content_mismatch"]
        self.assertEqual(len(mismatches), 1)
        self.assertEqual(mismatches[0]["format"], "json-schema")
        # А различие в batch_hash тут ожидаемо и НЕ должно давать
        # отдельную находку сверху — иначе одна порча считалась бы дважды
        # разными путями, один из которых ложный.
        self.assertEqual([f for f in findings if f["kind"] == "batch_hash_mismatch"], [])

    def test_single_language_data_does_not_trigger_cross_lang_checks(self):
        # Один язык в фикстуре — сравнивать не с чем, это не находка.
        rows, _ = an.parse_fixture(fixture(CLEAN_ROWS))
        findings = an.find_findings(rows)
        self.assertEqual([f for f in findings
                           if f["kind"] in ("cross_lang_mismatch", "batch_hash_mismatch",
                                             "batch_content_mismatch")], [])


if __name__ == "__main__":
    unittest.main()
