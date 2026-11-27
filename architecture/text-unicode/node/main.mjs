// Тексты в JavaScript: строка — UTF-16 (может хранить и одиночный суррогат), .length — code units, [...s] — code
// points, графемы — Intl.Segmenter, нормализация — String.prototype.normalize, регистр с учётом языка —
// toLocaleLowerCase, коллация — Intl.Collator; полной свёртки регистра в языке нет.
import { readFileSync } from "node:fs";

const v = JSON.parse(readFileSync("/v/vectors.json", "utf8"));
const s = (c) => String.fromCodePoint(...c);
const cps = (x) => [...x].map((c) => c.codePointAt(0));
const utf8 = (x) => Buffer.byteLength(x, "utf8");
const seg = new Intl.Segmenter("und", { granularity: "grapheme" });
const graphemes = (x) => [...seg.segment(x)].map((g) => g.segment);
// первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами
const firstGraphemes = (x, n, budget = 0) => {
  let out = "";
  for (const g of graphemes(x).slice(0, n)) {
    if (budget && utf8(out + g) > budget) break;
    out += g;
  }
  return out;
};

const out = { lang: "node", unicode: process.versions.unicode, icu: process.versions.icu, lengths: {} };
for (const [id, r] of Object.entries(v.strings)) {
  const x = s(r);
  out.lengths[id] = { native: x.length, utf8_bytes: utf8(x), utf16_units: x.length, code_points: [...x].length, graphemes: graphemes(x).length };
}
const sec = v.security;
const [nfc, nfd] = [s(sec.cafe_nfc), s(sec.cafe_nfd)];
out.normalization = { raw_equal: nfc === nfd, nfc_equal: nfc.normalize("NFC") === nfd.normalize("NFC"),
  nfd_equal: nfc.normalize("NFD") === nfd.normalize("NFD"), nfkc_ligature: s(sec.ligature_fi).normalize("NFKC") === "fi",
  nfkc_fullwidth: s(sec.admin_fullwidth).normalize("NFKC") === s(sec.admin_latin),
  nfc_e_one_point: [...s(v.strings.e_nfd).normalize("NFC")].length === 1 };
const f = (k) => s(v.fold[k]);
// коллация: немецкие правила, accent — без учёта регистра, с учётом диакритики
const col = new Intl.Collator("de", { sensitivity: "accent" });
out.casefold = {
  lower_dotted_I: cps(f("I_dot").toLowerCase()),
  lower_dotted_I_utf8_bytes: [utf8(f("I_dot")), utf8(f("I_dot").toLowerCase())],
  lower_A_stroke_utf8_bytes: [utf8(f("A_stroke")), utf8(f("A_stroke").toLowerCase())],
  turkish_lower_I: cps("I".toLocaleLowerCase("tr")),
  upper_sharp_s: cps("ß".toUpperCase()),
  lower_compare: { strasse: f("strasse").toLowerCase() === f("STRASSE").toLowerCase(),
                   sigma: f("sigma_final").toLowerCase() === f("sigma_plain").toLowerCase() },
  builtin_ignorecase: null,
  casefold_compare: null,
  collation_compare: { strasse: col.compare(f("strasse"), f("STRASSE")) === 0, cafe: col.compare(f("cafe"), f("cafe_plain")) === 0 },
};
const tr = v.truncate;
const text = s(tr.text), head = s(tr.text.slice(0, 6)), family = s(tr.text.slice(6, 13));
const naive = text.slice(0, 7);  // 6 букв + первая половина суррогатной пары
out.truncation = { naive: naive.isWellFormed() ? "valid" : "lone_surrogate",
  keep_family_intact: firstGraphemes(text, tr.keep_family) === head + family,
  drop_family_whole: firstGraphemes(text, tr.drop_family) === head,
  byte_budget_stops_before: firstGraphemes(text, Infinity, tr.byte_budget) === head };
// ключ уникальности: NFKC + нижний регистр — полной свёртки в языке нет
const key = (x) => x.normalize("NFKC").toLowerCase();
const same = (a, b) => key(a) === key(b);
out.security = { key: "nfkc+lower", set_size_raw: new Set([nfc, nfd]).size, set_size_nfc: new Set([nfc.normalize("NFC"), nfd.normalize("NFC")]).size,
  merges_homoglyph: same(s(sec.admin_cyrillic_a), s(sec.admin_latin)), merges_fullwidth: same(s(sec.admin_fullwidth), s(sec.admin_latin)),
  merges_strasse: same(f("strasse"), f("STRASSE")), merges_final_sigma: same(f("sigma_final"), f("sigma_plain")),
  merges_a7ce_a7cf: same(s(sec.a7ce), s(sec.a7cf)) };  // пара из Unicode 17
// некорректный ввод: Buffer.toString и TextDecoder по умолчанию молча заменяют на U+FFFD; одиночный суррогат
// строка хранит, при кодировании в UTF-8 он становится U+FFFD
const decoded = Buffer.from(v.invalid.bytes).toString("utf8");
const enc = Buffer.from(String.fromCharCode(v.invalid.lone_surrogate), "utf8");
out.invalid = { decode_default: decoded.includes("�") ? "replace" : "keep",
  lone_surrogate: enc.length === 3 && enc[0] === 0xef && enc[1] === 0xbf && enc[2] === 0xbd ? "kept_encodes_fffd" : "other" };
console.log(JSON.stringify(out));
