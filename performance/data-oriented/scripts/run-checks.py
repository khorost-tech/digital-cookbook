"""Прогон осей стенда performance/data-oriented → fixtures/checks.jsonl.

    python3 scripts/run-checks.py              → все оси, фикстура перезаписывается
    python3 scripts/run-checks.py hierarchy    → одна ось, только вывод строки

Одни и те же ядра на C и Go, два хоста: mac (Apple M5, линия 128 байт) и devproxy (AMD Zen 4 в ВМ,
линия 64 байта, аппаратные счётчики). Публикуются отношения, корзины и пороги; медианы нс — только в полях «~».
"""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'stand'))
from details import describe  # noqa: E402
import bench  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
H, L = bench.HOSTS, bench.LANGS
KB, MB = bench.KB, bench.MB


def bucket(x, edges):
    """Корзина по порогам: edges=(1.5, 3) → '<1.5', '1.5-3', '>3'."""
    if x < edges[0]:
        return '<%g' % edges[0]
    for lo, hi in zip(edges, edges[1:]):
        if x < hi:
            return '%g-%g' % (lo, hi)
    return '>%g' % edges[-1]


def ns(r, host, lang, kernel, args):
    return r[host]['runs']['%s %s %s' % (lang, kernel, args)]['ns_per_op']


def per(f):
    """{хост: {язык: f(хост, язык)}}."""
    return {h: {lang: f(h, lang) for lang in L} for h in H}


def control(r):
    def agree(h):
        runs = r[h]['runs']
        return all(runs['c ' + k[3:]]['check'] == runs[k]['check'] for k in runs if k.startswith('go '))
    seed = lambda h, s: r[h]['seed']['ws=%d seed=%d reps=1' % (MB, s)]['check']
    cycles = [v.get('cycles') for v in r['devproxy']['perf'].values()]
    return {'actual': {'checksums_agree_c_go': {h: agree(h) for h in H},
                       'other_seed_changes_checksum': {h: seed(h, 42) != seed(h, 43) for h in H},
                       'line_bytes': {h: int(r[h]['sys']['line']) for h in H},
                       'go_cache_line_pad': {h: r[h]['pad']['cache_line_pad'] for h in H},
                       'goarch': {h: r[h]['pad']['goarch'] for h in H},
                       'pmu_counts_cycles_privileged': bool(cycles) and all(c and c > 0 for c in cycles),
                       'pmu_counts_cycles_unprivileged': r['devproxy']['pmu_without_privileged']},
            'sys': {h: r[h]['sys'] for h in H}}


def hierarchy(r):
    def t(h, lang):
        return [ns(r, h, lang, 'chase', 'ws=%d' % ws) for ws in bench.CHASE_WS]

    def seq_line(h, lang):  # нс на 128 байт последовательного обхода
        return ns(r, h, lang, 'seq', 'ws=%d' % bench.SEQ_WS) * 16

    def row(h, lang):
        x = t(h, lang)
        return {'l1_plateau_32k_vs_16k': x[1] / x[0] < 1.3,
                'grows_to_ram': all(b >= 0.9 * a for a, b in zip(x, x[1:])) and x[-1] > 5 * x[0],
                'ram_over_l1': bucket(x[-1] / x[0], (10, 50, 200)),
                'random_over_seq_per_128b_at_256m_over_10x': x[-1] / seq_line(h, lang) > 10}
    return {'actual': per(row),
            'chase_over_16k~': per(lambda h, lang: [round(v / t(h, lang)[0], 1) for v in t(h, lang)]),
            'chase_ws_bytes': list(bench.CHASE_WS),
            'random_over_seq_per_128b~': per(lambda h, lang: round(t(h, lang)[-1] / seq_line(h, lang), 1))}


def _ratio(r, h, lang, a, b):
    return ns(r, h, lang, *a) / ns(r, h, lang, *b)


def aos_soa(r):
    """Пороги с запасом от наблюдаемого разброса: «равны» — в пределах 0.8–1.25, «быстрее» — больше чем на 25%."""
    big, small = bench.LARGE_N, bench.SMALL_N
    q = lambda k, n, f, extra='': (k, ('n=%d fields=%d' % (n, f)) + extra)
    pairs = {'large_one_field_aos_over_soa': (q('aos', big, 1), q('soa', big, 1)),
             'large_all_fields_aos_over_soa': (q('aos', big, 8), q('soa', big, 8)),
             'small_one_field_aos_over_soa': (q('aos', small, 1), q('soa', small, 1)),
             'large_ptr_shuffled_over_aos': (q('ptr', big, 1, ' shuffled=1'), q('aos', big, 1)),
             'large_ptr_in_order_over_aos': (q('ptr', big, 1, ' shuffled=0'), q('aos', big, 1)),
             'large_cross_aos_over_split': (('cross', 'n=%d split=0' % big), ('cross', 'n=%d split=1' % big)),
             'small_cross_aos_over_split': (('cross', 'n=%d split=0' % small), ('cross', 'n=%d split=1' % small))}

    def row(h, lang):
        x = {k: _ratio(r, h, lang, a, b) for k, (a, b) in pairs.items()}
        same = lambda v: 0.8 < v < 1.25
        return {'soa_faster_large_one_field': x['large_one_field_aos_over_soa'] > 1.25,
                'soa_no_gain_large_all_fields': x['large_all_fields_aos_over_soa'] < 1.25,
                'same_small_one_field': same(x['small_one_field_aos_over_soa']),
                'ptr_shuffled_over_3x': x['large_ptr_shuffled_over_aos'] > 3,
                'ptr_in_order_under_3x': x['large_ptr_in_order_over_aos'] < 3,
                'hot_cold_split_faster_large': x['large_cross_aos_over_split'] > 1.5}
    return {'actual': per(row),
            'ratios~': per(lambda h, lang: {k: round(_ratio(r, h, lang, a, b), 2) for k, (a, b) in pairs.items()})}


def false_sharing(r):
    rel = lambda h, lang, d: _ratio(r, h, lang, ('fs', 'dist=%d' % d), ('fs', 'dist=256'))
    near = [d for d in bench.FS_DIST if d != 256]
    return {'actual': per(lambda h, lang: {'slower_than_256_at_%d' % d: rel(h, lang, d) > 2 for d in near}),
            'over_256~': per(lambda h, lang: {str(d): round(rel(h, lang, d), 2) for d in near})}


def branch(r):
    n = bench.BR_N
    rel = lambda h, lang, k: _ratio(r, h, lang, (k, 'n=%d sorted=0' % n), (k, 'n=%d sorted=1' % n))
    # прямое сравнение на одних и тех же данных: отбор с условием к отбору без условия
    vs = lambda h, lang, s: _ratio(r, h, lang, ('filter', 'n=%d sorted=%d' % (n, s)), ('filter_branchless', 'n=%d sorted=%d' % (n, s)))

    def row(h, lang):
        out = {'%s_unsorted_over_sorted' % k: bucket(rel(h, lang, k), (1.5, 3)) for k in bench.BRANCH_KERNELS}
        out['filter_over_branchless_unsorted_over_2x'] = vs(h, lang, 0) > 2
        out['filter_over_branchless_sorted_under_1.5x'] = vs(h, lang, 1) < 1.5
        return out
    return {'actual': per(row),
            'ratios~': per(lambda h, lang: {k: round(rel(h, lang, k), 2) for k in bench.BRANCH_KERNELS}),
            'filter_over_branchless~': per(lambda h, lang: {'unsorted': round(vs(h, lang, 0), 2), 'sorted': round(vs(h, lang, 1), 2)})}


def codegen(r):
    """Машинный код тем компилятором, что собирает ядра на хосте: есть ли условный переход на строке условия."""
    def row(h):
        c = bench.codegen(r[h])
        return {'jump_on_condition_line': {lang: c[lang] for lang in L}, 'c_sum_vectorized': c['c_vector']['branch']}
    return {'actual': {h: row(h) for h in H}}


def counters(r):
    """Только devproxy: (счётчик при reps=3 − при reps=0) / (прогоны ядра × операции)."""
    p = r['devproxy']['perf']

    def per_op(lang, kernel, args, event):
        ops = (1 << 22) if kernel == 'chase' else int(dict(a.split('=') for a in args.split())['n'])
        hi = p['%s %s %s reps=%d' % (lang, kernel, args, bench.PERF_REPS)][event]
        lo = p['%s %s %s reps=0' % (lang, kernel, args)][event]
        # прогрев и PERF_REPS замеров, в каждом inner прогонов ядра
        return (hi - lo) / ((bench.PERF_REPS + 1) * bench.inner_runs(ops) * ops)
    big, n = bench.LARGE_N, bench.BR_N

    def row(lang):
        miss = lambda k, a: per_op(lang, k, a, 'cache-misses')
        bm = lambda k, a: per_op(lang, k, a, 'branch-misses')
        ref = lambda k, a: per_op(lang, k, a, 'cache-references')
        one = 'n=%d fields=1' % big
        return {'chase_misses_per_access_16k': bucket(miss('chase', 'ws=%d' % (16 * KB)), (0.05, 0.5)),
                'chase_misses_per_access_256m': bucket(miss('chase', 'ws=%d' % (256 * MB)), (0.05, 0.5)),
                'aos_misses_per_elem_large': bucket(miss('aos', one), (0.1, 0.5)),
                'aos_over_soa_references_large_over_4x': ref('aos', one) / ref('soa', one) > 4,
                'filter_misses_per_elem_unsorted': bucket(bm('filter', 'n=%d sorted=0' % n), (0.05, 0.3)),
                'filter_misses_per_elem_sorted': bucket(bm('filter', 'n=%d sorted=1' % n), (0.05, 0.3)),
                'filter_branchless_misses_per_elem_unsorted': bucket(bm('filter_branchless', 'n=%d sorted=0' % n), (0.05, 0.3)),
                'branch_misses_per_elem_unsorted': bucket(bm('branch', 'n=%d sorted=0' % n), (0.05, 0.3)),
                'branch_misses_per_elem_sorted': bucket(bm('branch', 'n=%d sorted=1' % n), (0.05, 0.3)),
                'branchless_misses_per_elem_unsorted': bucket(bm('branchless', 'n=%d sorted=0' % n), (0.05, 0.3))}

    def raw(lang):
        out = {}
        for k, a in bench.PERF_RUNS:
            for ev in ('cache-references', 'cache-misses', 'branch-misses', 'instructions'):
                out['%s %s %s' % (k, a, ev)] = round(per_op(lang, k, a, ev), 4)
        return out
    ratio = lambda lang: round(per_op(lang, 'aos', 'n=%d fields=1' % big, 'cache-references')
                               / per_op(lang, 'soa', 'n=%d fields=1' % big, 'cache-references'), 2)
    cov = r['devproxy']['perf_coverage']
    full = lambda lang: all((v or 0) == 100.0 for t, evs in cov.items() if t.startswith(lang + ' ') for v in evs.values())
    rows = {lang: dict(row(lang), events_counted_full_time=full(lang)) for lang in L}
    return {'actual': rows, 'per_op~': {lang: raw(lang) for lang in L},
            'min_coverage_percent': {lang: min((v or 0) for t, evs in cov.items() if t.startswith(lang + ' ') for v in evs.values()) for lang in L},
            'aos_over_soa_references~': {lang: ratio(lang) for lang in L}}


AXES = {'control': control, 'hierarchy': hierarchy, 'aos_soa': aos_soa, 'false_sharing': false_sharing,
        'branch': branch, 'codegen': codegen, 'counters': counters}


def expected():
    hyp = json.loads((ROOT / 'expected.json').read_text())
    exp = dict(hyp['checks'])
    for k in sorted(k for k in hyp if k.startswith('revision')):
        exp.update(hyp[k].get('checks', {}))
    return exp


def main():
    if not sys.argv[1:]:
        (ROOT / 'fixtures/RUN-COMPLETE').unlink(missing_ok=True)
    exp = expected()
    names = sys.argv[1:] or list(exp)
    if sorted(exp) != sorted(AXES) and not sys.argv[1:]:
        raise RuntimeError('набор осей не совпал с гипотезами')
    results = bench.measure()
    (ROOT / 'out').mkdir(exist_ok=True)
    (ROOT / 'out/raw.json').write_text(json.dumps(results, ensure_ascii=False, indent=1))
    rows = []
    for key in names:
        value = AXES[key](results)
        actual = value.pop('actual')
        row = {'kind': 'checks', 'variant': key, 'probe': 'ядра на C и Go, хосты mac и devproxy',
               'cmd': 'python3 scripts/run-checks.py ' + key, 'actual': actual,
               'outcome': 'match' if actual == exp[key] else 'mismatch', 'detail': describe(key, actual), **value}
        rows.append(row)
        print(json.dumps({k: row[k] for k in ('variant', 'outcome', 'detail')}, ensure_ascii=False), flush=True)
        if row['outcome'] != 'match':
            print('расхождение', key, json.dumps(actual, ensure_ascii=False), file=sys.stderr, flush=True)
    if not sys.argv[1:]:
        (ROOT / 'fixtures/checks.jsonl').write_text(
            ''.join(json.dumps(r, ensure_ascii=False) + '\n' for r in rows) + 'COMPLETE\n')
    if any(r['outcome'] != 'match' for r in rows):
        raise SystemExit('измерение противоречит гипотезе; фиксируйте revision_* до сессий')


if __name__ == '__main__':
    main()
