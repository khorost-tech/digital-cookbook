"""Прогон осей стенда architecture/web-frameworks → fixtures/checks.jsonl.

    python3 scripts/run-checks.py              → все оси, фикстура перезаписывается
    python3 scripts/run-checks.py validation   → одна ось, только вывод строки

Двенадцать приложений (пары «минимальный — полный» в шести экосистемах) с одним и тем же API получают одни и те же
запросы; ось — их ответы по умолчанию.
"""
import json, os, shutil, sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'stand'))
from details import describe  # noqa: E402
import apps  # noqa: E402
import probe  # noqa: E402
ROOT = Path(__file__).resolve().parents[1]


def per(r, *path):
    out = {}
    for a, res in r.items():
        x = res
        for k in path:
            x = x[k]
        out[a] = x
    return out


def ratio(r, eco):
    small, full = apps.PAIRS[eco]
    return r[full]['start_s'] / r[small]['start_s'], r[full]['rss_mib'] / r[small]['rss_mib']


def footprint(r):
    rs = {eco: ratio(r, eco) for eco in apps.PAIRS}
    # Node: соотношение у порога меняется от прогона к прогону (revision_stage2_session1) — только в полях ~
    return {'actual': {'java_full_starts_over_1_5x_slower': rs['java'][0] > 1.5,
                       'go_pair_within_2x': 0.5 < rs['go'][0] < 2},
            'start_ratio~': {eco: round(v[0], 2) for eco, v in rs.items()}, 'rss_ratio~': {eco: round(v[1], 2) for eco, v in rs.items()}}


def scalar(r, app, *path):
    x = r[app]
    for k in path:
        x = x[k]
    return x


def limits(r):
    by = {}
    for a, res in r.items():
        for size, code in res['limits'].items():
            by.setdefault(size, {})[a] = code
    # порог по серии: наибольший принятый размер и наименьший отвергнутый
    acc = {a: max([int(s) for s, c in res['limits'].items() if c == 201], default=None) for a, res in r.items()}
    rej = {a: min([int(s) for s, c in res['limits'].items() if c != 201], default=None) for a, res in r.items()}
    return {'actual': {'by_size': by}, 'accepted_max': acc, 'rejected_min': rej}


AXES = {
    'control': lambda r: {'actual': {'valid_post': per(r, 'control', 'valid_post'), 'valid_post_echoes_name': per(r, 'control', 'valid_post_echoes_name'),
                                     'valid_get': per(r, 'control', 'valid_get'), 'valid_get_returns_id': per(r, 'control', 'valid_get_returns_id'),
                                     'javalin_typed_valid_get': scalar(r, 'java-javalin', 'control', 'typed_valid_get')}},
    'routing': lambda r: {'actual': {**{k: per(r, 'routing', k) for k in ('wrong_method', 'trailing_slash', 'post_trailing_slash', 'post_redirect_body_kept',
                                                                            'bad_param', 'digits_prefix_param', 'zero_id', 'unknown_path')},
                                     'allow_present': {a: None if x['routing']['allow_on_405'] is None else bool(x['routing']['allow_on_405']) for a, x in r.items()},
                                     'javalin_typed_bad_param': scalar(r, 'java-javalin', 'routing', 'typed_bad_param')},
                          'allow': per(r, 'routing', 'allow_on_405'), 'post_redirect_location': per(r, 'routing', 'post_redirect_location')},
    'validation': lambda r: {'actual': {k: per(r, 'validation', k) for k in ('malformed_json', 'missing_name', 'empty_name', 'wrong_type', 'numeric_string', 'fractional', 'trailing_garbage')}},
    'limits': limits,
    'errors': lambda r: {'actual': {**{k: per(r, 'errors', k) for k in ('boom_status', 'boom_transport', 'boom_leaks_message_or_stack', 'server_discloses_software')},
                                    'detector_controls_ok': probe.detector_ok()},
                         'headers': {a: {'server': x['errors']['server_header'], 'x_powered_by': x['errors']['x_powered_by']} for a, x in r.items()},
                         'boom_body_head~': per(r, 'errors', 'boom_body_head')},
    'footprint': footprint,
}


def expected():
    hyp = json.loads((ROOT/'expected.json').read_text()); exp = dict(hyp['checks'])
    for k in sorted(k for k in hyp if k.startswith('revision')): exp.update(hyp[k].get('checks', {}))
    return exp


def main():
    if not sys.argv[1:]: (ROOT/'fixtures/RUN-COMPLETE').unlink(missing_ok=True)
    if shutil.disk_usage(ROOT).free < int(os.environ.get('MIN_FREE_GB', '5'))*1024**3: raise SystemExit('недостаточно места на хосте')
    exp = expected()
    names = sys.argv[1:] or list(exp)
    if sorted(exp) != sorted(AXES) and not sys.argv[1:]: raise RuntimeError('набор осей не совпал с гипотезами')
    for a in apps.APPS:
        apps.build(a)
    results = probe.probe_all()
    rows = []
    for key in names:
        value = AXES[key](results); actual = value.pop('actual')
        row = {'kind': 'checks', 'variant': key, 'probe': 'docker, 12 приложений, 127.0.0.1', 'cmd': 'python3 scripts/run-checks.py ' + key,
               'actual': actual, 'outcome': 'match' if actual == exp[key] else 'mismatch', 'detail': describe(key, actual, value), **value}
        rows.append(row); print(json.dumps({k: row[k] for k in ('variant', 'outcome', 'detail')}, ensure_ascii=False), flush=True)
        if row['outcome'] != 'match': print('расхождение', key, json.dumps(actual, ensure_ascii=False), file=sys.stderr, flush=True)
    if not sys.argv[1:]:
        (ROOT/'fixtures/checks.jsonl').write_text(''.join(json.dumps(r, ensure_ascii=False)+'\n' for r in rows)+'COMPLETE\n')
    if any(r['outcome'] != 'match' for r in rows):
        raise SystemExit('измерение противоречит гипотезе; фиксируйте revision_* до сессий')


if __name__ == '__main__': main()
