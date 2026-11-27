// Тексты в Rust: String/&str — только корректный UTF-8, len() — байты, chars() — скаляры Unicode. Нормализация и
// графемы — крейты unicode-normalization и unicode-segmentation; свёртки регистра, регистра с учётом языка и
// коллации в стандартной библиотеке нет.
use serde_json::{json, Map, Value};
use std::collections::HashSet;
use unicode_normalization::UnicodeNormalization;
use unicode_segmentation::UnicodeSegmentation;

fn s(v: &Value) -> String {
    v.as_array().unwrap().iter().map(|c| char::from_u32(c.as_u64().unwrap() as u32).unwrap()).collect()
}
fn cps(x: &str) -> Vec<u32> {
    x.chars().map(|c| c as u32).collect()
}
// первые n графем; budget > 0 — ещё и не больше budget байт, только целыми графемами
fn first_graphemes(x: &str, n: usize, budget: usize) -> String {
    let mut out = String::new();
    for g in x.graphemes(true).take(n) {
        if budget > 0 && out.len() + g.len() > budget {
            break;
        }
        out.push_str(g);
    }
    out
}

fn main() {
    let v: Value = serde_json::from_str(&std::fs::read_to_string("/v/vectors.json").unwrap()).unwrap();
    let (major, minor, update) = char::UNICODE_VERSION;
    let mut lengths = Map::new();
    for (id, r) in v["strings"].as_object().unwrap() {
        let x = s(r);
        lengths.insert(id.clone(), json!({
            "native": x.len(), // байты
            "utf8_bytes": x.len(),
            "utf16_units": x.encode_utf16().count(),
            "code_points": x.chars().count(),
            "graphemes": x.graphemes(true).count(),
        }));
    }
    let sec = &v["security"];
    let (nfc, nfd) = (s(&sec["cafe_nfc"]), s(&sec["cafe_nfd"]));
    let n = |x: &str| x.nfc().collect::<String>();
    let normalization = json!({
        "raw_equal": nfc == nfd,
        "nfc_equal": n(&nfc) == n(&nfd),
        "nfd_equal": nfc.nfd().collect::<String>() == nfd.nfd().collect::<String>(),
        "nfkc_ligature": s(&sec["ligature_fi"]).nfkc().collect::<String>() == "fi",
        "nfkc_fullwidth": s(&sec["admin_fullwidth"]).nfkc().collect::<String>() == s(&sec["admin_latin"]),
        "nfc_e_one_point": n(&s(&v["strings"]["e_nfd"])).chars().count() == 1,
    });
    let f = |k: &str| s(&v["fold"][k]);
    let lower_eq = |a: &str, b: &str| a.to_lowercase() == b.to_lowercase();
    let casefold = json!({
        "lower_dotted_I": cps(&f("I_dot").to_lowercase()),
        "lower_dotted_I_utf8_bytes": [f("I_dot").len(), f("I_dot").to_lowercase().len()],
        "lower_A_stroke_utf8_bytes": [f("A_stroke").len(), f("A_stroke").to_lowercase().len()],
        "turkish_lower_I": Value::Null, // регистра с учётом языка в std нет
        "upper_sharp_s": cps(&"\u{df}".to_uppercase()),
        "lower_compare": {"strasse": lower_eq(&f("strasse"), &f("STRASSE")), "sigma": lower_eq(&f("sigma_final"), &f("sigma_plain"))},
        "builtin_ignorecase": Value::Null, // eq_ignore_ascii_case — только ASCII
        "casefold_compare": Value::Null,   // полной свёртки в std нет
        "collation_compare": Value::Null,  // коллации в std нет
    });
    let tr = &v["truncate"];
    let text = s(&tr["text"]);
    let cut = s(&json!(tr["text"].as_array().unwrap()[..6])).len() + 1;
    std::panic::set_hook(Box::new(|_| {}));
    let t = text.clone();
    let naive = match std::panic::catch_unwind(move || t[..cut].to_string()) {
        Err(_) => "panic",
        Ok(_) => "valid",
    };
    let head = s(&json!(tr["text"].as_array().unwrap()[..6]));
    let family = s(&json!(tr["text"].as_array().unwrap()[6..13]));
    let num = |k: &str| tr[k].as_u64().unwrap() as usize;
    let truncation = json!({
        "naive": naive,
        // безопасный срез без паники — str::get возвращает Option
        "get_returns_none": text.get(..cut).is_none(),
        "keep_family_intact": first_graphemes(&text, num("keep_family"), 0) == format!("{head}{family}"),
        "drop_family_whole": first_graphemes(&text, num("drop_family"), 0) == head,
        "byte_budget_stops_before": first_graphemes(&text, usize::MAX, num("byte_budget")) == head,
    });
    // ключ уникальности: NFKC + нижний регистр — полной свёртки в std нет
    let key = |x: &str| x.nfkc().collect::<String>().to_lowercase();
    let same = |a: &str, b: &str| key(a) == key(b);
    let raws: HashSet<&String> = [&nfc, &nfd].into_iter().collect();
    let nfcs: HashSet<String> = [n(&nfc), n(&nfd)].into_iter().collect();
    let security = json!({
        "key": "nfkc+lower",
        "set_size_raw": raws.len(),
        "set_size_nfc": nfcs.len(),
        "merges_homoglyph": same(&s(&sec["admin_cyrillic_a"]), &s(&sec["admin_latin"])),
        "merges_fullwidth": same(&s(&sec["admin_fullwidth"]), &s(&sec["admin_latin"])),
        "merges_strasse": same(&f("strasse"), &f("STRASSE")),
        "merges_final_sigma": same(&f("sigma_final"), &f("sigma_plain")),
        "merges_a7ce_a7cf": same(&s(&sec["a7ce"]), &s(&sec["a7cf"])), // пара из Unicode 17
    });
    // некорректный ввод: String::from_utf8 отвергает; суррогат не может быть char вовсе
    let bad: Vec<u8> = v["invalid"]["bytes"].as_array().unwrap().iter().map(|b| b.as_u64().unwrap() as u8).collect();
    let decode = if String::from_utf8(bad).is_err() { "error" } else { "valid" };
    let lone = v["invalid"]["lone_surrogate"].as_u64().unwrap() as u32;
    let surrogate = if char::from_u32(lone).is_none() { "not_representable" } else { "kept" };
    let invalid = json!({"decode_default": decode, "lone_surrogate": surrogate});
    println!("{}", json!({"lang": "rust", "unicode": format!("{major}.{minor}.{update}"), "lengths": lengths,
                          "normalization": normalization, "casefold": casefold, "truncation": truncation,
                          "security": security, "invalid": invalid}));
}
