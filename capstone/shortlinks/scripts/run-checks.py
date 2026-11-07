"""Прогон осей стенда capstone/shortlinks → fixtures/checks.jsonl.

    python3 scripts/run-checks.py            → все оси, фикстура перезаписывается
    python3 scripts/run-checks.py cache      → одна ось, только вывод строки
"""
import json, os, shutil, sys, time
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'stand'))
from details import describe  # noqa: E402
import stack  # noqa: E402
import axes_compose  # noqa: E402
import axes_build  # noqa: E402
import axes_obs  # noqa: E402
import axes_k8s  # noqa: E402
ROOT = Path(__file__).resolve().parents[1]
AXES = dict(axes_compose.AXES)
AXES.update(axes_build.AXES)
AXES.update(axes_obs.AXES)
AXES.update(axes_k8s.AXES)
def expected():
    hyp = json.loads((ROOT/'expected.json').read_text()); exp = dict(hyp['checks'])
    for k in sorted(k for k in hyp if k.startswith('revision')): exp.update(hyp[k].get('checks', {}))
    return exp
def build():
    # сервис и probe — Go (корневой Dockerfile), consumer — Java (consumer-java/Dockerfile)
    for tag, argv in (('shortlinks-service:dev', ['--build-arg', 'CMD=service', str(ROOT)]),
                      ('shortlinks-probe:dev', ['--build-arg', 'CMD=probe', str(ROOT)]),
                      ('shortlinks-consumer:dev', [str(ROOT / 'consumer-java')])):
        for n in range(5):
            p = stack.sh(['docker', 'build', '-q', '-t', tag, *argv], check=False, timeout=1800)
            if p.returncode == 0: break
            time.sleep(10)
        else: raise RuntimeError('сборка %s: %s' % (tag, p.stderr[-1500:]))
def main():
    if not sys.argv[1:]: (ROOT/'fixtures/RUN-COMPLETE').unlink(missing_ok=True)
    if shutil.disk_usage(ROOT).free < int(os.environ.get('MIN_FREE_GB', '3'))*1024**3: raise SystemExit('недостаточно места на хосте')
    exp = expected()
    names = sys.argv[1:] or list(exp)
    if sorted(exp) != sorted(AXES) and not sys.argv[1:]: raise RuntimeError('набор осей не совпал с гипотезами')
    (ROOT/'out').mkdir(exist_ok=True)
    build()
    stack.down()
    rows = []
    try:
        for key in names:
            value = AXES[key](); actual = value.pop('actual')
            row = {'kind': 'checks', 'variant': key, 'probe': 'k8s-volga, ns %s' % axes_k8s.NS if key in axes_k8s.AXES else 'docker compose, 127.0.0.1', 'cmd': 'python3 scripts/run-checks.py ' + key,
                   'actual': actual, 'outcome': 'match' if actual == exp[key] else 'mismatch', 'detail': describe(key, actual, value), **value}
            rows.append(row); print(json.dumps({k: row[k] for k in ('variant', 'outcome', 'detail')}, ensure_ascii=False), flush=True)
            if row['outcome'] != 'match': print('расхождение', key, json.dumps(actual, ensure_ascii=False), file=sys.stderr, flush=True)
    finally:
        stack.down()
    if not sys.argv[1:]:
        (ROOT/'fixtures/checks.jsonl').write_text(''.join(json.dumps(r, ensure_ascii=False)+'\n' for r in rows)+'COMPLETE\n')
    if any(r['outcome'] != 'match' for r in rows):
        raise SystemExit('измерение противоречит гипотезе; фиксируйте revision_after_probe до сессий')
if __name__ == '__main__': main()
