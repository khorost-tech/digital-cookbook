// Тексты в Java: String — UTF-16 (может хранить и одиночный суррогат), length() — code units, codePointCount —
// code points. Графемы — два способа из JDK: BreakIterator и регулярное выражение \X; нормализация —
// java.text.Normalizer; коллация — java.text.Collator; полной свёртки регистра в JDK нет.
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.text.BreakIterator;
import java.text.Collator;
import java.text.Normalizer;
import java.util.*;
import java.util.regex.*;

public class Main {
    // минимальный разбор vectors.json: объекты, массивы чисел и строки — без сторонних библиотек
    static int pos; static String src;
    static Object parse() { ws(); char c = src.charAt(pos);
        if (c == '{') { pos++; Map<String, Object> m = new LinkedHashMap<>(); ws();
            if (src.charAt(pos) == '}') { pos++; return m; }
            while (true) { ws(); String k = (String) parse(); ws(); pos++; m.put(k, parse()); ws();
                if (src.charAt(pos++) == '}') return m; } }
        if (c == '[') { pos++; List<Object> l = new ArrayList<>(); ws();
            if (src.charAt(pos) == ']') { pos++; return l; }
            while (true) { l.add(parse()); ws(); if (src.charAt(pos++) == ']') return l; } }
        if (c == '"') { int e = src.indexOf('"', pos + 1); String s = src.substring(pos + 1, e); pos = e + 1; return s; }
        int st = pos; while (pos < src.length() && "-0123456789".indexOf(src.charAt(pos)) >= 0) pos++;
        return Integer.parseInt(src.substring(st, pos)); }
    static void ws() { while (Character.isWhitespace(src.charAt(pos))) pos++; }
    @SuppressWarnings("unchecked") static Map<String, Object> obj(Object o) { return (Map<String, Object>) o; }
    @SuppressWarnings("unchecked") static List<Object> arr(Object o) { return (List<Object>) o; }
    static String str(Object o) { StringBuilder b = new StringBuilder(); for (Object c : arr(o)) b.appendCodePoint((Integer) c); return b.toString(); }
    static List<Integer> cps(String s) { List<Integer> l = new ArrayList<>(); s.codePoints().forEach(l::add); return l; }
    static int utf8(String s) { return s.getBytes(StandardCharsets.UTF_8).length; }
    static String nfc(String s) { return Normalizer.normalize(s, Normalizer.Form.NFC); }
    static String nfkc(String s) { return Normalizer.normalize(s, Normalizer.Form.NFKC); }
    static List<String> graphemes(String s) { List<String> l = new ArrayList<>(); Matcher m = Pattern.compile("\\X").matcher(s);
        while (m.find()) l.add(m.group()); return l; }
    static int breakIter(String s) { BreakIterator it = BreakIterator.getCharacterInstance(); it.setText(s);
        int n = 0; while (it.next() != BreakIterator.DONE) n++; return n; }
    // первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами
    static String firstGraphemes(String s, int n, int budget) { StringBuilder b = new StringBuilder(); int k = 0;
        for (String g : graphemes(s)) { if (k >= n || (budget > 0 && utf8(b.toString()) + utf8(g) > budget)) break; b.append(g); k++; }
        return b.toString(); }
    static String json(Object o) {
        if (o == null) return "null";
        if (o instanceof String s) return "\"" + s + "\"";
        if (o instanceof Map<?, ?> m) { StringJoiner j = new StringJoiner(",", "{", "}");
            m.forEach((k, v) -> j.add("\"" + k + "\":" + json(v))); return j.toString(); }
        if (o instanceof List<?> l) { StringJoiner j = new StringJoiner(",", "[", "]"); l.forEach(x -> j.add(json(x))); return j.toString(); }
        return String.valueOf(o); }
    static Map<String, Object> map(Object... kv) { Map<String, Object> m = new LinkedHashMap<>();
        for (int i = 0; i < kv.length; i += 2) m.put((String) kv[i], kv[i + 1]); return m; }

    public static void main(String[] a) throws Exception {
        src = Files.readString(Path.of("/v/vectors.json")); Map<String, Object> v = obj(parse());
        Map<String, Object> lengths = new LinkedHashMap<>();
        for (var e : obj(v.get("strings")).entrySet()) { String s = str(e.getValue());
            lengths.put(e.getKey(), map("native", s.length(), "utf8_bytes", utf8(s), "utf16_units", s.length(),
                "code_points", s.codePointCount(0, s.length()), "graphemes", graphemes(s).size(), "graphemes_breakiterator", breakIter(s))); }
        var sec = obj(v.get("security")); String cafeNfc = str(sec.get("cafe_nfc")), cafeNfd = str(sec.get("cafe_nfd"));
        var normalization = map("raw_equal", cafeNfc.equals(cafeNfd), "nfc_equal", nfc(cafeNfc).equals(nfc(cafeNfd)),
            "nfd_equal", Normalizer.normalize(cafeNfc, Normalizer.Form.NFD).equals(Normalizer.normalize(cafeNfd, Normalizer.Form.NFD)),
            "nfkc_ligature", nfkc(str(sec.get("ligature_fi"))).equals("fi"),
            "nfkc_fullwidth", nfkc(str(sec.get("admin_fullwidth"))).equals(str(sec.get("admin_latin"))),
            "nfc_e_one_point", nfc(str(obj(v.get("strings")).get("e_nfd"))).length() == 1);
        var fold = obj(v.get("fold"));
        java.util.function.Function<String, String> f = k -> str(fold.get(k));
        java.util.function.Function<String, String> lower = x -> x.toLowerCase(Locale.ROOT);
        // коллация: немецкие правила, SECONDARY — без учёта регистра, с учётом диакритики
        Collator col = Collator.getInstance(Locale.GERMAN); col.setStrength(Collator.SECONDARY);
        var casefold = map(
            "lower_dotted_I", cps(lower.apply(f.apply("I_dot"))),
            "lower_dotted_I_utf8_bytes", List.of(utf8(f.apply("I_dot")), utf8(lower.apply(f.apply("I_dot")))),
            "lower_A_stroke_utf8_bytes", List.of(utf8(f.apply("A_stroke")), utf8(lower.apply(f.apply("A_stroke")))),
            "turkish_lower_I", cps("I".toLowerCase(Locale.forLanguageTag("tr"))),
            "upper_sharp_s", cps("ß".toUpperCase(Locale.ROOT)),
            "lower_compare", map("strasse", lower.apply(f.apply("strasse")).equals(lower.apply(f.apply("STRASSE"))),
                                 "sigma", lower.apply(f.apply("sigma_final")).equals(lower.apply(f.apply("sigma_plain")))),
            // equalsIgnoreCase: toLowerCase(toUpperCase(c)) посимвольно
            "builtin_ignorecase", map("strasse", f.apply("strasse").equalsIgnoreCase(f.apply("STRASSE")),
                                      "sigma", f.apply("sigma_lone_final").equalsIgnoreCase(f.apply("sigma_lone")),
                                      "dotless_i", f.apply("I").equalsIgnoreCase(f.apply("dotless_i"))),
            "casefold_compare", null,
            "collation_compare", map("strasse", col.compare(f.apply("strasse"), f.apply("STRASSE")) == 0,
                                     "cafe", col.compare(f.apply("cafe"), f.apply("cafe_plain")) == 0));
        var tr = obj(v.get("truncate")); String text = str(tr.get("text"));
        String head = str(arr(tr.get("text")).subList(0, 6)), family = str(arr(tr.get("text")).subList(6, 13));
        String naive = text.substring(0, 7); // 6 букв + первая половина суррогатной пары
        var truncation = map("naive", Character.isHighSurrogate(naive.charAt(naive.length() - 1)) ? "lone_surrogate" : "valid",
            "keep_family_intact", firstGraphemes(text, (Integer) tr.get("keep_family"), 0).equals(head + family),
            "drop_family_whole", firstGraphemes(text, (Integer) tr.get("drop_family"), 0).equals(head),
            "byte_budget_stops_before", firstGraphemes(text, Integer.MAX_VALUE, (Integer) tr.get("byte_budget")).equals(head));
        // ключ уникальности: NFKC + нижний регистр — полной свёртки в JDK нет
        java.util.function.BiPredicate<String, String> same = (x, y) -> lower.apply(nfkc(x)).equals(lower.apply(nfkc(y)));
        var security = map("key", "nfkc+lower", "set_size_raw", new HashSet<>(List.of(cafeNfc, cafeNfd)).size(),
            "set_size_nfc", new HashSet<>(List.of(nfc(cafeNfc), nfc(cafeNfd))).size(),
            "merges_homoglyph", same.test(str(sec.get("admin_cyrillic_a")), str(sec.get("admin_latin"))),
            "merges_fullwidth", same.test(str(sec.get("admin_fullwidth")), str(sec.get("admin_latin"))),
            "merges_strasse", same.test(f.apply("strasse"), f.apply("STRASSE")),
            "merges_final_sigma", same.test(f.apply("sigma_final"), f.apply("sigma_plain")),
            "merges_a7ce_a7cf", same.test(str(sec.get("a7ce")), str(sec.get("a7cf"))));
        // некорректный ввод: new String(bytes, UTF_8) молча заменяет на U+FFFD; одиночный суррогат строка хранит,
        // а при кодировании в UTF-8 он становится «?»
        var inv = obj(v.get("invalid")); byte[] bad = new byte[arr(inv.get("bytes")).size()];
        for (int i = 0; i < bad.length; i++) bad[i] = (byte) (int) (Integer) arr(inv.get("bytes")).get(i);
        String decoded = new String(bad, StandardCharsets.UTF_8);
        String lone = String.valueOf((char) (int) (Integer) inv.get("lone_surrogate"));
        byte[] enc = lone.getBytes(StandardCharsets.UTF_8);
        var invalid = map("decode_default", decoded.indexOf('�') >= 0 ? "replace" : "keep",
            "lone_surrogate", enc.length == 1 && enc[0] == '?' ? "kept_encodes_question_mark" : enc.length == 3 ? "kept_encodes_fffd" : "other");
        System.out.println(json(map("lang", "java", "unicode", null, "lengths", lengths, "normalization", normalization,
            "casefold", casefold, "truncation", truncation, "security", security, "invalid", invalid)));
    }
}
