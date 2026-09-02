# -*- coding: utf-8 -*-
"""Предохранители разбора оси перекрёстного чтения (Задача 8).

Всё, что здесь проверяется (решение контроллера + schemas/spec.md §17):

  1. Полнота матрицы писатель/читатель (4 сочетания x 5 записей на
     клетку) и общее число строк фикстуры против шапки.
  2. Контрольные клетки (писатель == читатель) обязаны построчно
     совпасть с осью эволюции (fixtures/evolution.txt, kind=compat) —
     расхождение здесь Critical (control_mismatch), а не "другой путь
     кода".
  3. "wrong" без "got" — недоказанная находка, как и в оси эволюции.
  4. Проба идентичности недействительна без контроля (control_equal
     ложен хотя бы у одной реализации) — тогда межъязыковое сравнение
     байт не проводится вовсе, и это отдельная, более приоритетная
     находка, чем обычное несовпадение.
  5. ГЛАВНАЯ ЗАЩИТА ЭТОГО ФАЙЛА (то самое требование бланка задачи):
     отчёт main() НИКОГДА не должен утверждать безусловную гарантию —
     тест ищет слово «гарант» (в любой словоформе — гарантия,
     гарантирует, гарантированно, ...) в напечатанном отчёте и валит
     прогон, если оно там появилось. Вывод обязан различать «подтверждено
     спецификацией формата» (avro), «подтверждено на практике для этих
     двух конкретных реализаций» (protobuf) и «не предполагается вовсе»
     (json/json-schema) — три разных утверждения, которые нельзя
     обобщить одним обещающим словом.

Тесты работают на СКОНСТРУИРОВАННЫХ фикстурах, не на реальном прогоне
bench/run-cross.sh.
"""

import importlib.util
import io
import json
import unittest
from contextlib import redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_cross", ROOT / "scripts" / "analyze-cross.py")
ac = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ac)


def cross_row(writer, reader, format_, change, direction, record_index,
              outcome, bytes_=50, record=None, want=None, got=None, error=None):
    row = {
        "cell": "%s/cross-accept/%s/%s/%s/%d" % (reader, format_, change, direction, record_index),
        "kind": "cross",
        "format": format_,
        "change": change,
        "direction": direction,
        "record_index": record_index,
        "stage": "decode",
        "lang": reader,
        "outcome": outcome,
        "encoded": True,
        "bytes": bytes_,
        "writer": writer,
        "reader": reader,
    }
    if record is not None:
        row["record"] = record
    if want is not None:
        row["want"] = want
    if got is not None:
        row["got"] = got
    if error is not None:
        row["error"] = error
    return row


def full_cell(format_, change, direction, outcome="ok", **kw):
    """Полная клетка: все 4 сочетания писатель/читатель x 5 записей."""
    rows = []
    for writer in ("go", "java"):
        for reader in ("go", "java"):
            for i in range(5):
                rows.append(cross_row(writer, reader, format_, change, direction, i,
                                       outcome, record={"id": i}, want={"id": i}, got={"id": i}, **kw))
    return rows


def identity_row(lang, format_, control_equal, sha, change="base", bytes_=30):
    return {
        "kind": "identity-probe", "format": format_, "change": change, "lang": lang,
        "control_equal": control_equal, "sha256": sha, "bytes": bytes_,
    }


def evo_compat_row(lang, format_, change, direction, record_index, outcome,
                    bytes_=50, record=None, want=None, got=None):
    row = {
        "cell": "%s/compat/%s/%s/%s/%d" % (lang, format_, change, direction, record_index),
        "kind": "compat", "format": format_, "change": change, "direction": direction,
        "record_index": record_index, "stage": "decode", "lang": lang, "outcome": outcome,
        "encoded": True, "bytes": bytes_,
    }
    if record is not None:
        row["record"] = record
    if want is not None:
        row["want"] = want
    if got is not None:
        row["got"] = got
    return row


def evolution_index_from(rows):
    idx = {}
    for r in rows:
        key = (r["lang"], r["format"], r["change"], r["direction"], r["record_index"])
        idx[key] = r
    return idx


class ParseFixtureTest(unittest.TestCase):
    def test_missing_complete_marker_raises(self):
        with self.assertRaises(ac.TruncatedFixture):
            ac.parse_fixture('{"kind":"env","expected_lines":0}\n')

    def test_corrupted_line_raises(self):
        text = '{"kind":"env","expected_lines":1}\nnot-json\nCOMPLETE\n'
        with self.assertRaises(ac.TruncatedFixture):
            ac.parse_fixture(text)

    def test_parses_env_and_rows(self):
        rows_in = full_cell("avro", "add_default", "newer_reader")
        text = "\n".join(
            ['{"kind":"env","expected_lines":%d}' % len(rows_in)]
            + [json.dumps(r) for r in rows_in] + ["COMPLETE"])
        rows, env = ac.parse_fixture(text)
        self.assertEqual(len(rows), len(rows_in))
        self.assertEqual(env["expected_lines"], len(rows_in))


class CompletenessTest(unittest.TestCase):
    def test_full_cell_has_no_completeness_findings(self):
        rows = full_cell("avro", "add_default", "newer_reader")
        idx = ac._cross_index(rows)
        findings = ac._completeness_findings(idx, rows, {"expected_lines": len(rows)})
        self.assertEqual(findings, [])

    def test_missing_writer_reader_pair_detected(self):
        rows = [r for r in full_cell("avro", "rename", "newer_reader")
                if not (r["writer"] == "java" and r["reader"] == "go")]
        idx = ac._cross_index(rows)
        findings = ac._completeness_findings(idx, rows, None)
        kinds = [f["kind"] for f in findings]
        self.assertIn("missing_writer_reader_pair", kinds)

    def test_incomplete_cell_detected(self):
        rows = [r for r in full_cell("avro", "rename", "newer_reader")
                if not (r["writer"] == "go" and r["reader"] == "go" and r["record_index"] == 4)]
        idx = ac._cross_index(rows)
        findings = ac._completeness_findings(idx, rows, None)
        incomplete = [f for f in findings if f["kind"] == "incomplete_cell"]
        self.assertEqual(len(incomplete), 1)
        self.assertEqual(incomplete[0]["missing"], [4])

    def test_fixture_line_count_mismatch_detected(self):
        rows = full_cell("avro", "add_default", "newer_reader")
        idx = ac._cross_index(rows)
        findings = ac._completeness_findings(idx, rows, {"expected_lines": len(rows) + 1})
        kinds = [f["kind"] for f in findings]
        self.assertIn("fixture_line_count_mismatch", kinds)


class WrongWithoutObservationTest(unittest.TestCase):
    def test_wrong_missing_got_is_a_finding(self):
        row = cross_row("go", "java", "protobuf", "retype", "newer_reader", 0, "wrong",
                         record={"id": 1}, want={"id": 1})
        findings = ac._wrong_without_observation_findings([row])
        self.assertEqual(len(findings), 1)

    def test_wrong_with_falsy_but_present_got_is_not_a_finding(self):
        row = cross_row("go", "java", "protobuf", "retype", "newer_reader", 0, "wrong",
                         record={"id": 1}, want={"id": 1}, got={})
        findings = ac._wrong_without_observation_findings([row])
        self.assertEqual(findings, [])


class ControlFindingsTest(unittest.TestCase):
    """Требование 2: контроль (writer == reader) обязан построчно
    совпасть с осью эволюции. Это САМАЯ ОТВЕТСТВЕННАЯ проверка этой
    оси — она ловит порчу, которую внёс именно файловый обмен."""

    def test_matching_control_cell_has_no_findings(self):
        cross_rows = [r for r in full_cell("avro", "add_default", "newer_reader")
                      if r["writer"] == r["reader"]]
        evo_rows = [evo_compat_row(r["writer"], r["format"], r["change"], r["direction"],
                                    r["record_index"], r["outcome"],
                                    bytes_=r["bytes"], record=r["record"], want=r["want"], got=r["got"])
                    for r in cross_rows]
        idx = ac._cross_index(cross_rows)
        findings = ac._control_findings(idx, evolution_index_from(evo_rows))
        self.assertEqual(findings, [])

    def test_control_outcome_mismatch_is_critical(self):
        cross_rows = full_cell("avro", "rename", "newer_reader", outcome="wrong")
        # Ось эволюции для контрольной клетки go/go говорит "ok" — файловый
        # обмен явно что-то испортил.
        evo_rows = [evo_compat_row("go", "avro", "rename", "newer_reader", i, "ok",
                                    record={"id": i}, want={"id": i}, got={"id": i})
                    for i in range(5)]
        evo_rows += [evo_compat_row("java", "avro", "rename", "newer_reader", i, "wrong",
                                     record={"id": i}, want={"id": i}, got={"id": i})
                     for i in range(5)]
        idx = ac._cross_index(cross_rows)
        findings = ac._control_findings(idx, evolution_index_from(evo_rows))
        mismatches = [f for f in findings if f["kind"] == "control_mismatch" and f["lang"] == "go"]
        self.assertTrue(mismatches, "расхождение go-контроля с осью эволюции обязано найтись")

    def test_missing_evolution_fixture_is_an_explicit_finding(self):
        cross_rows = full_cell("avro", "add_default", "newer_reader")
        idx = ac._cross_index(cross_rows)
        findings = ac._control_findings(idx, None)
        self.assertEqual([f["kind"] for f in findings], ["evolution_fixture_missing"])


class IdentitySummaryTest(unittest.TestCase):
    def test_control_green_and_hashes_equal_gives_cross_equal_true(self):
        rows = [identity_row("go", "avro", True, "aaaa"), identity_row("java", "avro", True, "aaaa")]
        summaries, findings = ac.identity_summary(rows)
        self.assertEqual(findings, [])
        self.assertEqual(len(summaries), 1)
        self.assertTrue(summaries[0]["control_equal"])
        self.assertTrue(summaries[0]["cross_equal"])

    def test_control_green_and_hashes_differ_gives_cross_equal_false(self):
        rows = [identity_row("go", "json", True, "aaaa"), identity_row("java", "json", True, "bbbb")]
        summaries, findings = ac.identity_summary(rows)
        self.assertEqual(findings, [])
        self.assertFalse(summaries[0]["cross_equal"])

    def test_control_red_for_either_lang_makes_proba_invalid(self):
        rows = [identity_row("go", "protobuf", False, "aaaa"), identity_row("java", "protobuf", True, "aaaa")]
        summaries, findings = ac.identity_summary(rows)
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0]["kind"], "identity_control_invalid")
        # Проба недействительна: cross_equal НЕ утверждается, даже если
        # дайджесты формально совпали, — сравнивать было решено рано.
        self.assertIsNone(summaries[0]["cross_equal"])
        self.assertFalse(summaries[0]["control_equal"])

    def test_missing_language_row_is_a_finding(self):
        rows = [identity_row("go", "avro", True, "aaaa")]
        summaries, findings = ac.identity_summary(rows)
        self.assertEqual(summaries, [])
        self.assertEqual(findings[0]["kind"], "identity_missing_lang")


class ReportNeverOverpromisesTest(unittest.TestCase):
    """Требование бланка задачи: отчёт не должен использовать слово со
    значением безусловного обещания («гарант...») — вывод обязан
    различать «подтверждено спецификацией» (avro), «подтверждено на
    практике для этих реализаций» (protobuf) и «не предполагается
    вовсе» (json/json-schema)."""

    def _build_fixture(self, tmp_path):
        cross_rows = []
        for format_, change in (("avro", "add_default"), ("protobuf", "unknown_field")):
            cross_rows += full_cell(format_, change, "newer_reader")
        identity_rows = []
        for format_, sha in (("avro", "aaaa"), ("protobuf", "bbbb")):
            identity_rows.append(identity_row("go", format_, True, sha))
            identity_rows.append(identity_row("java", format_, True, sha))
        for format_, sha_go, sha_java in (("json", "cccc", "dddd"), ("json-schema", "cccc", "dddd")):
            identity_rows.append(identity_row("go", format_, True, sha_go))
            identity_rows.append(identity_row("java", format_, True, sha_java))

        rows = cross_rows + identity_rows
        text = "\n".join(
            ['{"kind":"env","expected_lines":%d}' % len(rows)]
            + [json.dumps(r) for r in rows] + ["COMPLETE"])
        cross_path = tmp_path / "cross.txt"
        cross_path.write_text(text, encoding="utf-8")

        evo_rows = [evo_compat_row(r["writer"], r["format"], r["change"], r["direction"],
                                    r["record_index"], r["outcome"],
                                    bytes_=r["bytes"], record=r["record"], want=r["want"], got=r["got"])
                    for r in cross_rows if r["writer"] == r["reader"]]
        evo_text = "\n".join(
            ['{"kind":"env","expected_lines":%d}' % len(evo_rows)]
            + [json.dumps(r) for r in evo_rows] + ["COMPLETE"])
        evo_path = tmp_path / "evolution.txt"
        evo_path.write_text(evo_text, encoding="utf-8")
        return cross_path, evo_path

    def test_main_report_contains_no_unconditional_guarantee_word(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            cross_path, evo_path = self._build_fixture(tmp_path)

            old_fixture, old_evo = ac.FIXTURE, ac.EVOLUTION_FIXTURE
            ac.FIXTURE, ac.EVOLUTION_FIXTURE = cross_path, evo_path
            try:
                buf = io.StringIO()
                with redirect_stdout(buf):
                    ac.main()
            finally:
                ac.FIXTURE, ac.EVOLUTION_FIXTURE = old_fixture, old_evo

            report = buf.getvalue()
            self.assertNotIn("гарант", report,
                              "отчёт обещает безусловно то, что измерено только частично")
            # Позитивный контроль: отчёт действительно СОДЕРЖИТ три разных
            # формулировки, а не просто избегает слова целиком.
            self.assertIn("спецификации", report)
            self.assertIn("практике", report)
            self.assertIn("не определён", report)


if __name__ == "__main__":
    unittest.main()
