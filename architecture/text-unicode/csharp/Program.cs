// Тексты в C#: string — UTF-16 (может хранить и одиночный суррогат), Length — code units, Rune — скаляр Unicode,
// графемы — StringInfo (text elements), нормализация — string.Normalize, регистр с учётом языка — ToLower(CultureInfo),
// коллация — CompareInfo; полной свёртки регистра в .NET нет.
using System.Globalization;
using System.Text;
using System.Text.Json.Nodes;

var v = JsonNode.Parse(File.ReadAllText("/v/vectors.json"))!;
string S(JsonNode n) { var b = new StringBuilder(); foreach (var c in n.AsArray()) b.Append(char.ConvertFromUtf32((int)c!)); return b.ToString(); }
JsonArray Cps(string x) => new(x.EnumerateRunes().Select(r => (JsonNode)r.Value).ToArray());
int Utf8(string x) => Encoding.UTF8.GetByteCount(x);
List<string> Graphemes(string x) { var e = StringInfo.GetTextElementEnumerator(x); var l = new List<string>(); while (e.MoveNext()) l.Add(e.GetTextElement()); return l; }
// первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами
string FirstGraphemes(string x, int n, int budget) {
    var b = new StringBuilder();
    foreach (var g in Graphemes(x).Take(n)) { if (budget > 0 && Utf8(b.ToString() + g) > budget) break; b.Append(g); }
    return b.ToString();
}

var lengths = new JsonObject();
foreach (var (id, r) in v["strings"]!.AsObject()) {
    var x = S(r!);
    lengths[id] = new JsonObject { ["native"] = x.Length, ["utf8_bytes"] = Utf8(x), ["utf16_units"] = x.Length,
        ["code_points"] = x.EnumerateRunes().Count(), ["graphemes"] = new StringInfo(x).LengthInTextElements };
}
var sec = v["security"]!;
string nfc = S(sec["cafe_nfc"]!), nfd = S(sec["cafe_nfd"]!);
var normalization = new JsonObject { ["raw_equal"] = nfc == nfd,
    ["nfc_equal"] = nfc.Normalize(NormalizationForm.FormC) == nfd.Normalize(NormalizationForm.FormC),
    ["nfd_equal"] = nfc.Normalize(NormalizationForm.FormD) == nfd.Normalize(NormalizationForm.FormD),
    ["nfkc_ligature"] = S(sec["ligature_fi"]!).Normalize(NormalizationForm.FormKC) == "fi",
    ["nfkc_fullwidth"] = S(sec["admin_fullwidth"]!).Normalize(NormalizationForm.FormKC) == S(sec["admin_latin"]!),
    ["nfc_e_one_point"] = S(v["strings"]!["e_nfd"]!).Normalize(NormalizationForm.FormC).EnumerateRunes().Count() == 1 };
string F(string k) => S(v["fold"]![k]!);
string Lower(string x) => x.ToLowerInvariant();
// коллация: немецкие правила, IgnoreCase — без учёта регистра, с учётом диакритики
var col = new CultureInfo("de-DE").CompareInfo;
bool ColEq(string a, string b) => col.Compare(a, b, CompareOptions.IgnoreCase) == 0;
var casefold = new JsonObject {
    ["lower_dotted_I"] = Cps(Lower(F("I_dot"))),
    ["lower_dotted_I_utf8_bytes"] = new JsonArray(Utf8(F("I_dot")), Utf8(Lower(F("I_dot")))),
    ["lower_A_stroke_utf8_bytes"] = new JsonArray(Utf8(F("A_stroke")), Utf8(Lower(F("A_stroke")))),
    ["turkish_lower_I"] = Cps("I".ToLower(new CultureInfo("tr-TR"))),
    ["upper_sharp_s"] = Cps("ß".ToUpperInvariant()),
    ["lower_compare"] = new JsonObject { ["strasse"] = Lower(F("strasse")) == Lower(F("STRASSE")), ["sigma"] = Lower(F("sigma_final")) == Lower(F("sigma_plain")) },
    // OrdinalIgnoreCase — простой верхний регистр
    ["builtin_ignorecase"] = new JsonObject { ["strasse"] = string.Equals(F("strasse"), F("STRASSE"), StringComparison.OrdinalIgnoreCase),
        ["sigma"] = string.Equals(F("sigma_lone_final"), F("sigma_lone"), StringComparison.OrdinalIgnoreCase),
        ["dotless_i"] = string.Equals(F("I"), F("dotless_i"), StringComparison.OrdinalIgnoreCase) },
    ["casefold_compare"] = null,
    ["collation_compare"] = new JsonObject { ["strasse"] = ColEq(F("strasse"), F("STRASSE")), ["cafe"] = ColEq(F("cafe"), F("cafe_plain")) } };
var tr = v["truncate"]!;
var text = S(tr["text"]!);
var arr = tr["text"]!.AsArray();
string head = S(new JsonArray(arr.Take(6).Select(x => (JsonNode)(int)x!).ToArray())), family = S(new JsonArray(arr.Skip(6).Take(7).Select(x => (JsonNode)(int)x!).ToArray()));
var naive = text.Substring(0, 7); // 6 букв + первая половина суррогатной пары
var truncation = new JsonObject { ["naive"] = char.IsHighSurrogate(naive[^1]) ? "lone_surrogate" : "valid",
    ["keep_family_intact"] = FirstGraphemes(text, (int)tr["keep_family"]!, 0) == head + family,
    ["drop_family_whole"] = FirstGraphemes(text, (int)tr["drop_family"]!, 0) == head,
    ["byte_budget_stops_before"] = FirstGraphemes(text, int.MaxValue, (int)tr["byte_budget"]!) == head };
// ключ уникальности: NFKC + нижний регистр — полной свёртки в .NET нет
string Key(string x) => Lower(x.Normalize(NormalizationForm.FormKC));
bool Same(string a, string b) => Key(a) == Key(b);
var security = new JsonObject { ["key"] = "nfkc+lower", ["set_size_raw"] = new HashSet<string> { nfc, nfd }.Count,
    ["set_size_nfc"] = new HashSet<string> { nfc.Normalize(), nfd.Normalize() }.Count,
    ["merges_homoglyph"] = Same(S(sec["admin_cyrillic_a"]!), S(sec["admin_latin"]!)),
    ["merges_fullwidth"] = Same(S(sec["admin_fullwidth"]!), S(sec["admin_latin"]!)),
    ["merges_strasse"] = Same(F("strasse"), F("STRASSE")), ["merges_final_sigma"] = Same(F("sigma_final"), F("sigma_plain")),
    ["merges_a7ce_a7cf"] = Same(S(sec["a7ce"]!), S(sec["a7cf"]!)) }; // пара из Unicode 17
// некорректный ввод: Encoding.UTF8.GetString молча заменяет на U+FFFD; одиночный суррогат строка хранит,
// при кодировании в UTF-8 он становится U+FFFD
var bad = v["invalid"]!["bytes"]!.AsArray().Select(b => (byte)(int)b!).ToArray();
var decoded = Encoding.UTF8.GetString(bad);
var enc = Encoding.UTF8.GetBytes(((char)(int)v["invalid"]!["lone_surrogate"]!).ToString());
var invalid = new JsonObject { ["decode_default"] = decoded.Contains('�') ? "replace" : "keep",
    ["lone_surrogate"] = enc.Length == 3 && enc[0] == 0xEF && enc[1] == 0xBF && enc[2] == 0xBD ? "kept_encodes_fffd" : "other" };
Console.WriteLine(new JsonObject { ["lang"] = "csharp", ["unicode"] = null, ["lengths"] = lengths, ["normalization"] = normalization,
    ["casefold"] = casefold, ["truncation"] = truncation, ["security"] = security, ["invalid"] = invalid }.ToJsonString());
