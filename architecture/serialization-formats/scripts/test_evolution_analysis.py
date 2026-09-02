# -*- coding: utf-8 -*-
"""Предохранители разбора оси эволюции схемы (Задача 6 + круги правок
6bis, 6ter).

Круг 6ter — по требованию ревью — перенёс "известные исключения"
(клетка расщепляется предсказуемо; языки законно расходятся) ИЗ КОДА
РАЗБОРА В ДАННЫЕ: раньше это были множества координат прямо в
analyze-evolution.py (KNOWN_SPLIT_CELLS, KNOWN_LANGUAGE_DIVERGENT_CELLS)
— самая ответственная часть стенда (что валит прогон, а что нет) жила
вне цепочки дайджестов манифеста. Теперь запись schemas/expected.json
может быть не только строкой-исходом, но и структурой:

  - {"by_record": {"0": "wrong", "1": "refused", ...}, "reason": "..."}
    — клетка расщепляется предсказуемо, исход зависит от номера записи;
  - {"by_lang": {"go": "wrong", "java": "refused"}, "reason": "..."}
    — языки законно дают разные исходы (расхождение библиотек, не
    стенда).

Разбор эти структуры ТОЛЬКО ЧИТАЕТ (resolve_expected,
declares_lang_divergence) — не хранит копию решения нигде ещё. Любое
значение, не совпадающее с объявленным, — обычная, свежая находка, без
исключений: тесты этого файла явно проверяют, что НЕобъявленное
расхождение (в том числе НА ТОЙ ЖЕ клетке, что уже что-то объявляет, но
иначе) обязано красить прогон.

Полный список того, что здесь проверяется (решение контроллера,
task-6-brief.md, + круги правок):

  1. Сверка каждой клетки таблицы эволюции — ТОЛЬКО для kind=compat,
     через resolve_expected (строка / by_lang / by_record).
  2. Клетка с исходом "wrong" обязана нести КЛЮЧ "got" — falsy, но
     присутствующее значение легитимно.
  3. Расщепление клетки — чисто описательная сводка для отчёта
     (_cell_split_findings), НЕ находка сама по себе: корректность уже
     полностью проверена требованием 1 на уровне отдельных записей.
  4. Межъязыковая построчная сверка по всем полям контракта, кроме
     cell, lang и error — с исключением ТОЛЬКО там, где каталог
     ЯВНО объявил by_lang и наблюдение СОВПАДАЕТ с объявленным для
     каждого языка. Всё прочее красит прогон, даже на клетке, которая
     уже что-то объявляет.
  5. Оборванная ИЛИ повреждённая посередине фикстура валит разбор с
     внятным сообщением.
  6. Полнота клетки (5 записей 0..4) и общее число строк фикстуры
     совпадает с шапкой.
  7. Любая находка — ненулевой код возврата у main().

Тесты работают на СКОНСТРУИРОВАННЫХ фикстурах и таблицах ожиданий.
"""

import importlib.util
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_evolution", ROOT / "scripts" / "analyze-evolution.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)


def evo_row(lang, kind, fmt, change, direction, record_index, outcome,
            got=None, want=None, record=None, encoded=True, bytes_=50,
            stage="decode", error=None, got_present=None):
    """Строит одну строку фикстуры compat/roundtrip — ту же форму, что
    печатает настоящая проба (go/cmd/probe/main.go: probeResult).

    got_present позволяет положить в строку КЛЮЧ "got" с ЛОЖНЫМ, но
    присутствующим значением (например {} или "")."""
    obj = {
        "cell": "%s/%s/%s/%s/%s/%d" % (lang, kind, fmt, change, direction, record_index),
        "kind": kind,
        "format": fmt,
        "change": change,
        "direction": direction,
        "record_index": record_index,
        "stage": stage,
        "lang": lang,
        "outcome": outcome,
        "encoded": encoded,
        "bytes": bytes_,
    }
    if record is not None:
        obj["record"] = record
    if want is not None:
        obj["want"] = want
    if got is not None:
        obj["got"] = got
    elif got_present is not None:
        obj["got"] = got_present
    if error is not None:
        obj["error"] = error
    return json.dumps(obj, ensure_ascii=False)


ENV_LINE = json.dumps({"kind": "env", "go": {"go_version": "go1.25"},
                        "java": {"image": "x"}}, ensure_ascii=False)


def env_line(expected_lines=None):
    obj = {"kind": "env", "go": {"go_version": "go1.25"}, "java": {"image": "x"}}
    if expected_lines is not None:
        obj["expected_lines"] = expected_lines
    return json.dumps(obj, ensure_ascii=False)


def fixture(lines, complete=True, env=ENV_LINE):
    body = ["# комментарий об условиях съёмки", env] + lines
    if complete:
        body.append("COMPLETE")
    return "\n".join(body) + "\n"


def uniform_cell(lang, kind, fmt, change, direction, outcome, want=None, got=None, record=None):
    """Пять строк одной клетки с ОДИНАКОВЫМ исходом."""
    return [
        evo_row(lang, kind, fmt, change, direction, i, outcome,
                want=want, got=got, record=record)
        for i in range(5)
    ]


# Клетка avro/retype/newer_reader ожидается "refused" по expected.json.
CLEAN_CELL_GO = uniform_cell("go", "compat", "avro", "retype", "newer_reader",
                              "refused", want=None, got=None, record={"id": 1})
CLEAN_CELL_JAVA = uniform_cell("java", "compat", "avro", "retype", "newer_reader",
                                "refused", want=None, got=None, record={"id": 1})

MINI_EXPECTED = {
    "retype": {"avro": {"newer_reader": "refused", "newer_writer": "refused"}},
}

MINI_EXPECTED_NEWER_READER_ONLY = {
    "retype": {"avro": {"newer_reader": "refused"}},
}


class ParsingRequiresCompleteMarker(unittest.TestCase):
    """Оборванная фикстура обязана валить разбор, а не давать частичный
    результат."""

    def test_missing_complete_marker_raises(self):
        text = fixture(CLEAN_CELL_GO, complete=False)
        with self.assertRaises(an.TruncatedFixture):
            an.parse_fixture(text)

    def test_present_complete_marker_parses(self):
        text = fixture(CLEAN_CELL_GO, complete=True)
        rows, env = an.parse_fixture(text)
        self.assertEqual(len(rows), 5)
        self.assertIn("go", env)

    def test_unknown_kind_is_rejected(self):
        bad = ['{"kind":"size","format":"avro"}']
        with self.assertRaises(ValueError):
            an.parse_fixture(fixture(bad))

    def test_missing_env_line_is_rejected(self):
        text = "\n".join(["# комментарий"] + CLEAN_CELL_GO + ["COMPLETE"]) + "\n"
        with self.assertRaises(ValueError):
            an.parse_fixture(text)

    def test_empty_rows_are_rejected(self):
        text = "\n".join(["# комментарий", ENV_LINE, "COMPLETE"]) + "\n"
        with self.assertRaises(ValueError):
            an.parse_fixture(text)

    def test_corrupted_line_in_the_middle_raises_truncated_fixture_with_a_clear_message(self):
        text = "\n".join(["# комментарий", ENV_LINE,
                           CLEAN_CELL_GO[0], "{не json", CLEAN_CELL_GO[1], "COMPLETE"]) + "\n"
        with self.assertRaises(an.TruncatedFixture) as ctx:
            an.parse_fixture(text)
        self.assertIn("№", str(ctx.exception), "сообщение обязано называть номер строки")


class ResolveExpectedHandlesAllThreeShapes(unittest.TestCase):
    """Круг 6ter: resolve_expected — единственное место, где разбор
    понимает структуру каталога. Строка, by_lang, by_record, и их
    вложенность друг в друга."""

    def test_plain_string_applies_to_every_lang_and_record(self):
        self.assertEqual(an.resolve_expected("refused", "go", 0), "refused")
        self.assertEqual(an.resolve_expected("refused", "java", 4), "refused")

    def test_by_lang_picks_the_right_language(self):
        entry = {"by_lang": {"go": "wrong", "java": "refused"}, "reason": "..."}
        self.assertEqual(an.resolve_expected(entry, "go", 0), "wrong")
        self.assertEqual(an.resolve_expected(entry, "java", 0), "refused")

    def test_by_lang_missing_language_resolves_to_none(self):
        entry = {"by_lang": {"go": "wrong"}, "reason": "..."}
        self.assertIsNone(an.resolve_expected(entry, "java", 0))

    def test_by_record_picks_the_right_record(self):
        entry = {"by_record": {"0": "wrong", "1": "refused"}, "reason": "..."}
        self.assertEqual(an.resolve_expected(entry, "go", 0), "wrong")
        self.assertEqual(an.resolve_expected(entry, "go", 1), "refused")

    def test_by_record_missing_index_resolves_to_none(self):
        entry = {"by_record": {"0": "wrong"}, "reason": "..."}
        self.assertIsNone(an.resolve_expected(entry, "go", 3))

    def test_malformed_entry_raises(self):
        with self.assertRaises(an.MalformedExpectedEntry):
            an.resolve_expected({"neither_key": True}, "go", 0)
        with self.assertRaises(an.MalformedExpectedEntry):
            an.resolve_expected(42, "go", 0)

    def test_declares_lang_divergence_true_for_by_lang(self):
        self.assertTrue(an.declares_lang_divergence({"by_lang": {"go": "a", "java": "b"}}))

    def test_declares_lang_divergence_false_for_plain_string(self):
        self.assertFalse(an.declares_lang_divergence("refused"))

    def test_declares_lang_divergence_false_for_by_record_without_by_lang(self):
        self.assertFalse(an.declares_lang_divergence({"by_record": {"0": "wrong", "1": "refused"}}))

    def test_declares_lang_divergence_true_when_nested_under_by_record(self):
        entry = {"by_record": {"0": {"by_lang": {"go": "a", "java": "b"}}, "1": "refused"}}
        self.assertTrue(an.declares_lang_divergence(entry))


class ExpectedComparisonHandlesStructuredEntries(unittest.TestCase):
    """Требование 1 после круга 6ter: сверка проходит через
    resolve_expected прозрачно — по-прежнему выдаёт expected_mismatch
    для расхождения, но теперь без хардкода "эта клетка особая"."""

    def test_matching_cell_produces_no_expected_mismatch(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_CELL_GO + CLEAN_CELL_JAVA))
        findings = an.find_findings(rows, MINI_EXPECTED)
        self.assertEqual([f for f in findings if f["kind"] == "expected_mismatch"], [])

    def test_diverging_cell_is_an_expected_mismatch_finding(self):
        broken = uniform_cell("go", "compat", "avro", "retype", "newer_reader",
                               "wrong", want={"id": 1}, got={"id": 2})
        rows, _ = an.parse_fixture(fixture(broken))
        findings = an.find_findings(rows, MINI_EXPECTED)
        mismatches = [f for f in findings if f["kind"] == "expected_mismatch"]
        self.assertEqual(len(mismatches), 5)
        self.assertEqual(mismatches[0]["expected"], "refused")
        self.assertEqual(mismatches[0]["observed"], "wrong")

    def test_roundtrip_rows_are_never_compared_to_expected_json(self):
        rows_text = uniform_cell("go", "roundtrip", "avro", "retype", "newer_reader",
                                  "wrong", want={"id": 1}, got={"id": 2})
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, MINI_EXPECTED)
        self.assertEqual([f for f in findings if f["kind"] == "expected_mismatch"], [])

    def test_missing_cell_in_fixture_is_reported(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_CELL_GO))  # только newer_reader
        findings = an.find_findings(rows, MINI_EXPECTED)
        missing = [f for f in findings if f["kind"] == "missing_cell"
                   and f["direction"] == "newer_writer"]
        self.assertEqual(len(missing), 1)

    def test_cell_in_fixture_without_a_catalog_entry_is_reported(self):
        rows_text = uniform_cell("go", "compat", "avro", "brand_new_change", "newer_reader", "ok")
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, MINI_EXPECTED)
        self.assertEqual(len([f for f in findings if f["kind"] == "unexpected_cell"]), 1)

    def test_by_record_entry_matching_observation_produces_no_mismatch(self):
        # Ровно форма retype_message/protobuf/newer_reader: запись 0
        # даёт wrong, остальные четыре — refused, каталог объявляет это
        # явно через by_record, и совпадающее наблюдение — не находка.
        expected = {
            "retype_message": {"protobuf": {"newer_reader": {
                "by_record": {"0": "wrong", "1": "refused", "2": "refused", "3": "refused", "4": "refused"},
                "reason": "wire-тип LEN общий у string и embedded message",
            }}},
        }
        rows_text = [
            evo_row("go", "compat", "protobuf", "retype_message", "newer_reader", i,
                    "wrong" if i == 0 else "refused")
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, expected)
        self.assertEqual([f for f in findings if f["kind"] == "expected_mismatch"], [])

    def test_by_record_entry_with_deviating_record_is_still_a_fresh_finding(self):
        # "Сломай нарочно" (требование ревью): запись 2 ДОЛЖНА быть
        # refused по каталогу, но пришла wrong — обязана покраснеть,
        # даже на клетке, которая уже официально "особая".
        expected = {
            "retype_message": {"protobuf": {"newer_reader": {
                "by_record": {"0": "wrong", "1": "refused", "2": "refused", "3": "refused", "4": "refused"},
                "reason": "...",
            }}},
        }
        rows_text = [
            evo_row("go", "compat", "protobuf", "retype_message", "newer_reader", i,
                    "wrong" if i in (0, 2) else "refused")  # запись 2 — НЕОБЪЯВЛЕННОЕ отклонение
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, expected)
        mismatches = [f for f in findings if f["kind"] == "expected_mismatch"]
        self.assertEqual(len(mismatches), 1)
        self.assertEqual(mismatches[0]["record_index"], 2)
        self.assertEqual(mismatches[0]["expected"], "refused")
        self.assertEqual(mismatches[0]["observed"], "wrong")

    def test_by_lang_entry_matching_observation_produces_no_mismatch(self):
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "hamba/avro разрешает молча, org.apache.avro отказывает",
            }}},
        }
        go_rows = uniform_cell("go", "compat", "avro", "alias_conflict", "newer_reader", "wrong")
        java_rows = uniform_cell("java", "compat", "avro", "alias_conflict", "newer_reader", "refused")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        self.assertEqual([f for f in findings if f["kind"] == "expected_mismatch"], [])

    def test_by_lang_entry_with_wrong_language_value_is_still_a_finding(self):
        # Java ВНЕЗАПНО тоже начинает давать wrong — это уже не то,
        # что объявлено каталогом для Java (refused), и обязано
        # покраснеть, а не молча засчитаться "почти тем же самым".
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "...",
            }}},
        }
        go_rows = uniform_cell("go", "compat", "avro", "alias_conflict", "newer_reader", "wrong")
        java_rows = uniform_cell("java", "compat", "avro", "alias_conflict", "newer_reader", "wrong")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        mismatches = [f for f in findings if f["kind"] == "expected_mismatch" and f["lang"] == "java"]
        self.assertEqual(len(mismatches), 5)

    def test_incomplete_by_lang_coverage_is_its_own_finding(self):
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong"},  # java не объявлен вовсе
                "reason": "...",
            }}},
        }
        go_rows = uniform_cell("go", "compat", "avro", "alias_conflict", "newer_reader", "wrong")
        java_rows = uniform_cell("java", "compat", "avro", "alias_conflict", "newer_reader", "refused")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        incomplete = [f for f in findings if f["kind"] == "expected_entry_incomplete" and f["lang"] == "java"]
        self.assertEqual(len(incomplete), 5)


class WrongOutcomeRequiresObservedValue(unittest.TestCase):
    """Требование 2: клетка с исходом wrong обязана нести КЛЮЧ "got"."""

    def test_wrong_without_got_is_a_finding(self):
        rows_text = [evo_row("go", "compat", "protobuf", "reuse_tag", "newer_reader", 0,
                              "wrong", want={"login_count": 0})]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        unproven = [f for f in findings if f["kind"] == "wrong_without_observation"]
        self.assertEqual(len(unproven), 1)

    def test_wrong_with_got_produces_no_such_finding(self):
        rows_text = [evo_row("go", "compat", "protobuf", "reuse_tag", "newer_reader", 0,
                              "wrong", want={"login_count": 0}, got={"login_count": 5})]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "wrong_without_observation"], [])

    def test_wrong_with_empty_but_present_got_is_not_a_finding(self):
        rows_text = [evo_row("go", "compat", "protobuf", "retype_message", "newer_reader", 0,
                              "wrong", want={"email": "x"}, got_present={})]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "wrong_without_observation"], [])

    def test_ok_outcome_without_got_is_not_flagged(self):
        rows_text = [evo_row("go", "compat", "avro", "add_default", "newer_reader", 0, "ok")]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "wrong_without_observation"], [])


class CellSplitIsDescriptiveOnly(unittest.TestCase):
    """Требование 3 после круга 6ter: расщепление клетки — чисто
    описательная сводка (_cell_split_findings), для отчёта, но НЕ
    находка сама по себе и НЕ часть find_findings — корректность каждой
    записи уже проверена требованием 1."""

    def test_uniform_cell_has_no_split(self):
        idx = an._index(an.parse_fixture(fixture(CLEAN_CELL_GO))[0])
        self.assertEqual(an._cell_split_findings(idx), [])

    def test_split_cell_is_reported_with_all_outcomes(self):
        rows_text = [
            evo_row("go", "compat", "protobuf", "reuse_tag", "newer_reader", i,
                    "wrong" if i != 3 else "ok")
            for i in range(5)
        ]
        idx = an._index(an.parse_fixture(fixture(rows_text))[0])
        splits = an._cell_split_findings(idx)
        self.assertEqual(len(splits), 1)
        self.assertEqual(splits[0]["outcomes_by_record"][3], "ok")

    def test_split_matching_catalog_is_not_in_find_findings_at_all(self):
        # Ключевая проверка круга 6ter: даже когда клетка ФАКТИЧЕСКИ
        # расщеплена, find_findings ничего про это не возвращает как
        # "находку" — расщепление есть только в отчётной сводке.
        expected = {
            "retype_message": {"protobuf": {"newer_reader": {
                "by_record": {"0": "wrong", "1": "refused", "2": "refused", "3": "refused", "4": "refused"},
                "reason": "...",
            }}},
        }
        rows_text = [
            evo_row("go", "compat", "protobuf", "retype_message", "newer_reader", i,
                    "wrong" if i == 0 else "refused",
                    got={"value": ""} if i == 0 else None)
            for i in range(5)
        ]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, expected)
        self.assertEqual([f for f in findings if f["kind"] == "cell_split"], [])
        self.assertEqual(findings, [])


class CrossLanguageRowByRowComparison(unittest.TestCase):
    """Требование 4: Go и Java обязаны совпасть построчно по всем полям
    контракта, кроме cell, lang и error — за вычетом ОБЪЯВЛЕННОГО
    каталогом расхождения (круг 6ter)."""

    def test_identical_rows_produce_no_finding(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_CELL_GO + CLEAN_CELL_JAVA))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "cross_lang_mismatch"], [])

    def test_differing_outcome_between_languages_is_critical_when_undeclared(self):
        go_rows = uniform_cell("go", "compat", "avro", "remove", "newer_reader", "ok")
        java_rows = uniform_cell("java", "compat", "avro", "remove", "newer_reader", "wrong")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, {})
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "outcome"]
        self.assertEqual(len(mismatches), 5)

    def test_differing_want_or_got_between_languages_is_flagged(self):
        go_rows = [evo_row("go", "compat", "protobuf", "retype", "newer_reader", 0, "wrong",
                            want={"age": "30"}, got={"age": 30})]
        java_rows = [evo_row("java", "compat", "protobuf", "retype", "newer_reader", 0, "wrong",
                              want={"age": "30"}, got={"age": 999})]
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, {})
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "got"]
        self.assertEqual(len(mismatches), 1)

    def test_differing_error_text_between_languages_is_not_a_finding(self):
        go_rows = [evo_row("go", "compat", "avro", "retype", "newer_reader", 0, "refused",
                            error="разрешение схем Avro отказало: несовместимые типы")]
        java_rows = [evo_row("java", "compat", "avro", "retype", "newer_reader", 0, "refused",
                              error="Avro schema resolution failed: incompatible types")]
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "cross_lang_mismatch"], [])

    def test_row_present_in_only_one_language_is_flagged(self):
        go_rows = uniform_cell("go", "compat", "avro", "rename", "newer_writer", "ok")
        shared_go = uniform_cell("go", "compat", "avro", "add_default", "newer_reader", "ok")
        shared_java = uniform_cell("java", "compat", "avro", "add_default", "newer_reader", "ok")
        rows, _ = an.parse_fixture(fixture(go_rows + shared_go + shared_java))
        findings = an.find_findings(rows, {})
        missing = [f for f in findings if f["kind"] == "missing_lang_row"]
        self.assertEqual(len(missing), 5)

    def test_single_language_data_does_not_trigger_cross_lang_checks_globally(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_CELL_GO))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings
                           if f["kind"] in ("cross_lang_mismatch", "missing_lang_row")], [])

    def test_declared_divergence_matching_catalog_exempts_outcome_derived_fields(self):
        # Круг 6ter, случай alias_conflict: каталог объявляет by_lang, и
        # наблюдение ТОЧНО совпадает с объявленным для каждого языка —
        # разница в outcome/want/got/stage/bytes/encoded не должна
        # красить прогон.
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "...",
            }}},
        }
        go_rows = [evo_row("go", "compat", "avro", "alias_conflict", "newer_reader", i,
                            "wrong", want={"id": 1, "name": "Анна"}, got={"id": 1, "name": "anna@x"},
                            stage="decode", bytes_=27, encoded=True)
                   for i in range(5)]
        java_rows = [evo_row("java", "compat", "avro", "alias_conflict", "newer_reader", i,
                              "refused", want={"id": 1, "name": "Анна"}, error="Duplicate field name",
                              stage="decode", bytes_=27, encoded=True)
                     for i in range(5)]
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        self.assertEqual([f for f in findings if f["kind"] == "cross_lang_mismatch"], [])

    def test_record_field_still_must_match_even_under_declared_divergence(self):
        # "record" не входит в OUTCOME_DERIVED_FIELDS: входная запись
        # обязана быть одной и той же у обоих языков ВСЕГДА, даже на
        # клетке с объявленным расхождением исходов.
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "...",
            }}},
        }
        go_rows = [evo_row("go", "compat", "avro", "alias_conflict", "newer_reader", i,
                            "wrong", record={"id": 1, "name": "Анна", "email": "anna@example.com"})
                   for i in range(5)]
        java_rows = [evo_row("java", "compat", "avro", "alias_conflict", "newer_reader", i,
                              "refused", record={"id": 1, "name": "ДРУГАЯ ЗАПИСЬ"})
                     for i in range(5)]
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "record"]
        self.assertEqual(len(mismatches), 5)

    def test_undeclared_divergence_on_a_cell_with_no_catalog_entry_still_burns(self):
        # "Сломай нарочно" (прямое требование ревью): расхождение той же
        # ФОРМЫ (wrong vs refused), что и известный alias_conflict, но
        # на СОВЕРШЕННО ДРУГОЙ клетке, у которой каталог вообще не
        # объявляет никакого by_lang, — обязано покраснеть как обычный
        # cross_lang_mismatch.
        expected = {"remove": {"avro": {"newer_reader": "wrong"}}}  # НЕТ by_lang
        go_rows = uniform_cell("go", "compat", "avro", "remove", "newer_reader", "wrong")
        java_rows = uniform_cell("java", "compat", "avro", "remove", "newer_reader", "refused")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "outcome"]
        self.assertEqual(len(mismatches), 5, "необъявленное расхождение обязано красить прогон")

    def test_undeclared_divergence_on_a_cell_that_partially_matches_by_lang_still_burns(self):
        # Ещё жёстче: клетка ИМЕЕТ объявление by_lang, но НАБЛЮДЕНИЕ не
        # совпадает с ним (например, регрессия сдвинула исход Java на
        # что-то третье) — снисхождения быть не должно вовсе, поле
        # "outcome" обязано покраснеть как cross_lang_mismatch.
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "...",
            }}},
        }
        go_rows = uniform_cell("go", "compat", "avro", "alias_conflict", "newer_reader", "wrong")
        java_rows = uniform_cell("java", "compat", "avro", "alias_conflict", "newer_reader", "error")
        rows, _ = an.parse_fixture(fixture(go_rows + java_rows))
        findings = an.find_findings(rows, expected)
        mismatches = [f for f in findings if f["kind"] == "cross_lang_mismatch" and f["field"] == "outcome"]
        self.assertEqual(len(mismatches), 5)


class CompletenessOfEachCell(unittest.TestCase):
    """Требование 6: ровно пять записей 0..4 без пропусков и повторов;
    общее число строк фикстуры совпадает с объявленным в шапке."""

    def test_full_five_record_cell_is_complete(self):
        rows, _ = an.parse_fixture(fixture(CLEAN_CELL_GO))
        findings = an.find_findings(rows, {})
        self.assertEqual([f for f in findings if f["kind"] == "incomplete_cell"], [])

    def test_removing_one_record_from_a_cell_is_flagged(self):
        rows_text = [evo_row("go", "compat", "protobuf", "reuse_tag", "newer_reader", i, "wrong")
                     for i in range(5) if i != 3]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        incomplete = [f for f in findings if f["kind"] == "incomplete_cell"]
        self.assertEqual(len(incomplete), 1)
        self.assertEqual(incomplete[0]["missing"], [3])

    def test_removing_four_of_five_records_from_a_split_cell_is_flagged(self):
        rows_text = [evo_row("go", "compat", "protobuf", "retype_message", "newer_reader", 0, "wrong")]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        incomplete = [f for f in findings if f["kind"] == "incomplete_cell"]
        self.assertEqual(len(incomplete), 1)
        self.assertEqual(incomplete[0]["missing"], [1, 2, 3, 4])

    def test_duplicate_record_index_is_flagged_as_unexpected(self):
        rows_text = [evo_row("go", "compat", "avro", "retype", "newer_reader", i, "refused")
                     for i in [0, 0, 1, 2, 3]]
        rows, _ = an.parse_fixture(fixture(rows_text))
        findings = an.find_findings(rows, {})
        incomplete = [f for f in findings if f["kind"] == "incomplete_cell"]
        self.assertEqual(len(incomplete), 1)
        self.assertEqual(incomplete[0]["missing"], [4])

    def test_total_line_count_mismatch_against_header_is_flagged(self):
        rows, env = an.parse_fixture(fixture(CLEAN_CELL_GO, env=env_line(expected_lines=999)))
        findings = an.find_findings(rows, {}, env)
        mismatch = [f for f in findings if f["kind"] == "fixture_line_count_mismatch"]
        self.assertEqual(len(mismatch), 1)
        self.assertEqual(mismatch[0]["actual_lines"], 5)

    def test_matching_line_count_is_not_flagged(self):
        rows, env = an.parse_fixture(fixture(CLEAN_CELL_GO, env=env_line(expected_lines=5)))
        findings = an.find_findings(rows, {}, env)
        self.assertEqual([f for f in findings if f["kind"] == "fixture_line_count_mismatch"], [])

    def test_missing_expected_lines_in_env_does_not_crash(self):
        rows, env = an.parse_fixture(fixture(CLEAN_CELL_GO))
        findings = an.find_findings(rows, {}, env)
        self.assertEqual([f for f in findings if f["kind"] == "fixture_line_count_mismatch"], [])


class FindingsGiveNonzeroExitCode(unittest.TestCase):
    """Требование 7: любая находка — ненулевой код возврата у main()."""

    def test_main_returns_zero_on_clean_fixture(self):
        code = self._run_main(CLEAN_CELL_GO + CLEAN_CELL_JAVA, MINI_EXPECTED_NEWER_READER_ONLY)
        self.assertEqual(code, 0)

    def test_main_returns_nonzero_when_a_finding_exists(self):
        broken = uniform_cell("go", "compat", "avro", "retype", "newer_reader", "wrong",
                               want={"id": 1}, got={"id": 2})
        code = self._run_main(broken, MINI_EXPECTED)
        self.assertNotEqual(code, 0)

    def test_main_returns_zero_when_declared_divergence_matches_catalog(self):
        # Круг 6ter, сквозная проверка через main(): объявленное
        # расхождение (by_lang) не портит код возврата.
        expected = {
            "alias_conflict": {"avro": {"newer_reader": {
                "by_lang": {"go": "wrong", "java": "refused"},
                "reason": "...",
            }}},
        }
        go_rows = uniform_cell("go", "compat", "avro", "alias_conflict", "newer_reader", "wrong",
                                got={"id": 1, "name": "anna@x"})
        java_rows = uniform_cell("java", "compat", "avro", "alias_conflict", "newer_reader", "refused")
        code = self._run_main(go_rows + java_rows, expected)
        self.assertEqual(code, 0)

    @staticmethod
    def _run_main(rows_text, expected):
        import os
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False, encoding="utf-8") as f:
            f.write(fixture(rows_text))
            fixture_path = f.name
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as f:
            json.dump(expected, f)
            expected_path = f.name
        old_fixture, old_expected = an.FIXTURE, an.EXPECTED_PATH
        try:
            an.FIXTURE = an.Path(fixture_path)
            an.EXPECTED_PATH = an.Path(expected_path)
            return an.main()
        finally:
            an.FIXTURE, an.EXPECTED_PATH = old_fixture, old_expected
            os.unlink(fixture_path)
            os.unlink(expected_path)


if __name__ == "__main__":
    unittest.main()
