# -*- coding: utf-8 -*-
"""Предохранители разбора оси 3 (Задача 7): что нужно ИМЕТЬ, чтобы
прочитать запись, и конверт реестра схем.

Симметрично python-стенду и остальным осям этого стенда: отсутствие
схемы обязано быть ДОКАЗАНО ДАННЫМИ, а не предположено. Разбор здесь
проверяет ровно то, что перечислено в task-7-brief.md («РЕШЕНИЕ
КОНТРОЛЛЕРА»):

  1. НЕДЕЙСТВИТЕЛЬНОСТЬ ПРОБЫ — самая важная проверка файла. Строка
     kind=need с leg=registry_down обязана иметь schema_available=false
     и НЕ иметь outcome "ok"/"wrong". Если она всё-таки прочиталась —
     это значит, что схема пришла ОТКУДА-ТО ЕЩЁ (кэш, вшитый файл), и
     разбор ОБЯЗАН объявить пробу негодной (invalid_probe) — самой
     громкой, приоритетной находкой, а не тихо написать "схема не
     нужна".
  2. Счётчик обращений к реестру (требование 4): leg=registry_up несёт
     registry_calls; проверяется, что оно ПРИСУТСТВУЕТ и что оно не
     нулевое (реестр обязан был быть спрошен).
  3. "wrong" без "got" — недоказанная находка (тот же принцип, что и в
     оси эволюции).
  4. Сравнение матрицы реестра (kind=registry_matrix) с колонкой
     avro/newer_reader матрицы эволюции (schemas/expected.json) —
     ТРЕБОВАНИЕ 1 брифа. Расхождение печатается как находка, но ЯВНО
     помечается отдельным видом (registry_matrix_divergence), чтобы не
     перепутать его со сбоем пробы: расхождение реестра с матрицей —
     содержание оси, а не дефект.
  5. Полнота: 9 изменений в registry_matrix без пропусков и повторов;
     обязательный набор строк kind=need (формат x плечо) весь присутствует.
  6. Оборванная или повреждённая посередине фикстура — TruncatedFixture.
  7. Любая находка — ненулевой код возврата main().
"""

import importlib.util
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_need", ROOT / "scripts" / "analyze-need.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)


ENV_LINE = '{"kind":"env","go":{"go_version":"go1.27.0"},"registry":{"image":"apicurio/apicurio-registry:latest","version":"3.3.1","port":18094}}'


def matrix_row(change, verdict="accepted", status=200):
    return json.dumps({
        "kind": "registry_matrix", "change": change, "format": "avro",
        "http_status": status, "registry_verdict": verdict, "detail": "",
    })


def need_row(fmt, leg, schema_available, outcome, registry_calls=0, got=None, want=None, error=None):
    row = {
        "kind": "need", "format": fmt, "leg": leg,
        "schema_available": schema_available, "outcome": outcome,
        "registry_calls": registry_calls,
    }
    if got is not None:
        row["got"] = got
    if want is not None:
        row["want"] = want
    if error is not None:
        row["error"] = error
    return json.dumps(row)


def envelope_row(outcome, prefix_len=5, got=None, want=None):
    row = {"kind": "envelope", "decoder": "naive", "outcome": outcome, "prefix_len": prefix_len}
    if got is not None:
        row["got"] = got
    if want is not None:
        row["want"] = want
    return json.dumps(row)


ALL_CHANGES = ["add_default", "add_nodefault", "remove", "rename", "retype",
               "reuse_tag", "unknown_field", "alias_conflict", "retype_message"]

REQUIRED_NEED_ROWS = [
    need_row("avro", "registry_up", True, "ok", registry_calls=1,
             got={"id": 1, "name": "A", "email": "a@x"}, want={"id": 1, "name": "A", "email": "a@x"}),
    need_row("avro", "schema_local", True, "ok", registry_calls=0,
             got={"id": 1, "name": "A", "email": "a@x"}, want={"id": 1, "name": "A", "email": "a@x"}),
    need_row("avro", "registry_down", False, "unavailable", registry_calls=1,
             want={"id": 1, "name": "A", "email": "a@x"}, error="connection refused"),
    need_row("protobuf", "schema_local", True, "ok", registry_calls=0,
             got={"id": 1}, want={"id": 1}),
    need_row("protobuf", "no_schema", False, "unavailable", error="нет дескриптора"),
    need_row("json", "no_schema", False, "ok", got={"id": 1}, want={"id": 1}),
    need_row("json-schema", "no_schema", False, "ok", got={"id": 1}, want={"id": 1}),
]


def build_fixture(matrix_rows=None, need_rows=None, env_rows=None, complete=True):
    if matrix_rows is None:
        matrix_rows = [matrix_row(c) for c in ALL_CHANGES]
    if need_rows is None:
        need_rows = list(REQUIRED_NEED_ROWS)
    if env_rows is None:
        env_rows = [envelope_row("wrong", got={"id": 0}, want={"id": 1})]
    lines = ["# комментарий", ENV_LINE] + matrix_rows + need_rows + env_rows
    if complete:
        lines.append("COMPLETE")
    return "\n".join(lines) + "\n"


MINIMAL_EXPECTED_EVOLUTION = {
    change: {"avro": {"newer_reader": "ok", "newer_writer": "ok"}}
    for change in ALL_CHANGES
}
# Реалистичные значения там, где они важны для тестов ниже.
MINIMAL_EXPECTED_EVOLUTION["add_nodefault"]["avro"]["newer_reader"] = "refused"
MINIMAL_EXPECTED_EVOLUTION["retype"]["avro"]["newer_reader"] = "refused"


class ParseFixtureTests(unittest.TestCase):
    def test_parses_all_kinds(self):
        rows, env = an.parse_fixture(build_fixture())
        self.assertEqual(env["registry"]["port"], 18094)
        kinds = {r["kind"] for r in rows}
        self.assertEqual(kinds, {"registry_matrix", "need", "envelope"})

    def test_truncated_without_complete_marker(self):
        text = build_fixture(complete=False)
        with self.assertRaises(an.TruncatedFixture):
            an.parse_fixture(text)

    def test_truncated_on_malformed_json_line(self):
        text = build_fixture()
        text = text.replace(matrix_row("remove"), "{не json")
        with self.assertRaises(an.TruncatedFixture):
            an.parse_fixture(text)


class InvalidProbeTests(unittest.TestCase):
    """Требование 2 брифа — САМАЯ важная проверка: если при погашенном
    реестре чтение всё-таки состоялось, проба недействительна."""

    def test_registry_down_that_actually_succeeded_is_invalid(self):
        bad_rows = list(REQUIRED_NEED_ROWS)
        bad_rows[2] = need_row("avro", "registry_down", True, "ok", registry_calls=1,
                                got={"id": 1, "name": "A", "email": "a@x"},
                                want={"id": 1, "name": "A", "email": "a@x"})
        rows, env = an.parse_fixture(build_fixture(need_rows=bad_rows))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        kinds = {f["kind"] for f in findings}
        self.assertIn("invalid_probe", kinds,
                       "погашенный реестр всё равно дал чтение — обязана быть "
                       "самая громкая находка invalid_probe, а не тихое ok")

    def test_registry_down_reported_as_unavailable_is_fine(self):
        rows, env = an.parse_fixture(build_fixture())
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        kinds = {f["kind"] for f in findings}
        self.assertNotIn("invalid_probe", kinds)

    def test_registry_down_with_schema_available_true_flagged_even_without_ok(self):
        # schema_available=true само по себе противоречиво для этого
        # плеча, даже если outcome почему-то не ok/wrong.
        bad_rows = list(REQUIRED_NEED_ROWS)
        bad_rows[2] = need_row("avro", "registry_down", True, "unavailable", registry_calls=1,
                                want={"id": 1})
        rows, _ = an.parse_fixture(build_fixture(need_rows=bad_rows))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        kinds = {f["kind"] for f in findings}
        self.assertIn("invalid_probe", kinds)


class WrongWithoutObservationTests(unittest.TestCase):
    def test_wrong_need_row_without_got_is_flagged(self):
        bad_rows = list(REQUIRED_NEED_ROWS)
        bad_rows[0] = need_row("avro", "registry_up", True, "wrong", registry_calls=1,
                                want={"id": 1})  # нет "got"
        rows, _ = an.parse_fixture(build_fixture(need_rows=bad_rows))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "wrong_without_observation" for f in findings))

    def test_wrong_envelope_row_without_got_is_flagged(self):
        rows, _ = an.parse_fixture(build_fixture(env_rows=[envelope_row("wrong", want={"id": 1})]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "wrong_without_observation" for f in findings))

    def test_wrong_with_got_present_is_not_flagged(self):
        rows, _ = an.parse_fixture(build_fixture())
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertFalse(any(f["kind"] == "wrong_without_observation" for f in findings))


class RegistryCallsTests(unittest.TestCase):
    """Требование 4: сколько обращений к реестру нужно до первого чтения
    при холодном кэше — величина обязана присутствовать и быть НЕ нулём
    там, где чтение реально шло через реестр."""

    def test_registry_up_with_zero_calls_is_flagged(self):
        bad_rows = list(REQUIRED_NEED_ROWS)
        bad_rows[0] = need_row("avro", "registry_up", True, "ok", registry_calls=0,
                                got={"id": 1}, want={"id": 1})
        rows, _ = an.parse_fixture(build_fixture(need_rows=bad_rows))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "registry_calls_missing" for f in findings))

    def test_registry_up_with_nonzero_calls_is_fine(self):
        rows, _ = an.parse_fixture(build_fixture())
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertFalse(any(f["kind"] == "registry_calls_missing" for f in findings))


class CompletenessTests(unittest.TestCase):
    def test_missing_change_in_matrix_is_flagged(self):
        rows, _ = an.parse_fixture(build_fixture(
            matrix_rows=[matrix_row(c) for c in ALL_CHANGES if c != "retype"]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "matrix_incomplete" for f in findings))

    def test_duplicate_change_in_matrix_is_flagged(self):
        rows, _ = an.parse_fixture(build_fixture(
            matrix_rows=[matrix_row(c) for c in ALL_CHANGES] + [matrix_row("retype")]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "matrix_incomplete" for f in findings))

    def test_missing_required_need_row_is_flagged(self):
        rows, _ = an.parse_fixture(build_fixture(need_rows=REQUIRED_NEED_ROWS[:-1]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertTrue(any(f["kind"] == "need_row_missing" for f in findings))

    def test_all_required_rows_present_no_completeness_finding(self):
        rows, _ = an.parse_fixture(build_fixture())
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        self.assertFalse(any(f["kind"] in ("need_row_missing", "matrix_incomplete") for f in findings))


class RegistryMatrixComparisonTests(unittest.TestCase):
    """Требование 1: сравнение вердикта реестра с той же клеткой матрицы
    эволюции (avro/<change>/newer_reader). Расхождение — находка, но
    отдельного, не тревожного вида: контроллер прямо предупредил не
    считать это дефектом."""

    def test_matching_verdict_accepted_vs_ok_no_divergence(self):
        rows, _ = an.parse_fixture(build_fixture(
            matrix_rows=[matrix_row("add_default", "accepted", 200)]
            + [matrix_row(c) for c in ALL_CHANGES if c != "add_default"]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        div = [f for f in findings if f["kind"] == "registry_matrix_divergence" and f["change"] == "add_default"]
        self.assertEqual(div, [])

    def test_matching_verdict_rejected_vs_refused_no_divergence(self):
        rows, _ = an.parse_fixture(build_fixture(
            matrix_rows=[matrix_row("add_nodefault", "rejected", 400)]
            + [matrix_row(c) for c in ALL_CHANGES if c != "add_nodefault"]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        div = [f for f in findings if f["kind"] == "registry_matrix_divergence" and f["change"] == "add_nodefault"]
        self.assertEqual(div, [])

    def test_divergent_verdict_is_flagged_but_distinctly(self):
        # Матрица говорит "refused" (реестр обязан отвергнуть), а реестр
        # неожиданно принял — расхождение, а не совпадение.
        rows, _ = an.parse_fixture(build_fixture(
            matrix_rows=[matrix_row("retype", "accepted", 200)]
            + [matrix_row(c) for c in ALL_CHANGES if c != "retype"]))
        findings = an.find_findings(rows, MINIMAL_EXPECTED_EVOLUTION)
        div = [f for f in findings if f["kind"] == "registry_matrix_divergence" and f["change"] == "retype"]
        self.assertEqual(len(div), 1)

    def test_degenerate_matrix_cell_not_compared(self):
        # reuse_tag у Avro — вырожденная пара (n/a в матрице эволюции):
        # сравнивать реестру не с чем, и это НЕ находка.
        expected = dict(MINIMAL_EXPECTED_EVOLUTION)
        expected["reuse_tag"] = {"avro": {"newer_reader": "n/a", "newer_writer": "n/a"}}
        rows, _ = an.parse_fixture(build_fixture())
        findings = an.find_findings(rows, expected)
        self.assertFalse(any(f["kind"] == "registry_matrix_divergence" and f["change"] == "reuse_tag"
                              for f in findings))


def _matrix_rows_matching(expected):
    """Строит matrix_rows, вердикт которых СОГЛАСОВАН с
    MINIMAL_EXPECTED_EVOLUTION (accepted там, где матрица "ok", rejected
    там, где "refused") — иначе тест "чистой" фикстуры сам заводил бы
    законную находку registry_matrix_divergence и не проверял бы то, что
    должен."""
    rows = []
    for change in ALL_CHANGES:
        outcome = expected[change]["avro"]["newer_reader"]
        if outcome == "refused":
            rows.append(matrix_row(change, "rejected", 400))
        else:
            rows.append(matrix_row(change, "accepted", 200))
    return rows


class ExitCodeTests(unittest.TestCase):
    def test_main_returns_zero_on_clean_fixture(self):
        clean = build_fixture(matrix_rows=_matrix_rows_matching(MINIMAL_EXPECTED_EVOLUTION))
        code = an.analyze(clean, MINIMAL_EXPECTED_EVOLUTION)
        self.assertEqual(code, 0)

    def test_main_returns_nonzero_on_any_finding(self):
        bad_rows = list(REQUIRED_NEED_ROWS)
        bad_rows[2] = need_row("avro", "registry_down", True, "ok", registry_calls=1,
                                got={"id": 1}, want={"id": 1})
        code = an.analyze(build_fixture(need_rows=bad_rows), MINIMAL_EXPECTED_EVOLUTION)
        self.assertNotEqual(code, 0)


if __name__ == "__main__":
    unittest.main()
