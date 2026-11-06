"""Прогон ядер на двух хостах → словарь результатов для scripts/run-checks.py.

mac       — этот Mac (Apple M5, arm64), нативно: clang -O2 и Go из go.mod;
devproxy  — Linux-ВМ с CPU type host (AMD Zen 4, amd64), образ стенда в docker
            --privileged: там же perf stat с аппаратными счётчиками.

Хост devproxy задаётся DOD_REMOTE (по умолчанию user@192.168.71.49). Все ядра
одного хоста идут одним скриптом подряд, по одному процессу на ядро.
"""
import json
import os
import re
import shlex
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REMOTE = os.environ.get('DOD_REMOTE', 'user@192.168.71.49')
IMAGE = 'dod-stand:1'
DOCKER = os.environ.get('DOD_DOCKER', 'sudo -n docker')  # пользователь devproxy не в группе docker
HOSTS = ('mac', 'devproxy')
LANGS = ('c', 'go')
KB, MB = 1024, 1024 * 1024

CHASE_WS = (16 * KB, 32 * KB, 256 * KB, 4 * MB, 64 * MB, 256 * MB)
SEQ_WS = 256 * MB
SMALL_N, LARGE_N = 2048, 2 * MB          # частиц по 64 байта: 128 КБ и 128 МБ
FS_DIST = (8, 16, 32, 64, 128, 256)
BR_N = 4 * MB
BRANCH_KERNELS = ('branch', 'branchless', 'filter', 'filter_branchless')


def plan():
    """(ядро, аргументы) — одинаково для обоих языков и хостов."""
    runs = [('chase', 'ws=%d' % ws) for ws in CHASE_WS]
    runs.append(('seq', 'ws=%d' % SEQ_WS))
    for n in (SMALL_N, LARGE_N):
        for fields in (1, 8):
            runs += [('aos', 'n=%d fields=%d' % (n, fields)), ('soa', 'n=%d fields=%d' % (n, fields)),
                     ('ptr', 'n=%d fields=%d shuffled=0' % (n, fields)),
                     ('ptr', 'n=%d fields=%d shuffled=1' % (n, fields))]
    runs += [('cross', 'n=%d split=%d' % (n, sp)) for n in (SMALL_N, LARGE_N) for sp in (0, 1)]
    runs += [('fs', 'dist=%d' % d) for d in FS_DIST]
    runs += [(k, 'n=%d sorted=%d' % (BR_N, s)) for k in BRANCH_KERNELS for s in (0, 1)]
    return runs


# perf stat: ядро минус подготовка. reps=0 — только setup, reps=3 — setup,
# прогрев и три прогона: разность делится на 4 прогона ядра.
PERF_EVENTS = 'cycles,instructions,cache-references,cache-misses,branch-misses'
PERF_REPS = 3
PERF_RUNS = [('chase', 'ws=%d' % (16 * KB)), ('chase', 'ws=%d' % (256 * MB)),
             ('aos', 'n=%d fields=1' % LARGE_N), ('soa', 'n=%d fields=1' % LARGE_N),
             ('branch', 'n=%d sorted=0' % BR_N), ('branch', 'n=%d sorted=1' % BR_N),
             ('branchless', 'n=%d sorted=0' % BR_N),
             ('filter', 'n=%d sorted=0' % BR_N), ('filter', 'n=%d sorted=1' % BR_N),
             ('filter_branchless', 'n=%d sorted=0' % BR_N)]


def inner_runs(ops):
    """Прогонов ядра в одном замере — та же формула, что в c/dod.c и go/main.go."""
    return 1 if ops >= 1 << 24 else ((1 << 24) + ops - 1) // ops


def script(host):
    b = {'c': './dod-c', 'go': './dod-go'} if host == 'devproxy' else {'c': 'out/mac/dod-c', 'go': 'out/mac/dod-go'}
    lines = ['set -e']
    if host == 'mac':
        lines += ['echo "@@sys line $(sysctl -n hw.cachelinesize)"',
                  'echo "@@sys cpu $(sysctl -n machdep.cpu.brand_string)"',
                  'echo "@@sys cc $(clang --version | head -1)"']
    else:
        lines += ['echo "@@sys line $(cat /sys/devices/system/cpu/cpu0/cache/index0/coherency_line_size)"',
                  'echo "@@sys cpu $(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2- | sed "s/^ //")"',
                  'echo "@@sys cc $(clang --version | head -1)"',
                  'echo "@@sys perf $(perf --version)"']
    lines.append('echo "@@sys go $(go version %s | cut -d" " -f2-)"' % b['go'])
    lines.append('%s warmup >/dev/null' % b['c'])  # первый замер не должен идти на холодной частоте
    lines.append('%s pad' % b['go'])
    # отрицательный контроль сверки сумм: другое зерно обязано дать другую сумму
    lines.append('%s seq ws=%d seed=42 reps=1' % (b['go'], MB))
    lines.append('%s seq ws=%d seed=43 reps=1' % (b['go'], MB))
    for kernel, args in plan():
        for lang in LANGS:
            lines.append('%s %s %s' % (b[lang], kernel, args))
    # машинный код тем же компилятором, что собирает ядра: строки с условием — в codegen()
    lines += ['echo "@@asm c"', 'clang -O2 -g -S -o - c/dod.c', 'echo "@@asm end"',
              'echo "@@asm go"', '(cd go && go build -a -gcflags=dod=-S -o /dev/null . 2>&1)', 'echo "@@asm end"']
    if host == 'devproxy':
        for kernel, args in PERF_RUNS:
            for lang in LANGS:
                for reps in (0, PERF_REPS):
                    tag = '%s %s %s reps=%d' % (lang, kernel, args, reps)
                    lines.append("perf stat -x';' -e %s -o /tmp/p -- %s %s %s reps=%d >/dev/null"
                                 % (PERF_EVENTS, b[lang], kernel, args, reps))
                    lines.append('echo "@@perf %s"; cat /tmp/p' % tag)
    return '\n'.join(lines) + '\n'


def build_mac():
    out = ROOT / 'out' / 'mac'
    out.mkdir(parents=True, exist_ok=True)
    subprocess.run(['clang', '-O2', '-pthread', '-o', str(out / 'dod-c'), 'c/dod.c'], cwd=ROOT, check=True)
    subprocess.run(['go', 'build', '-o', str(out / 'dod-go'), '.'], cwd=ROOT / 'go', check=True,
                   env=dict(os.environ, CGO_ENABLED='0'))


def build_remote():
    tar = subprocess.run(['tar', '-czf', '-', '--exclude', 'out', 'Dockerfile', 'c', 'go'], cwd=ROOT,
                         check=True, capture_output=True, env=dict(os.environ, COPYFILE_DISABLE='1')).stdout
    subprocess.run(['ssh', '-o', 'BatchMode=yes', REMOTE, '%s build -q -t %s -' % (DOCKER, IMAGE)],
                   input=tar, check=True, capture_output=True)


def run(host):
    s = script(host)
    if host == 'mac':
        p = subprocess.run(['bash', '-c', s], cwd=ROOT, capture_output=True, text=True)
    else:
        remote = '%s run --rm -i --privileged %s bash -c %s' % (DOCKER, IMAGE, shlex.quote(s))
        p = subprocess.run(['ssh', '-o', 'BatchMode=yes', REMOTE, remote], capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError('%s: код %d\n%s' % (host, p.returncode, p.stderr[-2000:]))
    return parse(p.stdout)


def parse(text):
    res = {'sys': {}, 'runs': {}, 'perf': {}, 'perf_coverage': {}, 'pad': None, 'seed': {}, 'asm': {}}
    tag, asm = None, None
    for line in text.splitlines():
        if line.startswith('@@asm '):
            asm = None if line == '@@asm end' else line[len('@@asm '):]
            if asm:
                res['asm'][asm] = []
        elif asm:
            res['asm'][asm].append(line)
        elif line.startswith('@@sys '):
            _, key, value = line.split(' ', 2)
            res['sys'][key] = value.strip()
        elif line.startswith('@@perf '):
            tag = line[len('@@perf '):]
            res['perf'][tag] = {}
            res['perf_coverage'][tag] = {}
        elif line.startswith('{'):
            row = json.loads(line)
            if row['kernel'] == 'warmup':
                continue
            if row['kernel'] == 'pad':
                res['pad'] = row
            elif 'seed=' in row['args']:
                res['seed'][row['args']] = row
            else:
                res['runs']['%s %s %s' % (row['lang'], row['kernel'], row['args'])] = row
        elif tag and ';' in line:
            # -x';': значение;единица;событие;время работы счётчика;доля времени, %;…
            # Доля меньше 100 — perf мультиплексировал счётчики и досчитал значение по выборке.
            f = line.split(';')
            m = re.match(r'^\d+$', f[0])
            res['perf'][tag][f[2]] = int(f[0]) if m else None
            res['perf_coverage'][tag][f[2]] = float(f[4]) if len(f) > 4 and re.match(r'^[\d.]+$', f[4]) else None
    return res


# Строки, где стоит условие ядра: в C — целиком, в Go — после метки case.
C_LINES = {'branch': 'if (br_data[i] >= 128) s += br_data[i];',
           'filter': 'if (br_data[i] >= 128) br_out[k++] = br_data[i];',
           'filter_branchless': 'k += br_data[i] >= 128;'}
GO_LINES = {'branch': ('case "branch":', 'if v >= 128 {'),
            'filter': ('case "filter":', 'if v >= 128 {'),
            'filter_branchless': ('case "filter_branchless":', 'k += int((uint(v) >> 7) & 1)')}
C_JUMP = re.compile(r'^\s+(j(?!mp\b)[a-z]+|b\.[a-z]{2}|cbn?z|tbn?z)\s')
GO_JUMP = re.compile(r'\s(J(?!MP\b)[A-Z]+|B(EQ|NE|LO|HS|HI|LS|LT|GE|GT|LE|CC|CS|MI|PL|VS|VC)|CBN?Z|CBN?ZW|TBN?Z)\s')


def source_line(path, needle, after=None):
    text = (ROOT / path).read_text().splitlines()
    start = next(i for i, l in enumerate(text) if after in l) if after else 0
    return next(i for i in range(start, len(text)) if needle in text[i]) + 1


C_SIMD = re.compile(r'%[xyz]mm\d|\bv\d+\.\d*[bhsd]\b|\bq\d+\b')


def jumps_by_line(lang, asm, pattern=None):
    """{строка исходника: есть ли среди её инструкций условный переход (или совпадение с pattern)}."""
    out = {}
    if lang == 'c':
        files = {m.group(1) for l in asm for m in [re.match(r'^\s+\.file\s+(\d+)\s+.*"(?:c/)?dod\.c"', l)] if m}
        cur = None
        for l in asm:
            m = re.match(r'^\s+\.loc\s+(\d+)\s+(\d+)', l)
            if m:
                cur = int(m.group(2)) if m.group(1) in files else None
            elif cur and not l.lstrip().startswith(('.', '#', ';', '@')) and l.startswith(('\t', ' ')):
                hit = pattern.search(l) if pattern else C_JUMP.match(l)
                out[cur] = out.get(cur, False) or bool(hit)
    else:
        for l in asm:
            m = re.search(r'main\.go:(\d+)\)\s+(\S+)', l)
            if m:
                ln = int(m.group(1))
                out[ln] = out.get(ln, False) or bool(GO_JUMP.search(' ' + m.group(2) + ' '))
    return out


def codegen(res):
    """{ядро: условный переход на строке условия} для C и Go этого хоста."""
    out = {}
    for lang in LANGS:
        found = jumps_by_line(lang, res['asm'].get(lang, []))
        lines = ({k: source_line('c/dod.c', v) for k, v in C_LINES.items()} if lang == 'c' else
                 {k: source_line('go/main.go', v[1], v[0]) for k, v in GO_LINES.items()})
        out[lang] = {k: found.get(ln) for k, ln in lines.items()}
    # векторные регистры на строке суммы в C: clang векторизовал цикл
    vec = jumps_by_line('c', res['asm'].get('c', []), C_SIMD)
    out['c_vector'] = {k: vec.get(source_line('c/dod.c', v)) for k, v in C_LINES.items()}
    return out


def pmu_without_privileged():
    """Отрицательный контроль: тот же образ без --privileged счётчиков не получает."""
    p = subprocess.run(['ssh', '-o', 'BatchMode=yes', REMOTE,
                        "%s run --rm %s perf stat -x';' -e cycles -- true" % (DOCKER, IMAGE)],
                       capture_output=True, text=True)
    m = re.search(r'^(\d+);[^;]*;cycles', p.stderr, re.M)
    return bool(m and int(m.group(1)) > 0)


def measure():
    build_mac()
    build_remote()
    results = {host: run(host) for host in HOSTS}
    results['devproxy']['pmu_without_privileged'] = pmu_without_privileged()
    return results


def cleanup_remote():
    subprocess.run(['ssh', '-o', 'BatchMode=yes', REMOTE, '%s image rm -f %s' % (DOCKER, IMAGE)], capture_output=True)
