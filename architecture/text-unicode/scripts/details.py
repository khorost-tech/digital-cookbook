"""Человекочитаемый detail строки фикстуры: из признаков actual, без шумных полей."""

NAMES = {"go": "Go", "rust": "Rust", "java": "Java", "python": "Python", "node": "Node", "csharp": "C#"}
MODELS = {"bytes": "байты UTF-8", "utf16": "code units UTF-16", "code_points": "code points"}
NAIVE = {"invalid_utf8": "битый UTF-8", "panic": "паника", "lone_surrogate": "одиночный суррогат",
         "split_grapheme": "семья разрезана", "valid": "без следа"}


def yes(b):
    return "да" if b else "нет"


def cps(x):
    return "нет в стандарте" if x is None else " ".join("U+%04X" % c for c in x)


def by_lang(d, fmt=str):
    return ", ".join("%s — %s" % (NAMES[k], fmt(v)) for k, v in d.items())


def describe(key, a, extra):
    if key == "lengths":
        return ("встроенная длина: %s; байты совпали у всех — %s, code points — %s; эмодзи и e+акут — одна графема у всех — %s; "
                "у Java BreakIterator и \\X согласны — %s; a + 1000 акутов — одна графема у всех — %s, 2001 байт — %s") % (
            by_lang(a["native_len_model"], lambda m: MODELS.get(m, m)), yes(a["utf8_bytes_agree"]), yes(a["code_points_agree"]),
            yes(a["emoji_and_nfd_one_grapheme_all"]), yes(a["java_breakiterator_agrees_with_regex"]), yes(a["zalgo_one_grapheme_all"]),
            yes(a["zalgo_utf8_2001_all"]))
    if key == "graphemes":
        return "графем в नमस्ते: %s" % by_lang(a["hindi"])
    if key == "normalization":
        return ("café в NFC и NFD без нормализации не равны у всех — %s; после NFC равны — %s, после NFD — %s; NFKC разворачивает "
                "лигатуру и полноширинные буквы — %s") % (yes(a["raw_unequal_all"]), yes(a["nfc_equal_all"]), yes(a["nfd_equal_all"]),
                                                         yes(a["nfkc_compat_all"]))
    if key == "casefold":
        cmp = lambda d: "нет в стандарте" if d is None else ", ".join("%s — %s" % (k, "равны" if x else "не равны") for k, x in d.items())
        pair = lambda x: "%d → %d" % tuple(x)
        return ("İ в нижний: %s; UTF-8 байт İ → нижний: %s; Ⱥ → нижний: %s; турецкая I в нижний: %s; ß в верхний: %s; "
                "нижний регистр: %s; встроенное сравнение без регистра: %s; свёртка: %s; коллация de без регистра: %s") % (
            by_lang(a["lower_dotted_I"], cps), by_lang(a["lower_dotted_I_utf8_bytes"], pair), by_lang(a["lower_A_stroke_utf8_bytes"], pair),
            by_lang(a["turkish_lower_I"], cps), by_lang(a["upper_sharp_s"], cps), by_lang(a["lower_compare"], cmp),
            by_lang(a["builtin_ignorecase"], cmp), by_lang(a["casefold_compare"], cmp), by_lang(a["collation_compare"], cmp))
    if key == "truncation":
        return ("наивная обрезка внутри эмодзи: %s; str::get в Rust без паники вернул None — %s; по графемам: лимит 7 — семья цела, "
                "XYZ отрезан — %s, лимит 6 — семья убрана целиком — %s, бюджет 20 байт — остановка до семьи — %s") % (
            by_lang(a["naive"], lambda k: NAIVE.get(k, k)), yes(a["rust_get_returns_none"]), yes(a["keep_family_intact_all"]),
            yes(a["drop_family_whole_all"]), yes(a["byte_budget_stops_before_all"]))
    if key == "security":
        m = lambda d: "гомоглиф %s, полноширинные %s, Straße/STRASSE %s, ς/σ %s, U+A7CE/U+A7CF %s" % tuple(
            "склеены" if d[k] else "различны" for k in ("homoglyph", "fullwidth", "strasse", "final_sigma", "a7ce_a7cf"))
        return ("café в двух формах без нормализации — два ключа у всех: %s; после NFC — один: %s; ключ: %s; склейка ключом: %s") % (
            yes(a["duplicate_without_normalization_all"]), yes(a["nfc_dedups_all"]), by_lang(a["key"]), by_lang(a["merges"], m))
    if key == "invalid":
        return "байты 66 6F FF 6F по умолчанию: %s; одиночный суррогат U+D800: %s" % (by_lang(a["decode_default"]), by_lang(a["lone_surrogate"]))
    if key == "database":
        return ("char_length — code points: %s, octet_length — байты: %s; DDL прошёл — %s; varchar(5) отверг одну графему-семью с 22001 — %s; "
                "varchar(1) принял é в NFC — %s, отверг в NFD с 22001 — %s; уникальный индекс по сырому имени принял обе формы café — %s, "
                "по normalize(name, NFC) отверг вторую с 23505 — %s; неверный запрос распознан как неожиданная ошибка — %s") % tuple(yes(a[k]) for k in (
                    "char_length_counts_code_points", "octet_length_counts_bytes", "ddl_succeeded", "varchar5_rejects_family_22001",
                    "varchar1_accepts_nfc_e", "varchar1_rejects_nfd_e_22001", "unique_raw_accepts_both_forms",
                    "unique_on_normalize_rejects_second_23505", "unexpected_sqlstate_detected"))
    return ""
