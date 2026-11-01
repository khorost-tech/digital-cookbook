# ebpf/fundamentals — eBPF-программы против счёта нагрузки

Стенд к статье [«eBPF с нуля: как это работает и зачем бэкендеру»](https://khorost.tech/infrastructure/ebpf-fundamentals/).
Один контейнер `tools`: Go 1.27 + [`cilium/ebpf`](https://github.com/cilium/ebpf)
0.22 (программы на C собирает `bpf2go`), clang/llvm 19, libbpf 1.5, bpftool 7.5,
bpftrace 0.23. Нагрузка `cmd/load` делает ровно столько событий, сколько
сказано, и печатает свой счёт — eBPF-программы сверяются с ним.

**Почему `privileged` и `pid: host`.** Загрузка программ и монтирование
debugfs/tracefs требуют `CAP_SYS_ADMIN`/`CAP_BPF`; программа фильтрует события
по tgid из `bpf_get_current_pid_tgid()` — PID нагрузки должен быть в том же
пространстве, что видит ядро. «Хост» для eBPF — ядро VM Docker (на Mac —
LinuxKit 7.0, aarch64, BTF есть), не macOS.

## Что внутри

```
cmd/load/          нагрузка: open N | call N | callgrow N | burst N K — счёт в RESULT
internal/loadproc/ запуск нагрузки: READY <pid> → программы прицеплены → go → RESULT
bpf/hooks.c        один счётчик на разных точках подключения
bpf/maps.c         hash (неатомарно и атомарно), per-CPU, ring buffer со счётом отказов
bpf/verifier/*.c   программы с ожидаемым вердиктом в первой строке
bpf/core*.c        CO-RE: tgid, state/__state с bpf_core_field_exists, программа под старые ядра
cmd/hooks|maps|verifier|core|bt  загрузчики осей (bt — bpftrace)
scripts/           раннеры, разбор с контролями, проверки, диверсии
fixtures/          замеры; manifests/ — сводки сессий; RUN-COMPLETE — маркер
expected.json      гипотезы до прогона, факты проб, расхождения
```

`bpf/vmlinux.h`, выход `bpf2go` и `bin/` собираются в контейнере при прогоне и
в репозиторий не входят.

## Как прогнать

Нужны Docker (Linux-ядро с BTF; Docker Desktop подходит) и Python 3.8+.

```sh
bash scripts/run-all.sh                      # всё разом, маркер последним
FRESH=1 bash scripts/run-all.sh              # то же с пустых томов и пересборкой
python3 scripts/run-hooks.py uretprobe-grow  # один вариант
python3 -m unittest discover -s scripts -p "test_*.py"
python3 scripts/sabotage.py
```

## Что замерено

Две сессии полного прогона (вторая — с пустых томов и пересборкой) совпали по
опубликованному (`fixtures/manifests/`).

**Ось 1 — hooks** (`hooks.jsonl`). 10 000 открытий файла: tracepoint
`sys_enter_openat`, kprobe и fentry на `do_sys_openat2` — ровно 10 000. uprobe и
uretprobe на листовой Go-функции — 10 000. На функции, чей кадр больше
начального стека горутины: uprobe — по 2 срабатывания на вызов (пролог уходит в
`morestack` и выполняется заново; изредка ещё одно — точный счёт вне сравнения
сессий); uretprobe — нагрузка падает: `unexpected return pc`.

**Ось 2 — maps** (`maps.jsonl`). 1 000 000 `getppid` из 8 потоков: hash с
неатомарным инкрементом теряет события — в прогонах стенда больше половины (величина от прогона к прогону
разная); атомарный инкремент и per-CPU — ровно 1 000 000. Ring buffer 4 КБ без
чтения во время нагрузки — 255 событий, с чтением — потери есть; 32 МБ — без
потерь. Получено + отказов `bpf_ringbuf_reserve` = нагрузке во всех вариантах.

**Ось 3 — verifier** (`verifier.jsonl`). 12 программ. Против гипотезы приняты:
разыменование из array-карты с константным ключом (verifier знает, что NULL не
будет), цикл, который clang свернул в одну инструкцию, чтение
неинициализированного стека (разрешено привилегированному загрузчику).

**Ось 4 — CO-RE** (`core.jsonl`). `tgid` через CO-RE = PID нагрузки; с
`bpf_core_field_exists` выбрано поле `__state` (ядро ≥ 5.14) и прочитан 0;
программа под старые ядра (`state` без проверки) не загружается:
`bad CO-RE relocation`.

**Ось 5 — bpftrace** (`bpftrace.jsonl`). Однострочники на тех же событиях —
ровно эталон, в том числе 1 000 000 из 8 потоков: `count()` хранит счёт в
per-CPU карте.

## Чего эти числа не значат

- **Одно ядро.** Переносимость CO-RE показана на переименованном поле, а не на
  втором ядре.
- **Ловушки Go зависят от версий** ядра и Go: uretprobe на Go-функциях с ростом
  стека — известная проблема, её обходят uprobe на инструкциях `RET`.
- **Время не мерилось** — только счёт; цена программ — тема стенда
  `ebpf/observability`.
- **Версии:** Go 1.27.1, cilium/ebpf 0.22.0, clang 19, libbpf 1.5.0, bpftool
  7.5.0, bpftrace 0.23.2, ядро 7.0.12-linuxkit (aarch64).
