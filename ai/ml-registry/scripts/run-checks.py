"""Прогон осей стенда ml-registry в контейнере → fixtures/checks.jsonl.

    python3 scripts/run-checks.py            → все проверки, фикстура перезаписывается
    python3 scripts/run-checks.py model      → одна проверка, только вывод строки
"""
import json, os, shutil, subprocess, sys, time
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
from details import describe  # noqa: E402
ROOT = Path(__file__).resolve().parents[1]
IMAGES = json.loads((ROOT/'images.json').read_text())
TAG = 'ai-ml-registry:local'
NET = 'ai-ml-registry-local'
def image(name):
    i = IMAGES[name]; return i['ref']+'@'+i['digest']
def call(argv, **kw):
    p = subprocess.run(argv, text=True, capture_output=True, **kw)
    if p.returncode: raise RuntimeError('command failed: '+repr(argv)+'\n'+p.stdout[-3000:]+'\n'+p.stderr[-3000:])
    return p.stdout
def pull(ref):
    for n in range(4):
        p = subprocess.run(['docker', 'pull', ref], capture_output=True, text=True)
        if p.returncode == 0: return
        print(p.stderr[-1000:], file=sys.stderr); time.sleep(2*(n+1))
    raise RuntimeError('не удалось получить '+ref)
def expected():
    hyp = json.loads((ROOT/'expected.json').read_text()); exp = dict(hyp['checks'])
    for k in sorted(k for k in hyp if k.startswith('revision')): exp.update(hyp[k].get('checks', {}))
    return exp
def main():
    if not sys.argv[1:]: (ROOT/'fixtures/RUN-COMPLETE').unlink(missing_ok=True)
    if shutil.disk_usage(ROOT).free < int(os.environ.get('MIN_FREE_GB', '2'))*1024**3: raise SystemExit('недостаточно места на хосте')
    for i in IMAGES: pull(image(i))
    if os.environ.get('FRESH') == '1' and (ROOT/'out').exists(): shutil.rmtree(ROOT/'out')
    (ROOT/'out').mkdir(exist_ok=True)
    call(['docker', 'build', '-q', '-t', TAG, str(ROOT)])
    def down():
        for n in ('ml-registry-pg', 'ml-registry-mlflow', 'ml-registry-measure'): subprocess.run(['docker', 'rm', '-f', n], capture_output=True)
        subprocess.run(['docker', 'network', 'rm', NET], capture_output=True)
    down()
    call(['docker', 'network', 'create', NET])
    try:
        call(['docker', 'run', '-d', '--name', 'ml-registry-pg', '--network', NET, '--network-alias', 'postgres', '--memory', '2g',
              '-e', 'POSTGRES_USER=mlflow', '-e', 'POSTGRES_PASSWORD=local-fixture-only', '-e', 'POSTGRES_DB=mlflow',
              image('postgres'), '-c', 'shared_buffers=256MB', '-c', 'work_mem=64MB'])
        for _ in range(60):  # барьер готовности: запрос проходит, а не «порт открыт»
            if subprocess.run(['docker', 'exec', 'ml-registry-pg', 'psql', '-U', 'mlflow', '-d', 'mlflow', '-tAc', 'SELECT 1'],
                              capture_output=True, text=True).stdout.strip() == '1': break
            time.sleep(1)
        else: raise RuntimeError('PostgreSQL не поднялся')
        # сервер MLflow: метаданные в PostgreSQL, артефакты на томе сервера (проксируются через него)
        call(['docker', 'run', '-d', '--name', 'ml-registry-mlflow', '--network', NET, '--network-alias', 'mlflow', '--memory', '2g',
              TAG, 'mlflow', 'server', '--backend-store-uri', 'postgresql+psycopg://mlflow:local-fixture-only@postgres:5432/mlflow',
              '--artifacts-destination', '/tmp/artifacts', '--host', '0.0.0.0', '--port', '5000',
              '--allowed-hosts', 'mlflow,mlflow:5000', '--workers', '2'])
        for _ in range(120):  # барьер: API отвечает, а не «процесс запущен»
            p = subprocess.run(['docker', 'exec', 'ml-registry-mlflow', 'python', '-c',
                                "import urllib.request as u;print(u.urlopen(u.Request('http://127.0.0.1:5000/health',headers={'Host':'mlflow:5000'})).status)"],
                               capture_output=True, text=True)
            if p.stdout.strip() == '200': break
            time.sleep(1)
        else: raise RuntimeError('MLflow не поднялся')
        stdout = call(['docker', 'run', '--rm', '--name', 'ml-registry-measure', '--network', NET, '--memory', '4g',
                       '-v', str(ROOT/'stand')+':/work/stand:ro',                        '-v', str(ROOT.parent/'ml-dataset/gen')+':/work/dataset:ro', '-w', '/work/stand', TAG, 'python', 'measure.py'])
    except RuntimeError:
        st = subprocess.run(['docker', 'inspect', '-f', '{{.State.Status}} OOM={{.State.OOMKilled}}', 'ml-registry-mlflow'], capture_output=True, text=True)
        print('сервер MLflow:', st.stdout.strip(), file=sys.stderr)
        print(subprocess.run(['docker', 'logs', '--tail', '20', 'ml-registry-mlflow'], capture_output=True, text=True).stderr[-3000:], file=sys.stderr)
        raise
    finally: down()
    (ROOT/'out/measure.json').write_text(stdout)
    data = json.loads([l for l in stdout.splitlines() if l.startswith('{')][-1])
    exp = expected()
    if sorted(data) != sorted(exp): raise RuntimeError('набор измерений не совпал')
    rows = []
    for key in sys.argv[1:] or list(exp):
        value = data[key]; actual = value.pop('actual')
        row = {'kind': 'checks', 'variant': key, 'probe': 'python, isolated Docker', 'cmd': 'python3 scripts/run-checks.py '+key,
               'actual': actual, 'outcome': 'match' if actual == exp[key] else 'mismatch', 'detail': describe(key, actual, value), **value}
        rows.append(row); print(json.dumps({k: row[k] for k in ('variant', 'outcome', 'detail')}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        (ROOT/'fixtures/checks.jsonl').write_text(''.join(json.dumps(r, ensure_ascii=False)+'\n' for r in rows)+'COMPLETE\n')
    if any(r['outcome'] != 'match' for r in rows):
        for r in rows:
            if r['outcome'] != 'match': print('расхождение', r['variant'], json.dumps(r['actual'], ensure_ascii=False), file=sys.stderr)
        raise SystemExit('измерение противоречит гипотезе; фиксируйте revision_after_probe до сессий')
if __name__ == '__main__': main()
