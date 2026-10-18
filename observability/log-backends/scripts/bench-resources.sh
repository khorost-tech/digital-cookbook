#!/usr/bin/env bash
# Сколько ресурсов забирают три бэкенда на одном и том же потоке.
#
#   ./scripts/bench-resources.sh [записей] [seed]
#
# Требует чистых томов: ./scripts/down.sh clean && ./scripts/up.sh
#
# СНИМАЕТСЯ СЕРИЯ, А НЕ СНИМОК. Одиночный `docker stats` даёт артефакт момента:
# в соседнем стенде этой серии он показал память Prometheus МЕНЬШЕ при взрыве
# кардинальности, чем без него. Здесь берутся медиана и пик по серии, и отдельно
# приводится покой — до начала потока.
#
# ЧТО ОЖИДАЕТСЯ НЕДОКАЗАТЕЛЬНЫМ. На стендовом объёме разница по CPU почти
# наверняка утонет в шуме: сто тысяч записей три бэкенда переваривают за
# секунды. Память — другое дело: разрыв между JVM и Go-бинарником виден уже в
# покое (1745 МиБ против 11 МиБ, results/00-feasibility.txt), и он не про
# нагрузку, а про устройство.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-100000}"
SEED="${2:-42}"
RUN="res-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/03-resources.txt"

SAMPLES="${SAMPLES:-20}"
SAMPLE_INTERVAL="${SAMPLE_INTERVAL:-3}"

# --- снятие серии ------------------------------------------------------------

# Один снимок по всем трём контейнерам сразу: так значения относятся к одному
# моменту, и сравнение между бэкендами корректно.
snapshot() {
  docker stats --no-stream --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' lb-loki lb-os lb-vlogs 2>/dev/null
}

# Медиана и пик по серии для одного контейнера.
# Единицы приводим сами: docker stats печатает то МиБ, то ГиБ.
series_stats() {
  local name="$1"
  "$PYTHON_BIN" -c "
import sys, statistics

mem, cpu = [], []
for line in sys.stdin:
    parts = line.strip().split('|')
    if len(parts) != 3 or parts[0] != '$name':
        continue
    raw = parts[1].split('/')[0].strip()
    val = float(''.join(c for c in raw if c.isdigit() or c == '.') or 0)
    if 'GiB' in raw:
        val *= 1024
    elif 'KiB' in raw:
        val /= 1024
    mem.append(val)
    c = parts[2].strip().rstrip('%')
    try:
        cpu.append(float(c))
    except ValueError:
        pass

if not mem:
    print('0 0 0 0'); raise SystemExit
print('%.1f %.1f %.1f %.1f' % (
    statistics.median(mem), max(mem),
    statistics.median(cpu) if cpu else 0, max(cpu) if cpu else 0))
"
}

# --- покой -------------------------------------------------------------------
#
# Замер до нагрузки: он отвечает на вопрос «сколько стоит просто держать это
# запущенным», и для выбора бэкенда он не менее важен, чем поведение под
# нагрузкой.

log "покой: серия из 5 снимков до начала потока"
IDLE_SERIES=""
for i in $(seq 1 5); do
  IDLE_SERIES="$IDLE_SERIES$(snapshot)
"
  [ "$i" -lt 5 ] && sleep 3
done

read -r IDLE_MEM_LOKI IDLE_PEAK_LOKI _ _ <<<"$(printf '%s' "$IDLE_SERIES" | series_stats lb-loki)"
read -r IDLE_MEM_OS   IDLE_PEAK_OS   _ _ <<<"$(printf '%s' "$IDLE_SERIES" | series_stats lb-os)"
read -r IDLE_MEM_VL   IDLE_PEAK_VL   _ _ <<<"$(printf '%s' "$IDLE_SERIES" | series_stats lb-vlogs)"
echo "  память в покое: Loki ${IDLE_MEM_LOKI} МиБ, OpenSearch ${IDLE_MEM_OS} МиБ, VictoriaLogs ${IDLE_MEM_VL} МиБ"

# --- нагрузка и серия во время неё -------------------------------------------

log "прогон: $COUNT записей, seed $SEED, метка $RUN"
docker compose --profile tools run --rm -T loadgen \
  -count "$COUNT" -seed "$SEED" -run "$RUN" >/dev/null 2>&1

log "серия из $SAMPLES снимков с интервалом ${SAMPLE_INTERVAL}с"
SERIES=""
for i in $(seq 1 "$SAMPLES"); do
  SERIES="$SERIES$(snapshot)
"
  [ "$i" -lt "$SAMPLES" ] && sleep "$SAMPLE_INTERVAL"
done

read -r MEM_LOKI PEAK_LOKI CPU_LOKI CPUPEAK_LOKI <<<"$(printf '%s' "$SERIES" | series_stats lb-loki)"
read -r MEM_OS   PEAK_OS   CPU_OS   CPUPEAK_OS   <<<"$(printf '%s' "$SERIES" | series_stats lb-os)"
read -r MEM_VL   PEAK_VL   CPU_VL   CPUPEAK_VL   <<<"$(printf '%s' "$SERIES" | series_stats lb-vlogs)"

echo "  под нагрузкой (медиана / пик):"
echo "    Loki:         ${MEM_LOKI} / ${PEAK_LOKI} МиБ, CPU ${CPU_LOKI} / ${CPUPEAK_LOKI} %"
echo "    OpenSearch:   ${MEM_OS} / ${PEAK_OS} МиБ, CPU ${CPU_OS} / ${CPUPEAK_OS} %"
echo "    VictoriaLogs: ${MEM_VL} / ${PEAK_VL} МиБ, CPU ${CPU_VL} / ${CPUPEAK_VL} %"

# --- отчёт -------------------------------------------------------------------

# Два разных отношения, и их нельзя путать: в покое разрыв кратно больше, чем
# под нагрузкой, потому что JVM забирает heap сразу, а Go-бинарник растёт по
# мере работы.
RATIO_IDLE="$("$PYTHON_BIN" -c "print('%.0f' % ($IDLE_MEM_OS / max($IDLE_MEM_VL, 0.1)))")"
RATIO_LOAD="$("$PYTHON_BIN" -c "print('%.0f' % ($MEM_OS / max($MEM_VL, 0.1)))")"

mkdir -p "$STAND_DIR/results"
{
  echo "Ресурсы трёх бэкендов на одном потоке"
  echo "======================================"
  echo "Прогон: $COUNT записей, seed $SEED, метка $RUN."
  echo "Серия: $SAMPLES снимков с интервалом ${SAMPLE_INTERVAL}с, плюс 5 снимков в покое."
  echo
  echo "ПАМЯТЬ, МиБ"
  printf "%-14s %10s %14s %10s\n" "БЭКЕНД" "ПОКОЙ" "ПОД НАГРУЗКОЙ" "ПИК"
  printf "%-14s %10s %14s %10s\n" "Loki"         "$IDLE_MEM_LOKI" "$MEM_LOKI" "$PEAK_LOKI"
  printf "%-14s %10s %14s %10s\n" "OpenSearch"   "$IDLE_MEM_OS"   "$MEM_OS"   "$PEAK_OS"
  printf "%-14s %10s %14s %10s\n" "VictoriaLogs" "$IDLE_MEM_VL"   "$MEM_VL"   "$PEAK_VL"
  echo
  echo "CPU, % (медиана / пик за серию)"
  printf "%-14s %10s %10s\n" "Loki"         "$CPU_LOKI" "$CPUPEAK_LOKI"
  printf "%-14s %10s %10s\n" "OpenSearch"   "$CPU_OS"   "$CPUPEAK_OS"
  printf "%-14s %10s %10s\n" "VictoriaLogs" "$CPU_VL"   "$CPUPEAK_VL"
  echo
  echo "ЧТО ЗДЕСЬ ДОКАЗАТЕЛЬНО"
  echo
  echo "1. ПАМЯТЬ В ПОКОЕ. OpenSearch против VictoriaLogs — ${RATIO_IDLE}x,"
  echo "   и это НЕ про нагрузку: столько нужно, чтобы просто держать сервис"
  echo "   запущенным. JVM забирает заданный -Xms сразу и не отдаёт; одиночный"
  echo "   Go-бинарник начинает с десятка мегабайт. Для выбора бэкенда это"
  echo "   существеннее показателей под нагрузкой, особенно если экземпляров"
  echo "   много."
  echo
  echo "2. ПАМЯТЬ ПОД НАГРУЗКОЙ. Разрыв сокращается до ${RATIO_LOAD}x: Loki и"
  echo "   VictoriaLogs растут в разы (с десятков до сотни с лишним мегабайт), а"
  echo "   OpenSearch прибавляет немного к уже занятому heap. В ЭТОЙ конфигурации"
  echo "   (Xms = Xmx = 1g) основную часть памяти OpenSearch задаёт heap, поэтому"
  echo "   она почти не зависит от нагрузки. Это наблюдение о стенде, а не"
  echo "   правило: вне heap у JVM и Lucene своя память, и она растёт с индексом."
  echo "   Путать эти два отношения нельзя: ${RATIO_IDLE}x и ${RATIO_LOAD}x"
  echo "   отвечают на разные вопросы."
  echo
  echo "ЧТО ЗДЕСЬ НЕ ДОКАЗАТЕЛЬНО — НИ МЕДИАНА CPU, НИ ПИК"
  echo
  echo "Медиана CPU: ${CPU_LOKI}% / ${CPU_OS}% / ${CPU_VL}%. Сто тысяч записей"
  echo "три бэкенда переваривают за секунды, а серия снимается ${SAMPLES} раз с"
  echo "интервалом ${SAMPLE_INTERVAL}с — большая её часть приходится на время"
  echo "ПОСЛЕ окончания приёма. Медиана в такой постановке описывает покой."
  echo
  echo "Пик CPU: ${CPUPEAK_LOKI}% / ${CPUPEAK_OS}% / ${CPUPEAK_VL}%. Сначала я"
  echo "счёл его доказательным — разрыв выглядел кратным, — и ошибся. Три прогона"
  echo "ОДНОЙ конфигурации дали у OpenSearch пик 662.6%, 70.5% и ${CPUPEAK_OS}%."
  echo "Между первым и вторым — разница на порядок. Причина в способе снятия:"
  echo "docker stats даёт мгновенное значение раз в ${SAMPLE_INTERVAL}с, и"
  echo "попадёт ли замер на всплеск индексации — вопрос везения, а не свойства"
  echo "бэкенда. Тот же прогон, та же нагрузка, разные числа."
  echo
  echo "Единственное, что можно сказать про CPU по этим данным: у OpenSearch"
  echo "всплески бывают выше 100%, то есть индексация распараллеливается на"
  echo "несколько ядер. Величину всплеска эти замеры не определяют."
  echo
  echo "Для честного замера CPU нужен длительный устойчивый поток (минуты, а не"
  echo "секунды) и контроль, что все три успевают его принимать без отставания."
  echo "Это отдельная работа, и в этом стенде она не делалась."
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"
