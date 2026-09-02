# -*- coding: utf-8 -*-
"""README против фикстур: каждое число паспорта обязано найтись в снятых данных.

ЗАЧЕМ. README читают чаще, чем `fixtures/*.txt`, и числа в нём набраны
руками по памяти о прошлом прогоне. Фикстуры при этом переснимаются
(новый образ реестра, новая версия библиотеки, перезамер после правки
пробы) — и устаревший паспорт выглядит ровно как свежий: числа не
подают виду, что расползлись с тем, что стенд измерил В ПОСЛЕДНИЙ РАЗ.
Тот же класс болезни уже ловился в python-стендах этого репозитория
(`python/*/scripts/test_readme_matches_fixtures.py`) — здесь тот же
принцип на четырёх осях serialization-formats.

ЧТО ИМЕННО ПРОВЕРЯЕТСЯ. Не «похоже», а точное совпадение с фикстурой:
каждое число, напечатанное в README (таблица размера, таблица версий,
счётчики строк по осям, конкретные исходы находок), обязано БУКВАЛЬНО
встречаться в соответствующей фикстуре — либо как значение поля
записанной строки, либо как факт, вычисленный из данных фикстуры тем же
способом, каким его вычислял бы человек, сверяющий README руками.

Особый вес — у находок, которые одиннадцать раз за эту работу
предъявлялись как свойство формата, хотя были поведением стенда: если
здесь появится расхождение, значит README успел обобщить наблюдение
шире того, что фикстура фактически показывает.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
README_TEXT = README.read_text(encoding="utf-8")
# Markdown переносит длинную фразу на несколько строк ради читаемого
# исходника; для человека это одна фраза, для буквального assertIn -- нет.
# README_FLAT схлопывает переносы строк и пробелы для проверки ПРОЗЫ (не
# используется там, где важна структура таблицы или блока кода).
README_FLAT = re.sub(r"\s+", " ", README_TEXT)


def fixture(name):
    """Строки ДАННЫХ фикстуры: без комментариев, без COMPLETE и без env."""
    path = ROOT / "fixtures" / name
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or line == "COMPLETE":
            continue
        row = json.loads(line)
        if row.get("kind") == "env":
            continue
        rows.append(row)
    return rows


def env_of_raw(name):
    """Строка kind=env -- отдельно от данных, у неё другая форма."""
    path = ROOT / "fixtures" / name
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or line == "COMPLETE":
            continue
        row = json.loads(line)
        if row.get("kind") == "env":
            return row
    raise AssertionError(f"в {name} нет строки kind=env")


def block_after(heading, text=README_TEXT):
    """Первый блок кода/таблицы после заголовка — строки таблицы Markdown.

    Возвращает строки, начинающиеся с "|", идущие сразу за заголовком (до
    первой строки, которая с "|" не начинается), пропуская строку-разделитель
    ("|---|---|").
    """
    start = text.index(heading)
    rest = text[start:].splitlines()
    lines = []
    started = False
    for line in rest:
        stripped = line.strip()
        if stripped.startswith("|"):
            started = True
            if set(stripped.replace("|", "").strip()) <= {"-", " "}:
                continue
            lines.append(stripped)
        elif started:
            break
    return lines


def cells(line):
    return [c.strip() for c in line.strip("|").split("|")]


def strip_backticks(s):
    return s.strip("`")


class SizeTable(unittest.TestCase):
    """Таблица record 0 (§10.3): bytes/zstd/schema_bytes/schema_file_bytes/batch_bytes."""

    def setUp(self):
        self.rows = fixture("size.txt")
        self.by_format = {
            r["format"]: r for r in self.rows
            if r.get("lang") == "go" and r.get("record_index") == 0
        }

    def test_size_table_matches_fixture_record_zero(self):
        lines = block_after("### `fixtures/size.txt`")
        # первая строка блока — шапка таблицы, пропускаем её
        checked = 0
        for line in lines[1:]:
            parts = cells(line)
            fmt_raw = strip_backticks(parts[0].split(" ")[0])
            self.assertIn(fmt_raw, self.by_format, parts[0])
            row = self.by_format[fmt_raw]
            printed_bytes, _zstd, printed_schema, printed_schema_file, printed_batch = (
                int(parts[1]), int(parts[2]), int(parts[3]), int(parts[4]), int(parts[5]))
            self.assertEqual(printed_bytes, row["bytes"], fmt_raw)
            self.assertEqual(printed_schema, row["schema_bytes"], fmt_raw)
            self.assertEqual(printed_schema_file, row["schema_file_bytes"], fmt_raw)
            self.assertEqual(printed_batch, row["batch_bytes"], fmt_raw)
            checked += 1
        self.assertEqual(checked, 4)

    def test_content_hash_literal_matches_all_eight_rows(self):
        """Хеш содержимого пачки, процитированный в README, обязан быть
        РОВНО тем значением, что несут ВСЕ восемь строк (4 плеча x 2 языка) —
        а не значением одной случайно выбранной строки."""
        m = re.search(r"`([0-9a-f]{64})`", README_TEXT)
        self.assertIsNotNone(m, "в README не найден 64-символьный хеш в code span")
        quoted = m.group(1)
        hashes = {r["batch_content_hash"] for r in self.rows}
        self.assertEqual(hashes, {quoted},
                          "batch_content_hash в README не совпадает со всеми строками фикстуры")

    def test_batch_hash_divergence_claim(self):
        """avro/protobuf совпадают между Go и Java по batch_hash, json/json-schema — нет."""
        by = {}
        for r in self.rows:
            by.setdefault((r["format"], r["lang"]), r["batch_hash"])
        for fmt in ("avro", "protobuf"):
            self.assertEqual(by[(fmt, "go")], by[(fmt, "java")], fmt)
        for fmt in ("json", "json-schema"):
            self.assertNotEqual(by[(fmt, "go")], by[(fmt, "java")], fmt)


class VersionsTable(unittest.TestCase):

    def setUp(self):
        self.size_env = env_of_raw("size.txt")
        self.need_env = env_of_raw("need.txt")

    def test_every_printed_version_is_in_the_fixture_env(self):
        lines = block_after("| Компонент | Версия |")
        expected = {
            "go1.27.0": self.size_env["go"]["go_version"],
            "v1.19.2": self.size_env["go"]["klauspost_compress_version"],
            "maven:3.9-eclipse-temurin-25": self.size_env["java"]["image"],
            "1.12.2": self.size_env["java"]["avro_version"],
            "4.36.0": self.size_env["java"]["protobuf_version"],
            "3.0.7": self.size_env["java"]["json_schema_validator_version"],
            "1.5.7-15": self.size_env["java"]["zstd_jni_version"],
            "3.3.1": self.need_env["registry"]["version"],
        }
        checked = 0
        for printed, actual in expected.items():
            self.assertIn(printed, README_TEXT, printed)
            self.assertEqual(printed, actual, printed)
            checked += 1
        self.assertEqual(checked, len(expected))
        # ровно то, что реально стоит в таблице -- не только "где-то в тексте"
        table_text = "\n".join(lines)
        for printed in expected:
            self.assertIn(printed, table_text, printed)

    def test_zstd_level_matches(self):
        self.assertEqual(self.size_env["zstd_level"], 3)
        self.assertIn("Уровень сжатия zstd", README_TEXT)

    def test_registry_image_tag_is_pinned_not_floating(self):
        """Тег образа реестра обязан быть версией, а не 'latest'.

        Та же ловушка, что с версией брокера в другом стенде: плавающий
        тег проверяет версию ПОСЛЕ того, как она уже скачалась. Тег
        обязан совпадать с версией, объявленной в этой же строке env, —
        иначе пиновка формальна (тег есть, но не тот, что реально
        измерен).
        """
        image = self.need_env["registry"]["image"]
        version = self.need_env["registry"]["version"]
        self.assertNotIn(":latest", image, image)
        self.assertTrue(image.endswith(f":{version}"),
                        f"тег образа {image} не совпадает с измеренной версией {version}")
        self.assertIn(image, README_TEXT)
        self.assertIn("Образ запинен тегом", README_FLAT)


class EvolutionCounts(unittest.TestCase):

    def setUp(self):
        self.data = fixture("evolution.txt")
        self.env = env_of_raw("evolution.txt")

    def test_line_count_claim_matches_fixture(self):
        m = re.search(r"=\s*\*\*(\d+)\s*строк\*\*\s*данных", README_TEXT)
        self.assertIsNotNone(m, "не найдено объявление числа строк оси эволюции")
        printed = int(m.group(1))
        self.assertEqual(printed, self.env["expected_lines"])
        self.assertEqual(printed, len(self.data))

    def test_alias_conflict_wrong_vs_refused_by_lang(self):
        """Go даёт wrong на всех 5, Java -- refused на всех 5 (§6.2, §15.1).

        Только направление newer_reader: на newer_writer оба языка
        отказывают одинаково (проверено отдельно ниже) -- смешение
        направлений превратило бы находку в артефакт агрегации.
        """
        cell = [r for r in self.data
                if r["kind"] == "compat" and r["format"] == "avro"
                and r["change"] == "alias_conflict"
                and r["direction"] == "newer_reader"]
        go_outcomes = {r["outcome"] for r in cell if r["lang"] == "go"}
        java_outcomes = {r["outcome"] for r in cell if r["lang"] == "java"}
        go_count = sum(1 for r in cell if r["lang"] == "go")
        java_count = sum(1 for r in cell if r["lang"] == "java")
        self.assertEqual(go_outcomes, {"wrong"})
        self.assertEqual(java_outcomes, {"refused"})
        self.assertEqual(go_count, 5)
        self.assertEqual(java_count, 5)
        # числа из README-абзаца про эту находку
        self.assertIn("`wrong` на всех 5 записях", README_FLAT)
        self.assertIn("`refused` на всех 5 записях", README_FLAT)

    def test_retype_message_split_is_not_uniform(self):
        """protobuf/retype_message: клетка расщепляется -- не все 5 записей
        дают один и тот же исход (§15.2, §3.5)."""
        cell = [r for r in self.data
                if r["kind"] == "compat" and r["format"] == "protobuf"
                and r["change"] == "retype_message"
                and r["direction"] == "newer_reader"]
        for lang in ("go", "java"):
            outcomes = [r["outcome"] for r in cell if r["lang"] == lang]
            self.assertEqual(len(outcomes), 5, lang)
            self.assertIn("wrong", outcomes, lang)
            self.assertIn("refused", outcomes, lang)
            self.assertGreater(outcomes.count("refused"), outcomes.count("wrong"), lang)


class CrossCounts(unittest.TestCase):

    def setUp(self):
        self.rows = fixture("cross.txt")
        self.env = env_of_raw("cross.txt")
        self.cross = [r for r in self.rows if r.get("kind") == "cross"]
        self.identity = [r for r in self.rows if r.get("kind") == "identity-probe"]

    def test_total_line_count_matches(self):
        m = re.search(r"Итого\s+(\d+)\s+строк\s+данных", README_TEXT)
        self.assertIsNotNone(m)
        printed = int(m.group(1))
        self.assertEqual(printed, self.env["expected_lines"])
        self.assertEqual(printed, len(self.cross) + len(self.identity))

    def test_identity_probe_row_count_and_control_equal(self):
        m = re.search(r"×\s*2\s*языка\s*=\s*(\d+)\s*строк", README_TEXT)
        self.assertIsNotNone(m)
        self.assertEqual(int(m.group(1)), len(self.identity))
        self.assertEqual(len(self.identity), 8)
        self.assertTrue(all(r["control_equal"] for r in self.identity))

    def test_alias_conflict_outcome_depends_on_reader_not_writer(self):
        """Перекрёстная проба, направление newer_reader: исход зависит от
        reader, не от writer (то же направление, что и в README-абзаце и в
        EvolutionCounts.test_alias_conflict_wrong_vs_refused_by_lang --
        newer_writer даёт другую, но тоже writer-независимую картину, и
        подмешивать её сюда значило бы проверять не то, что написано)."""
        rows = [r for r in self.cross if r["format"] == "avro"
                and r["change"] == "alias_conflict"
                and r["direction"] == "newer_reader"]
        self.assertTrue(rows)
        by_reader = {}
        for r in rows:
            by_reader.setdefault(r["reader"], set()).add(r["outcome"])
        self.assertEqual(by_reader["go"], {"wrong"})
        self.assertEqual(by_reader["java"], {"refused"})
        # оба writer'а встретились для каждого reader -- иначе утверждение
        # "независимо от того, кто писал" не проверено, а предположено
        writers_for_go_reader = {r["writer"] for r in rows if r["reader"] == "go"}
        writers_for_java_reader = {r["writer"] for r in rows if r["reader"] == "java"}
        self.assertEqual(writers_for_go_reader, {"go", "java"})
        self.assertEqual(writers_for_java_reader, {"go", "java"})
        self.assertIn("независимо от того, какая из двух реализаций записала байты",
                       README_FLAT)


class NeedAxisFacts(unittest.TestCase):

    def setUp(self):
        self.rows = fixture("need.txt")

    def test_alias_conflict_registry_verdict(self):
        row = next(r for r in self.rows
                   if r.get("kind") == "registry_matrix"
                   and r.get("change") == "alias_conflict")
        self.assertEqual(row["http_status"], 422)
        self.assertEqual(row["registry_verdict"], "schema_error")
        self.assertIn("`http_status=422`", README_TEXT)
        self.assertIn("`registry_verdict=schema_error`", README_TEXT)

    def test_registry_calls_up_vs_down(self):
        up = next(r for r in self.rows
                  if r.get("kind") == "need" and r.get("leg") == "registry_up")
        down = next(r for r in self.rows
                    if r.get("kind") == "need" and r.get("leg") == "registry_down")
        local = next(r for r in self.rows
                     if r.get("kind") == "need" and r.get("leg") == "schema_local")
        self.assertEqual(up["registry_calls"], 1)
        self.assertEqual(down["registry_calls"], 1)
        self.assertEqual(local["registry_calls"], 0)
        self.assertEqual(up["outcome"], "ok")
        self.assertEqual(down["outcome"], "unavailable")

    def test_envelope_prefix_length(self):
        row = next(r for r in self.rows if r.get("kind") == "envelope")
        self.assertEqual(row["prefix_len"], 5)
        self.assertEqual(row["outcome"], "wrong")
        self.assertIn("5-байтовый идентификатор схемы", README_FLAT)


class LimitationsSectionIsComplete(unittest.TestCase):
    """Три обязательных ограничения метода -- ни одно не может отсутствовать
    или быть смягчено до общей оговорки."""

    def _section(self):
        flat_from_heading = README_FLAT[README_FLAT.index("## Ограничения метода"):]
        return flat_from_heading

    def test_three_limitations_named_explicitly(self):
        section = self._section()
        self.assertIn("пересказывает правила разрешения схем самого Avro", section)
        self.assertIn("свойство формата, данных И БИБЛИОТЕКИ СЖАТИЯ", section)
        self.assertIn("разного происхождения для разных плеч", section)

    def test_registry_version_caveat_present(self):
        section = self._section()
        self.assertIn("3.3.1", section)
        self.assertIn("Тройное расхождение", section)


class SanityOfTheCheckItself(unittest.TestCase):
    """Доказательство, что проверка ловит расхождение, а не всегда зелёная.

    Тот же принцип, что в python-стендах этого репозитория
    (test_detects_a_wrong_number): подсовываем заведомо неверное число на
    месте настоящего README-текста и убеждаемся, что сравнение с фикстурой
    его отвергает.
    """

    def test_detects_a_wrong_line_count(self):
        env = env_of_raw("evolution.txt")
        real = env["expected_lines"]
        pattern = re.compile(r"(=\s*\*\*)(\d+)(\s*строк\*\*\s*данных)")
        self.assertRegex(README_TEXT, pattern)
        fake_text = pattern.sub(lambda m: f"{m.group(1)}{real + 1}{m.group(3)}",
                                 README_TEXT, count=1)
        m = pattern.search(fake_text)
        printed = int(m.group(2))
        self.assertNotEqual(printed, real)  # подмена действительно состоялась
        with self.assertRaises(AssertionError):
            self.assertEqual(printed, real)

    def test_detects_a_wrong_hash(self):
        rows = fixture("size.txt")
        real_hashes = {r["batch_content_hash"] for r in rows}
        fake = "0" * 64
        with self.assertRaises(AssertionError):
            self.assertEqual(real_hashes, {fake})


if __name__ == "__main__":
    unittest.main(verbosity=2)
