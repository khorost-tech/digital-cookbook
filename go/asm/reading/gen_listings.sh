#!/usr/bin/env bash
# Снимает дизасм-листинги для статьи «Как читать дизассемблер».
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p reading/listings

# Дизасм generic-цикла: показать, что компилятор НЕ векторизует.
go build -gcflags='-S' ./dotprod/ 2>reading/listings/dotprod_all.txt || true
# Только dotGeneric (блок от строки "...dotGeneric STEXT" до следующего STEXT).
# Пакетный скоуп -gcflags='-S=dotGeneric' не поддерживается на Go 1.26 —
# вместо этого фильтруем полный -S дамп по имени символа.
awk '/\.dotGeneric STEXT/{p=1} p && /STEXT/ && !/\.dotGeneric STEXT/{p=0} p' \
  reading/listings/dotprod_all.txt > reading/listings/dotGeneric.txt

# objdump рукописного AVX2 (символ dotAVX2) из тестового бинарника:
go test -c -o reading/listings/dotprod.test.exe ./dotprod/
go tool objdump -s 'dotAVX2' reading/listings/dotprod.test.exe > reading/listings/dotAVX2_objdump_raw.txt
rm -f reading/listings/dotprod.test.exe

# ИЗВЕСТНОЕ ОГРАНИЧЕНИЕ: на этом хосте/версии Go (1.26.3 windows/amd64)
# декодер `go tool objdump` (golang.org/x/arch/x86/x86asm) не распознаёт
# VEX-кодированные FMA3-инструкции (VFMADD231PD/VFMADD231SD). Байты читаются
# верно, но после первого VFMADD мнемоники "плывут" (мусорные RORB/ADCB/LRET…).
# Если доступен python (python3/python) + capstone — передекодируем те же
# самые байты корректно и пишем итоговый листинг в dotAVX2_objdump.txt. Иначе —
# используем сырой (потенциально "поплывший") вывод objdump как есть.
PY=""
for cand in python3 python; do
  if command -v "$cand" >/dev/null 2>&1 && "$cand" -c 'import capstone' >/dev/null 2>&1; then
    PY="$cand"
    break
  fi
done
if [ -n "$PY" ]; then
  "$PY" - "reading/listings/dotAVX2_objdump_raw.txt" "reading/listings/dotAVX2_objdump.txt" <<'PYEOF'
import sys, re, capstone

raw_path, out_path = sys.argv[1], sys.argv[2]
line_re = re.compile(r'^\s*(?P<src>\S+)\s+0x(?P<addr>[0-9a-f]+)\s+(?P<hex>[0-9a-f]+)\s+(?P<rest>.*)$')

with open(raw_path, encoding='utf-8') as f:
    lines = f.readlines()

header, rows, body_start = [], [], 0
for i, ln in enumerate(lines):
    m = line_re.match(ln)
    if m:
        body_start = i
        break
    header.append(ln.rstrip('\n'))
for ln in lines[body_start:]:
    m = line_re.match(ln)
    if m:
        rows.append((int(m.group('addr'), 16), m.group('hex'), m.group('src')))

if not rows:
    sys.exit("no instruction rows parsed from " + raw_path)

code = bytes.fromhex(''.join(r[1] for r in rows))
base = rows[0][0]
addr_to_src = {}
for addr, hx, src in rows:
    addr_to_src.setdefault(addr, src)

md = capstone.Cs(capstone.CS_ARCH_X86, capstone.CS_MODE_64)
decoded = list(md.disasm(code, base))

out = header + [
    '',
    '# NOTE: go tool objdump (golang.org/x/arch/x86/x86asm) не декодирует VEX FMA3',
    '# (VFMADD231PD/SD) на этом хосте/версии Go — байты верны, мнемоники после',
    '# первого VFMADD "плывут" (см. dotAVX2_objdump_raw.txt). Здесь те же байты',
    '# передекодированы Capstone 5 (корректная поддержка VEX/AVX2/FMA3).',
    '',
]
for insn in decoded:
    src = addr_to_src.get(insn.address, '')
    out.append(f"  {src}\t0x{insn.address:x}\t{insn.bytes.hex()}\t{insn.mnemonic} {insn.op_str}".rstrip())

with open(out_path, 'w', encoding='utf-8') as f:
    f.write('\n'.join(out) + '\n')
print(f"capstone: передекодировано {len(decoded)} инструкций -> {out_path}")
PYEOF
else
  echo "capstone недоступен (pip install capstone) — используем сырой вывод go tool objdump как есть" >&2
  {
    echo "# ПРЕДУПРЕЖДЕНИЕ: go tool objdump на этом хосте не декодирует VEX FMA3"
    echo "# (VFMADD231PD/SD) корректно — возможны мусорные мнемоники после первого"
    echo "# VFMADD. Установите capstone (pip install capstone) и перезапустите скрипт"
    echo "# для корректного листинга. См. reading/README.md."
    echo
    cat reading/listings/dotAVX2_objdump_raw.txt
  } > reading/listings/dotAVX2_objdump.txt
fi

echo "Листинги в reading/listings/"
