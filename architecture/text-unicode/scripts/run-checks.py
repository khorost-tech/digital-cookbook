"""Прогон осей стенда architecture/text-unicode → fixtures/checks.jsonl.

    python3 scripts/run-checks.py              → все оси, фикстура перезаписывается
    python3 scripts/run-checks.py casefold     → одна ось, только вывод строки

Шесть программ (Go, Rust, Java, Python, Node, C#) отвечают на одни и те же вопросы о строках из
vectors.json; ось — сводка их ответов. Ось database — PostgreSQL.
"""
import json, os, shutil, sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'stand'))
from details import describe  # noqa: E402
import langs  # noqa: E402
import db  # noqa: E402
ROOT = Path(__file__).resolve().parents[1]
VECTORS = json.loads((ROOT/'vectors.json').read_text())
EMOJI = ('e_nfd', 'smile', 'family', 'flag_ru', 'thumb_tone')
MODEL = {'bytes': 'utf8_bytes', 'utf16': 'utf16_units', 'code_points': 'code_points'}


def per(r, path):
    """Значение по пути для каждого языка: {'go': …, …}."""
    out = {}
    for lang, res in r.items():
        x = res
        for k in path:
            x = x[k]
        out[lang] = x
    return out


def all_of(r, path, want=True):
    return all(v == want for v in per(r, path).values())


def lengths(r):
    def model(res):
        hits = [m for m, f in MODEL.items() if all(l['native'] == l[f] for l in res['lengths'].values())]
        return hits[0] if len(hits) == 1 else 'ambiguous:' + ','.join(hits)
    same = lambda f: len({json.dumps({k: l[f] for k, l in res['lengths'].items()}, sort_keys=True) for res in r.values()}) == 1
    java = r['java']['lengths']
    return {'actual': {'native_len_model': {lang: model(res) for lang, res in r.items()},
                       'utf8_bytes_agree': same('utf8_bytes'), 'code_points_agree': same('code_points'),
                       'emoji_and_nfd_one_grapheme_all': all(res['lengths'][k]['graphemes'] == 1 for res in r.values() for k in EMOJI),
                       'java_breakiterator_agrees_with_regex': all(l['graphemes_breakiterator'] == l['graphemes'] for l in java.values()),
                       'zalgo_one_grapheme_all': all(res['lengths']['zalgo']['graphemes'] == 1 for res in r.values()),
                       'zalgo_utf8_2001_all': all(res['lengths']['zalgo']['utf8_bytes'] == 2001 for res in r.values())},
            'table': {lang: {k: res['lengths'][k] for k in ('e_nfc', 'family', 'hindi', 'zalgo')} for lang, res in r.items()},
            'unicode': {lang: res.get('unicode') for lang, res in r.items()}, 'icu': r['node'].get('icu')}


def graphemes(r):
    return {'actual': {'hindi': {lang: res['lengths']['hindi']['graphemes'] for lang, res in r.items()}}}


def normalization(r):
    return {'actual': {'raw_unequal_all': all_of(r, ('normalization', 'raw_equal'), False),
                       'nfc_equal_all': all_of(r, ('normalization', 'nfc_equal')), 'nfd_equal_all': all_of(r, ('normalization', 'nfd_equal')),
                       'nfkc_compat_all': all(res['normalization'][k] for res in r.values()
                                              for k in ('nfkc_ligature', 'nfkc_fullwidth', 'nfc_e_one_point'))}}


def casefold(r):
    keys = ('lower_dotted_I', 'turkish_lower_I', 'upper_sharp_s', 'lower_dotted_I_utf8_bytes', 'lower_A_stroke_utf8_bytes',
            'lower_compare', 'builtin_ignorecase', 'casefold_compare', 'collation_compare')
    return {'actual': {k: per(r, ('casefold', k)) for k in keys}}


def truncation(r):
    return {'actual': {'naive': per(r, ('truncation', 'naive')),
                       'keep_family_intact_all': all_of(r, ('truncation', 'keep_family_intact')),
                       'drop_family_whole_all': all_of(r, ('truncation', 'drop_family_whole')),
                       'byte_budget_stops_before_all': all_of(r, ('truncation', 'byte_budget_stops_before')),
                       'rust_get_returns_none': r['rust']['truncation']['get_returns_none']}}


def security(r):
    m = lambda res: {'homoglyph': res['security']['merges_homoglyph'], 'fullwidth': res['security']['merges_fullwidth'],
                     'strasse': res['security']['merges_strasse'], 'final_sigma': res['security']['merges_final_sigma'],
                     'a7ce_a7cf': res['security']['merges_a7ce_a7cf']}
    return {'actual': {'key': per(r, ('security', 'key')),
                       'duplicate_without_normalization_all': all_of(r, ('security', 'set_size_raw'), 2),
                       'nfc_dedups_all': all_of(r, ('security', 'set_size_nfc'), 1),
                       'merges': {lang: m(res) for lang, res in r.items()}}}


def invalid(r):
    return {'actual': {'decode_default': per(r, ('invalid', 'decode_default')), 'lone_surrogate': per(r, ('invalid', 'lone_surrogate'))}}


LANG_AXES = {'lengths': lengths, 'graphemes': graphemes, 'normalization': normalization, 'casefold': casefold,
             'truncation': truncation, 'security': security, 'invalid': invalid}
AXES = dict(LANG_AXES, database=lambda r: db.database(VECTORS))


def expected():
    hyp = json.loads((ROOT/'expected.json').read_text()); exp = dict(hyp['checks'])
    for k in sorted(k for k in hyp if k.startswith('revision')): exp.update(hyp[k].get('checks', {}))
    return exp


def main():
    if not sys.argv[1:]: (ROOT/'fixtures/RUN-COMPLETE').unlink(missing_ok=True)
    if shutil.disk_usage(ROOT).free < int(os.environ.get('MIN_FREE_GB', '3'))*1024**3: raise SystemExit('недостаточно места на хосте')
    exp = expected()
    names = sys.argv[1:] or list(exp)
    if sorted(exp) != sorted(AXES) and not sys.argv[1:]: raise RuntimeError('набор осей не совпал с гипотезами')
    results = langs.run_all() if any(n in LANG_AXES for n in names) else {}
    rows = []
    for key in names:
        value = AXES[key](results); actual = value.pop('actual')
        row = {'kind': 'checks', 'variant': key, 'probe': 'docker, шесть языков' if key in LANG_AXES else 'docker, PostgreSQL 18',
               'cmd': 'python3 scripts/run-checks.py ' + key, 'actual': actual,
               'outcome': 'match' if actual == exp[key] else 'mismatch', 'detail': describe(key, actual, value), **value}
        rows.append(row); print(json.dumps({k: row[k] for k in ('variant', 'outcome', 'detail')}, ensure_ascii=False), flush=True)
        if row['outcome'] != 'match': print('расхождение', key, json.dumps(actual, ensure_ascii=False), file=sys.stderr, flush=True)
    if not sys.argv[1:]:
        (ROOT/'fixtures/checks.jsonl').write_text(''.join(json.dumps(r, ensure_ascii=False)+'\n' for r in rows)+'COMPLETE\n')
    if any(r['outcome'] != 'match' for r in rows):
        raise SystemExit('измерение противоречит гипотезе; фиксируйте revision_* до сессий')


if __name__ == '__main__': main()
