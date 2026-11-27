import json, unittest
from pathlib import Path
from contract import Contract
ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Contract(outcomes=("match", "mismatch"), success="match", fields=("kind", "variant", "probe", "outcome", "detail", "cmd", "actual"))
LANGS = {"go", "rust", "java", "python", "node", "csharp"}
def rows():
    return {r["variant"]: r for r in CONTRACT.read_fixture(ROOT/"fixtures/checks.jsonl")}
def actual(key):
    return rows()[key]["actual"]
class Checks(unittest.TestCase):
    def test_contract_complete(self):
        r = rows()
        hyp = json.loads((ROOT/"expected.json").read_text())
        expected = dict(hyp["checks"])  # набор ключей — исходные проверки и добавленные пересмотрами revision*
        for k in sorted(k for k in hyp if k.startswith("revision")):
            expected.update(hyp[k].get("checks", {}))
        self.assertEqual(sorted(r), sorted(expected))
        for k, v in r.items():
            self.assertEqual(v["cmd"], "python3 scripts/run-checks.py " + k)
            self.assertEqual(v["outcome"], "match")

    def test_lengths(self):
        a = actual("lengths")
        self.assertEqual(set(a["native_len_model"]), LANGS)
        # встроенная длина — три разные модели на шесть языков
        self.assertEqual(set(a["native_len_model"].values()), {"bytes", "utf16", "code_points"})
        self.assertTrue(a["utf8_bytes_agree"] and a["code_points_agree"])          # контроль: строки одни и те же
        self.assertTrue(a["emoji_and_nfd_one_grapheme_all"] and a["java_breakiterator_agrees_with_regex"])
        self.assertTrue(a["zalgo_one_grapheme_all"] and a["zalgo_utf8_2001_all"])     # одна графема — 2001 байт

    def test_graphemes(self):
        h = actual("graphemes")["hindi"]
        self.assertEqual(set(h), LANGS)
        self.assertEqual(set(h.values()), {3, 4})                                    # реализации расходятся
        self.assertEqual(h["go"], 4)

    def test_normalization(self):
        a = actual("normalization")
        self.assertTrue(a["raw_unequal_all"])
        self.assertTrue(a["nfc_equal_all"] and a["nfd_equal_all"] and a["nfkc_compat_all"])

    def test_casefold(self):
        a = actual("casefold")
        self.assertEqual(a["upper_sharp_s"]["python"], [83, 83])                     # полное отображение: ß → SS
        self.assertEqual(a["upper_sharp_s"]["go"], [223])                            # простое: ß остаётся
        self.assertEqual(a["lower_dotted_I_utf8_bytes"]["go"], [2, 1])               # простое отображение меняет длину в байтах
        self.assertEqual(a["lower_A_stroke_utf8_bytes"]["go"], [2, 3])
        self.assertIsNone(a["turkish_lower_I"]["rust"])                              # в std Rust нет регистра с языком
        self.assertEqual(a["turkish_lower_I"]["java"], [305])
        self.assertFalse(any(x["strasse"] for x in a["lower_compare"].values()))     # нижний регистр не уравнивает
        self.assertTrue(a["casefold_compare"]["python"]["strasse"] and a["casefold_compare"]["python"]["sigma"])
        self.assertIsNone(a["casefold_compare"]["node"])                             # свёртки в языке нет
        self.assertFalse(a["collation_compare"]["node"]["cafe"])                     # коллация с учётом диакритики
        self.assertTrue(a["collation_compare"]["java"]["strasse"])                   # у JDK свои правила, не CLDR
        self.assertTrue(a["builtin_ignorecase"]["go"]["sigma"])                      # простая свёртка уравнивает ς и σ
        self.assertNotEqual(a["builtin_ignorecase"]["go"]["dotless_i"], a["builtin_ignorecase"]["java"]["dotless_i"])

    def test_truncation(self):
        a = actual("truncation")
        self.assertEqual(a["naive"]["rust"], "panic")
        self.assertEqual(a["naive"]["go"], "invalid_utf8")
        self.assertEqual(a["naive"]["python"], "split_grapheme")
        self.assertNotIn("valid", a["naive"].values())                               # наивная обрезка нигде не прошла бесследно
        self.assertTrue(a["rust_get_returns_none"])
        # обрезка по графемам действительно режет: семья цела, хвост ушёл; при меньшем лимите семья уходит целиком
        self.assertTrue(a["keep_family_intact_all"] and a["drop_family_whole_all"] and a["byte_budget_stops_before_all"])

    def test_security(self):
        a = actual("security")
        self.assertTrue(a["duplicate_without_normalization_all"] and a["nfc_dedups_all"])
        self.assertEqual(set(a["key"].values()), {"nfkc+casefold", "nfkc+lower"})
        for lang, m in a["merges"].items():
            self.assertFalse(m["homoglyph"])                                         # гомоглифы нормализация не ловит
            self.assertTrue(m["fullwidth"])                                          # контроль: NFKC работает
            self.assertEqual(m["strasse"], a["key"][lang] == "nfkc+casefold")        # ключ на нижнем регистре пропускает дубль
        merged = {lang: m["a7ce_a7cf"] for lang, m in a["merges"].items()}
        self.assertEqual(set(merged.values()), {True, False})                        # таблицы разных версий — разные ключи

    def test_invalid(self):
        a = actual("invalid")
        self.assertEqual(a["decode_default"]["go"], "keep_bytes")
        self.assertEqual(a["decode_default"]["rust"], "error")
        self.assertEqual(a["decode_default"]["java"], "replace")                     # молча — U+FFFD
        self.assertEqual(a["lone_surrogate"]["rust"], "not_representable")
        self.assertEqual(a["lone_surrogate"]["python"], "kept_encode_error")

    def test_database(self):
        a = actual("database")
        self.assertTrue(a["char_length_counts_code_points"] and a["octet_length_counts_bytes"] and a["ddl_succeeded"])
        self.assertTrue(a["varchar1_accepts_nfc_e"])                                 # контроль ограничения
        self.assertTrue(a["varchar1_rejects_nfd_e_22001"] and a["varchar5_rejects_family_22001"])
        self.assertTrue(a["unique_raw_accepts_both_forms"] and a["unique_on_normalize_rejects_second_23505"])
        self.assertTrue(a["unexpected_sqlstate_detected"])                           # чужая ошибка не сходит за ограничение

if __name__ == "__main__":
    unittest.main()
