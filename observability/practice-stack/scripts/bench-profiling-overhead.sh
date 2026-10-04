#!/usr/bin/env bash
# Накладные расходы НЕПРЕРЫВНОГО ПРОФИЛИРОВАНИЯ — сверх телеметрии, а не вместо неё.
#
#   ./scripts/bench-profiling-overhead.sh [запросов] [повторов]
#   ./scripts/bench-profiling-overhead.sh report      # только отчёт из results/.profbench
#
# Три режима, у всех трёх телеметрия ВКЛЮЧЕНА. Сравнивается именно цена профилей:
#   noprof — только OTel (трейсы, метрики, логи)
#   go     — плюс профилирование Go (pyroscope-go + метки otelpyroscope)
#   both   — плюс профилирование Java (второй javaagent)
#
# Дисциплина замера — та же, что в bench-overhead.sh волны 1, и она не формальность:
#  1. Перед каждым замером стенд поднимается заново с ЧИСТЫМИ томами.
#  2. Прогрев и пауза: у JVM без прогрева измеряется разогрев, а не работа.
#  3. Ресурсы снимаются ЦИКЛОМ `docker stats --no-stream`. Потоковый `docker stats`
#     на этой машине под нагрузкой отдавал прочерки в 97% строк.
#  4. Первый снимок отбрасывается — он снят до разгона нагрузки.
#  5. Числа приводятся к числам в awk через +0: без этого сравнение идёт как со
#     строками, и "104.32" оказывается меньше "8.7". Признак такой ошибки —
#     среднее больше максимума, и скрипт проверяет это сам.
#  6. Каждый режим прогоняется дважды, в отчёт идут ОБА числа. Если разброс между
#     проходами шире разницы между режимами — вывода не делается, вместо него
#     пишется, почему его нет.
#
# Заявление, которое проверяется: документация Pyroscope обещает накладные расходы
# «в единицы процентов». Либо подтверждаем своими числами, либо честно говорим,
# что на этом профиле нагрузки разброс шире эффекта.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

REPORT_ONLY=0
if [ "${1:-}" = "report" ]; then
  REPORT_ONLY=1
  shift
fi

REQUESTS="${1:-20000}"
REPEATS="${2:-2}"
WARMUP=600
CONCURRENCY=8
RESULT_FILE="$STAND_DIR/results/10-profiling-overhead.txt"
# Свой каталог, не общий с bench-overhead.sh: перезапись чужих данных уже
# случалась в этом стенде и стоила повторного прогона.
TMP="$STAND_DIR/results/.profbench"

require_tools

if [ "$REPORT_ONLY" = "0" ]; then
  rm -rf "$TMP"
fi
mkdir -p "$TMP"

SAMPLER_PID=""

sample_start() {
  local out="$1"
  : > "$out"
  touch "$TMP/.sampling"
  (
    while [ -f "$TMP/.sampling" ]; do
      docker stats --no-stream --format "{{.Name}};{{.CPUPerc}};{{.MemUsage}}" \
        ops-go-frontend ops-java-backend >> "$out" 2>/dev/null || true
    done
  ) &
  SAMPLER_PID=$!
}

sample_stop() {
  rm -f "$TMP/.sampling"
  if [ -n "$SAMPLER_PID" ]; then
    wait "$SAMPLER_PID" 2>/dev/null || true
    SAMPLER_PID=""
  fi
}

summarize_stats() {
  local file="$1"
  awk -F';' '
  { name=$1; total[name]++
    if ($2 ~ /^--/ || $3 ~ /^--/) { skipped[name]++; next }
    seen[name]++
    if (seen[name] == 1) { first[name]++; next }
    cpu=$2; gsub("%","",cpu); split($3,m," / "); v=m[1]; u=v
    gsub(/[A-Za-z]+/,"",v); gsub(/[0-9.]+/,"",u)
    if (u=="GiB") v=v*1024
    cpu = cpu + 0; v = v + 0
    n[name]++; c[name]+=cpu; s[name]+=v
    if (v>mx[name]+0) mx[name]=v
    if (cpu>cmx[name]+0) cmx[name]=cpu }
  END { for (k in total) {
          if (n[k] == 0) { printf "    %-18s нет годных точек из %d\n", k, total[k]; continue }
          avg_cpu = c[k]/n[k]; avg_mem = s[k]/n[k]
          # Самопроверка замера. Среднее больше максимума физически невозможно и
          # означает ошибку разбора, а не свойство нагрузки.
          flag = ""
          if (avg_cpu > cmx[k]+0.001 || avg_mem > mx[k]+0.001) flag = "  ЗАМЕР НЕДЕЙСТВИТЕЛЕН: среднее больше максимума"
          printf "    %-18s CPU ср %6.1f%%  CPU макс %6.1f%%  ОЗУ ср %6.1f МиБ  ОЗУ макс %6.1f МиБ  (%d точек, прочерков %d)%s\n",
            k, avg_cpu, cmx[k], avg_mem, mx[k], n[k], skipped[k]+0, flag } }' "$file" | sort
}

measure() {
  local profiling="$1" label="$2" pass="$3"
  local tag="${label}-${pass}"

  echo "  подъём: телеметрия otel, профилирование $profiling (проход $pass)" >&2
  ./scripts/down.sh clean >/dev/null 2>&1
  # Телеметрия одинакова во всех трёх режимах — меняется только профилирование.
  ./scripts/up.sh all otel none "$profiling" >/dev/null 2>&1

  echo "  прогрев $WARMUP запросами" >&2
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$WARMUP" -seed 1 -concurrency "$CONCURRENCY" >/dev/null 2>&1 || true
  sleep 5

  echo "  замер $REQUESTS запросов" >&2
  sample_start "$TMP/stats-$tag.txt"

  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$REQUESTS" -seed 42 -concurrency "$CONCURRENCY" \
    -run "profbench-$tag" -json 2>/dev/null | sed -n '/^{/,$p' > "$TMP/load-$tag.json" || true

  sample_stop

  "$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
l = d['latency']
print('%s;%.1f;%.1f;%.1f;%.1f;%s;%s' % ('$tag', l['p50_ms'], l['p90_ms'], l['p99_ms'],
      l['max_ms'], d['duration'], d['mismatch']))
" < "$TMP/load-$tag.json"
}

if [ "$REPORT_ONLY" = "1" ]; then
  echo "=== Только отчёт, из сохранённых данных в results/.profbench/"
else
  echo "=== Цена профилирования: $REQUESTS запросов, $REPEATS повтора на режим"
  : > "$TMP/latency.csv"
  for pass in $(seq 1 "$REPEATS"); do
    for pair in "off:noprof" "go:goprof" "both:bothprof"; do
      prof="${pair%%:*}"
      label="${pair##*:}"
      echo "-- профилирование $prof, проход $pass"
      measure "$prof" "$label" "$pass" >> "$TMP/latency.csv"
    done
  done
fi

{
  echo "Накладные расходы непрерывного профилирования, $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "==========================================================================="
  echo "Нагрузка: $REQUESTS запросов, параллельно $CONCURRENCY, $REPEATS повтора на режим."
  echo "Во ВСЕХ режимах телеметрия OTel включена — меряется цена профилей сверх неё."
  echo
  echo "Режимы:"
  echo "  noprof    — только OTel: трейсы, метрики, логи"
  echo "  goprof    — плюс pyroscope-go 1.4.1 и метки otelpyroscope 0.6.0"
  echo "  bothprof  — плюс агент Pyroscope 2.9.0 для Java (режим ITIMER)"
  echo
  echo "Дисциплина: чистые томы перед каждым замером, прогрев $WARMUP запросами,"
  echo "пауза 5 с, ресурсы циклом docker stats --no-stream, первый снимок отброшен."
  echo "Скрипт сам проверяет признак вранья замера — среднее больше максимума."
  echo
  echo "Латентность по успешным запросам, мс"
  echo "  тег              p50     p90     p99     max     длительность  расхождение"
  if [ -f "$TMP/latency.csv" ]; then
    awk -F';' '{ printf "  %-16s %-7s %-7s %-7s %-7s %-13s %s\n", $1,$2,$3,$4,$5,$6,$7 }' "$TMP/latency.csv"
  fi
  echo
  echo "Ресурсы по режимам"
  for f in "$TMP"/stats-*.txt; do
    [ -f "$f" ] || continue
    b="$(basename "$f" .txt)"
    echo "  ${b#stats-}"
    summarize_stats "$f"
  done
  echo
  echo "Что из этого следует"
  echo "--------------------"
  if [ -f "$TMP/latency.csv" ]; then
    # PYTHONIOENCODING обязателен: без него вывод русского текста в файл на
    # Windows идёт в кодировке консоли, и отчёт оказывается нечитаемым.
    PYTHONIOENCODING=utf-8 "$PYTHON_BIN" - "$TMP/latency.csv" <<'PYEOF'
import sys, csv, statistics

rows = {}
with open(sys.argv[1], encoding="utf-8") as fh:
    for r in csv.reader(fh, delimiter=";"):
        if len(r) < 5:
            continue
        label = r[0].rsplit("-", 1)[0]
        rows.setdefault(label, []).append({"p50": float(r[1]), "p90": float(r[2]), "p99": float(r[3])})

order = ["noprof", "goprof", "bothprof"]
present = [m for m in order if m in rows]
if len(present) < 2:
    print("  Данных меньше двух режимов — сравнивать нечего.")
    raise SystemExit

# Разброс между проходами одного режима — это шум измерения. Если разница между
# режимами в него укладывается, вывода нет: так уже было в волне 1 с латентностью
# инструментирования и с памятью JVM.
for metric in ("p50", "p90", "p99"):
    print("  %s:" % metric)
    spreads = []
    for m in present:
        vals = [x[metric] for x in rows[m]]
        spread = max(vals) - min(vals) if len(vals) > 1 else 0.0
        spreads.append(spread)
        print("    %-9s проходы %s, разброс %.1f мс" %
              (m, ", ".join("%.1f" % v for v in vals), spread))
    noise = max(spreads) if spreads else 0.0
    base = statistics.median([x[metric] for x in rows[present[0]]])
    for m in present[1:]:
        cur = statistics.median([x[metric] for x in rows[m]])
        diff = cur - base
        verdict = ("ВЫВОДА НЕТ: разброс между проходами (%.1f мс) шире эффекта (%.1f мс)"
                   % (noise, abs(diff))) if abs(diff) <= noise else \
                  ("эффект %+.1f мс превышает разброс %.1f мс" % (diff, noise))
        print("    %s против %s: %+.1f мс — %s" % (m, present[0], diff, verdict))
PYEOF
  fi

  echo
  echo "Ресурсы: то же правило про разброс"
  echo "----------------------------------"
  PYTHONIOENCODING=utf-8 "$PYTHON_BIN" - "$TMP" <<'PYEOF'
import sys, os, re, statistics

# Разбор тех же файлов снимков, но с группировкой по режиму: интересует не
# отдельный проход, а устойчивость метрики между проходами.
tmp = sys.argv[1]
data = {}
for fn in sorted(os.listdir(tmp)):
    m = re.match(r'stats-(noprof|goprof|bothprof)-(\d+)\.txt$', fn)
    if not m:
        continue
    mode, _pass = m.group(1), m.group(2)
    per_container = {}
    seen = {}
    for line in open(os.path.join(tmp, fn), encoding='utf-8', errors='replace'):
        parts = line.strip().split(';')
        if len(parts) < 3 or parts[1].startswith('--') or parts[2].startswith('--'):
            continue
        name = parts[0]
        seen[name] = seen.get(name, 0) + 1
        if seen[name] == 1:      # первый снимок — до разгона
            continue
        mem = parts[2].split(' / ')[0]
        val = float(re.sub(r'[A-Za-z]+', '', mem))
        if mem.rstrip().endswith('GiB'):
            val *= 1024
        cpu = float(parts[1].replace('%', ''))
        per_container.setdefault(name, {'mem': [], 'cpu': []})
        per_container[name]['mem'].append(val)
        per_container[name]['cpu'].append(cpu)
    for name, vals in per_container.items():
        data.setdefault((mode, name), []).append(
            (statistics.mean(vals['mem']), statistics.mean(vals['cpu'])))

for container in ('ops-go-frontend', 'ops-java-backend'):
    print('  %s:' % container)
    for metric, idx, unit in (('ОЗУ', 0, 'МиБ'), ('CPU', 1, '%')):
        per_mode = {}
        for (mode, name), runs in data.items():
            if name != container:
                continue
            per_mode[mode] = [r[idx] for r in runs]
        if 'noprof' not in per_mode:
            continue
        spreads = []
        line = []
        for mode in ('noprof', 'goprof', 'bothprof'):
            if mode not in per_mode:
                continue
            vals = per_mode[mode]
            spread = max(vals) - min(vals) if len(vals) > 1 else 0.0
            spreads.append(spread)
            line.append('%s %s (разброс %.1f)' %
                        (mode, ', '.join('%.1f' % v for v in vals), spread))
        noise = max(spreads)
        print('    %s, %s: %s' % (metric, unit, '; '.join(line)))
        base = statistics.median(per_mode['noprof'])
        for mode in ('goprof', 'bothprof'):
            if mode not in per_mode:
                continue
            cur = statistics.median(per_mode[mode])
            diff = cur - base
            if abs(diff) <= noise:
                verdict = 'ВЫВОДА НЕТ: разброс между проходами (%.1f) шире эффекта (%.1f)' % (noise, abs(diff))
            else:
                verdict = 'эффект %+.1f превышает разброс %.1f' % (diff, noise)
            print('      %s против noprof: %+.1f %s — %s' % (mode, diff, unit, verdict))
PYEOF
} > "$RESULT_FILE"

cat "$RESULT_FILE"
echo
echo "Отчёт: results/$(basename "$RESULT_FILE")"
