// Тексты в Go: string — произвольные байты (обычно UTF-8, но не обязательно), rune — code point.
// Нормализация, свёртка регистра, регистр с учётом языка и коллация — golang.org/x/text, графемы — rivo/uniseg;
// в стандартной библиотеке их нет.
package main

import (
	"encoding/json"
	"os"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

type vectors struct {
	Strings  map[string][]rune `json:"strings"`
	Truncate struct {
		Text       []rune `json:"text"`
		KeepFamily int    `json:"keep_family"`
		DropFamily int    `json:"drop_family"`
		ByteBudget int    `json:"byte_budget"`
	} `json:"truncate"`
	Fold     map[string][]rune `json:"fold"`
	Security map[string][]rune `json:"security"`
	Invalid  struct {
		Bytes         []byte `json:"bytes"`
		LoneSurrogate rune   `json:"lone_surrogate"`
	} `json:"invalid"`
}

func cps(s string) []rune { return []rune(s) }

// firstGraphemes — первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами.
func firstGraphemes(s string, n, budget int) string {
	var b strings.Builder
	g, k := uniseg.NewGraphemes(s), 0
	for g.Next() && k < n {
		if budget > 0 && b.Len()+len(g.Str()) > budget {
			break
		}
		b.WriteString(g.Str())
		k++
	}
	return b.String()
}

func main() {
	var v vectors
	raw, _ := os.ReadFile("/v/vectors.json")
	_ = json.Unmarshal(raw, &v)
	out := map[string]any{"lang": "go", "unicode": unicode.Version}

	lengths := map[string]map[string]int{}
	for id, r := range v.Strings {
		s := string(r)
		lengths[id] = map[string]int{
			"native":      len(s), // байты
			"utf8_bytes":  len(s),
			"utf16_units": len(utf16.Encode([]rune(s))),
			"code_points": utf8.RuneCountInString(s),
			"graphemes":   uniseg.GraphemeClusterCount(s),
		}
	}
	out["lengths"] = lengths

	sec := v.Security
	nfc, nfd := string(sec["cafe_nfc"]), string(sec["cafe_nfd"])
	out["normalization"] = map[string]bool{
		"raw_equal":       nfc == nfd,
		"nfc_equal":       norm.NFC.String(nfc) == norm.NFC.String(nfd),
		"nfd_equal":       norm.NFD.String(nfc) == norm.NFD.String(nfd),
		"nfkc_ligature":   norm.NFKC.String(string(sec["ligature_fi"])) == "fi",
		"nfkc_fullwidth":  norm.NFKC.String(string(sec["admin_fullwidth"])) == string(sec["admin_latin"]),
		"nfc_e_one_point": utf8.RuneCountInString(norm.NFC.String(string(v.Strings["e_nfd"]))) == 1,
	}

	f := func(k string) string { return string(v.Fold[k]) }
	lowerEq := func(a, b string) bool { return strings.ToLower(a) == strings.ToLower(b) }
	fold := cases.Fold()
	foldEq := func(a, b string) bool { return fold.String(a) == fold.String(b) }
	// коллация: немецкие правила, без учёта регистра, с учётом диакритики
	col := collate.New(language.German, collate.IgnoreCase)
	colEq := func(a, b string) bool { return col.CompareString(a, b) == 0 }
	out["casefold"] = map[string]any{
		"lower_dotted_I":            cps(strings.ToLower(f("I_dot"))),
		"lower_dotted_I_utf8_bytes": []int{len(f("I_dot")), len(strings.ToLower(f("I_dot")))},
		"lower_A_stroke_utf8_bytes": []int{len(f("A_stroke")), len(strings.ToLower(f("A_stroke")))},
		"turkish_lower_I":           cps(cases.Lower(language.Turkish).String("I")),
		"upper_sharp_s":             cps(strings.ToUpper("ß")),
		"lower_compare":             map[string]bool{"strasse": lowerEq(f("strasse"), f("STRASSE")), "sigma": lowerEq(f("sigma_final"), f("sigma_plain"))},
		"builtin_ignorecase": map[string]bool{ // EqualFold — простая свёртка Unicode
			"strasse": strings.EqualFold(f("strasse"), f("STRASSE")), "sigma": strings.EqualFold(f("sigma_lone_final"), f("sigma_lone")),
			"dotless_i": strings.EqualFold(f("I"), f("dotless_i"))},
		"casefold_compare":          map[string]bool{"strasse": foldEq(f("strasse"), f("STRASSE")), "sigma": foldEq(f("sigma_final"), f("sigma_plain"))},
		"collation_compare":         map[string]bool{"strasse": colEq(f("strasse"), f("STRASSE")), "cafe": colEq(f("cafe"), f("cafe_plain"))},
	}

	tr := v.Truncate
	text := string(tr.Text)
	cut := len(string(tr.Text[:6])) + 1 // байт внутрь первого эмодзи
	naive := "valid"
	if !utf8.ValidString(text[:cut]) {
		naive = "invalid_utf8"
	}
	head, family := string(tr.Text[:6]), string(tr.Text[6:13])
	out["truncation"] = map[string]any{
		"naive":                    naive,
		"keep_family_intact":       firstGraphemes(text, tr.KeepFamily, 0) == head+family,
		"drop_family_whole":        firstGraphemes(text, tr.DropFamily, 0) == head,
		"byte_budget_stops_before": firstGraphemes(text, 1<<30, tr.ByteBudget) == head,
	}

	// ключ уникальности: NFKC + свёртка регистра (cases.Fold)
	key := func(s string) string { return fold.String(norm.NFKC.String(s)) }
	same := func(a, b string) bool { return key(a) == key(b) }
	out["security"] = map[string]any{
		"key":                "nfkc+casefold",
		"set_size_raw":       len(map[string]bool{nfc: true, nfd: true}),
		"set_size_nfc":       len(map[string]bool{norm.NFC.String(nfc): true, norm.NFC.String(nfd): true}),
		"merges_homoglyph":   same(string(sec["admin_cyrillic_a"]), string(sec["admin_latin"])),
		"merges_fullwidth":   same(string(sec["admin_fullwidth"]), string(sec["admin_latin"])),
		"merges_strasse":     same(f("strasse"), f("STRASSE")),
		"merges_final_sigma": same(f("sigma_final"), f("sigma_plain")),
		"merges_a7ce_a7cf":   same(string(sec["a7ce"]), string(sec["a7cf"])), // пара из Unicode 17
	}

	// некорректный ввод: string(b) хранит байты как есть; U+FFFD появляется только при переборе рун
	bad := string(v.Invalid.Bytes)
	decode := "keep_bytes"
	if utf8.ValidString(bad) {
		decode = "valid"
	}
	// rune → string: суррогат — не скаляр Unicode, вместо него получится U+FFFD
	surrogate := "replaced_on_conversion"
	if string(v.Invalid.LoneSurrogate) != "�" {
		surrogate = "kept"
	}
	out["invalid"] = map[string]any{"decode_default": decode, "lone_surrogate": surrogate}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
