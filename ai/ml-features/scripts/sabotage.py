import json, os, shutil, subprocess, sys, tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def suite(root, test=None):
    argv=[sys.executable,'-m','unittest']
    argv+=['discover','-s','scripts','-p','test_*.py'] if test is None else [test]
    env=dict(os.environ, PYTHONPATH=str(root/'scripts'))
    return subprocess.run(argv,cwd=root,env=env,capture_output=True,text=True)
def main():
    failures=0; count=0
    with tempfile.TemporaryDirectory() as tmp:
        control=Path(tmp)/'control'
        shutil.copytree(ROOT,control,ignore=shutil.ignore_patterns('out','__pycache__'))
        p=suite(control)
        if p.returncode: print(p.stdout+p.stderr); return 1
        hyp=json.loads((ROOT/'expected.json').read_text()); exp=dict(hyp['checks'])
        for k in sorted(k for k in hyp if k.startswith('revision')): exp.update(hyp[k].get('checks',{}))
        keys=list(exp)
        for key in keys+['contract_complete','varying_values_are_marked','current_fixtures_match_last_session','marker']:
            if key=='marker' and os.environ.get('STAND_MARKER_PENDING')=='1': continue
            dest=Path(tmp)/key; shutil.copytree(control,dest)
            if key in ('varying_values_are_marked','current_fixtures_match_last_session'):
                files=sorted((dest/'fixtures/manifests').glob('*.json'))
                if key=='varying_values_are_marked' and len(files)<2:
                    print('пропущена: нужна вторая сессия'); continue
                target='test_marker.Manifests.test_'+key
                path=files[0] if key=='varying_values_are_marked' else files[-1]
                data=json.loads(path.read_text());axis=next(iter(data['published']));variant=next(iter(data['published'][axis]))
                data['published'][axis][variant]['actual']={'changed':True};path.write_text(json.dumps(data))
            elif key=='marker':
                (dest/'fixtures/RUN-COMPLETE').write_text('{"fixtures":{},"tests":0,"sabotages":0}')
                target='test_marker.Marker'
            else:
                path=dest/'fixtures/checks.jsonl'
                rows=[json.loads(l) for l in path.read_text().splitlines() if l.startswith('{')]
                if key=='contract_complete': rows.pop(); target='test_axes.Checks.test_contract_complete'
                else:
                    for r in rows:
                        if r['variant']==key: r['actual']={'deliberately_broken':True}
                    target='test_axes.Checks.test_'+key
                path.write_text(''.join(json.dumps(r)+'\n' for r in rows)+'COMPLETE\n')
            p=suite(dest,target); count+=1
            if p.returncode==0: failures+=1
            print(('покраснело: ' if p.returncode else 'ОСТАЛОСЬ ЗЕЛЁНЫМ: ')+key)
    print('диверсий: %d, не сработало: %d'%(count,failures))
    return bool(failures)
if __name__=='__main__': sys.exit(main())
