"""Тексты в Python: str — последовательность code points (может хранить и одиночный суррогат), len — code points.
Нормализация — unicodedata, полная свёртка регистра — str.casefold; графем в стандартной библиотеке нет —
регулярное выражение \\X из regex; коллации, не зависящей от настроек ОС, тоже нет."""
import json
import unicodedata

import regex

v = json.load(open("/v/vectors.json"))
s = lambda cps: "".join(map(chr, cps))
cps = lambda x: [ord(c) for c in x]
graphemes = lambda x: regex.findall(r"\X", x)
N = unicodedata.normalize


def first_graphemes(x, n, budget=0):
    """Первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами."""
    out = ""
    for g in graphemes(x)[:n]:
        if budget and len((out + g).encode()) > budget:
            break
        out += g
    return out


out = {"lang": "python", "unicode": unicodedata.unidata_version, "lengths": {}}
for vid, r in v["strings"].items():
    x = s(r)
    out["lengths"][vid] = {"native": len(x), "utf8_bytes": len(x.encode()), "utf16_units": len(x.encode("utf-16-le")) // 2,
                           "code_points": len(x), "graphemes": len(graphemes(x))}
sec = v["security"]
nfc, nfd = s(sec["cafe_nfc"]), s(sec["cafe_nfd"])
out["normalization"] = {"raw_equal": nfc == nfd, "nfc_equal": N("NFC", nfc) == N("NFC", nfd), "nfd_equal": N("NFD", nfc) == N("NFD", nfd),
                        "nfkc_ligature": N("NFKC", s(sec["ligature_fi"])) == "fi",
                        "nfkc_fullwidth": N("NFKC", s(sec["admin_fullwidth"])) == s(sec["admin_latin"]),
                        "nfc_e_one_point": len(N("NFC", s(v["strings"]["e_nfd"]))) == 1}
f = lambda k: s(v["fold"][k])
out["casefold"] = {
    "lower_dotted_I": cps(f("I_dot").lower()),
    "lower_dotted_I_utf8_bytes": [len(f("I_dot").encode()), len(f("I_dot").lower().encode())],
    "lower_A_stroke_utf8_bytes": [len(f("A_stroke").encode()), len(f("A_stroke").lower().encode())],
    "turkish_lower_I": None,  # регистра с учётом языка в str нет
    "upper_sharp_s": cps("ß".upper()),
    "lower_compare": {"strasse": f("strasse").lower() == f("STRASSE").lower(), "sigma": f("sigma_final").lower() == f("sigma_plain").lower()},
    "builtin_ignorecase": None,  # отдельной операции нет — только casefold
    "casefold_compare": {"strasse": f("strasse").casefold() == f("STRASSE").casefold(),
                         "sigma": f("sigma_final").casefold() == f("sigma_plain").casefold()},
    "collation_compare": None,  # locale.strcoll зависит от настроек ОС, это не ICU
}
tr = v["truncate"]
text, head, family = s(tr["text"]), s(tr["text"][:6]), s(tr["text"][6:13])
naive = text[:7]  # 6 букв + первый code point семьи: строка корректна, но последний «символ» — мужчина из семьи
out["truncation"] = {"naive": "split_grapheme" if graphemes(naive)[-1] not in graphemes(text) else "valid",
                     "keep_family_intact": first_graphemes(text, tr["keep_family"]) == head + family,
                     "drop_family_whole": first_graphemes(text, tr["drop_family"]) == head,
                     "byte_budget_stops_before": first_graphemes(text, 10**9, tr["byte_budget"]) == head}
key = lambda x: N("NFKC", x).casefold()  # ключ уникальности: NFKC + полная свёртка
same = lambda a, b: key(a) == key(b)
out["security"] = {"key": "nfkc+casefold", "set_size_raw": len({nfc, nfd}), "set_size_nfc": len({N("NFC", nfc), N("NFC", nfd)}),
                   "merges_homoglyph": same(s(sec["admin_cyrillic_a"]), s(sec["admin_latin"])),
                   "merges_fullwidth": same(s(sec["admin_fullwidth"]), s(sec["admin_latin"])),
                   "merges_strasse": same(f("strasse"), f("STRASSE")),
                   "merges_final_sigma": same(f("sigma_final"), f("sigma_plain")),
                   "merges_a7ce_a7cf": same(s(sec["a7ce"]), s(sec["a7cf"]))}  # пара из Unicode 17
# некорректный ввод: bytes.decode() отвергает; одиночный суррогат str хранит, но кодирование в UTF-8 падает
try:
    bytes(v["invalid"]["bytes"]).decode()
    decode = "valid"
except UnicodeDecodeError:
    decode = "error"
lone = chr(v["invalid"]["lone_surrogate"])
try:
    lone.encode()
    surrogate = "kept_encodes"
except UnicodeEncodeError:
    surrogate = "kept_encode_error"
out["invalid"] = {"decode_default": decode, "lone_surrogate": surrogate}
print(json.dumps(out))
