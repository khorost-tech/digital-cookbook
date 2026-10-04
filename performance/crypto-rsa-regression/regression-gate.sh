#!/usr/bin/env bash
# regression-gate.sh <old.txt> <new.txt> [threshold_percent]
#
# CI-гейт: сравнивает медиану ns/op КАЖДОГО бенча в двух прогонах и РОНЯЕТ сборку
# (exit 1), если хоть один регрессировал больше порога (дефолт 50%) ИЛИ исчез из
# нового прогона. Именно такой гейт поймал бы регрессию crypto/rsa в Go 1.20:
# на 1.19.13->1.20.14 он падает ровно на шести бенчмарках: публичные verify/encrypt
# (+405…+602%), RSA-4096 sign (+61%) и RS256 round-trip (+50%). RSA-2048 sign (+34%)
# порог 50% не пробивает — порог выбирают под свой SLA.
#
# Пример:
#   ./regression-gate.sh results/go1.19.13.txt results/go1.20.14.txt  # -> FAIL (verify/encrypt)
#   ./regression-gate.sh results/go1.21.13.txt results/go1.26.4.txt   # -> OK
#
# Гейт-решение считается по медиане ns/op (устойчиво, без внешних зависимостей).
# Для ЧЕСТНОЙ A/B-статистики (доверительные интервалы, p-value) рядом печатается
# benchstat, если доступен Docker (best-effort, на решение гейта не влияет).
set -euo pipefail

OLD="${1:?usage: regression-gate.sh <old.txt> <new.txt> [threshold%]}"
NEW="${2:?usage: regression-gate.sh <old.txt> <new.txt> [threshold%]}"
THRESH="${3:-50}"

# --- benchstat (best-effort, честная статистика) ---
if command -v benchstat >/dev/null 2>&1; then
    echo "=== benchstat (${OLD} -> ${NEW}) ==="; benchstat "$OLD" "$NEW" || true; echo
elif command -v docker >/dev/null 2>&1; then
    echo "=== benchstat через Docker (best-effort) ==="
    # benchstat запинен на конкретную версию x/perf (не @latest — иначе инструмент анализа плавает).
    BENCHSTAT_VER="${BENCHSTAT_VER:-v0.0.0-20260709024250-82a0b07e230d}"
    MSYS_NO_PATHCONV=1 docker run --rm -v "$(cd "$(dirname "$OLD")" && (pwd -W 2>/dev/null || pwd)):/d" \
        -w /d golang:1.26.4 sh -c \
        "go install golang.org/x/perf/cmd/benchstat@${BENCHSTAT_VER} >/dev/null 2>&1 && benchstat $(basename "$OLD") $(basename "$NEW")" \
        2>/dev/null || echo "(benchstat недоступен — пропускаю, гейт считает по медиане ниже)"
    echo
fi

# --- гейт: медиана ns/op по каждому бенчу ---
median() { grep "$2" "$1" | awk '{print $3}' | sort -n \
    | awk '{a[NR]=$1} END{if(NR==0){print "NA"} else print (NR%2? a[(NR+1)/2] : (a[NR/2]+a[NR/2+1])/2)}'; }

benches="$(grep -oE '^Benchmark[A-Za-z0-9]+' "$OLD" | sed 's/-[0-9]*$//' | sort -u)"
fail=0
printf "%-24s %14s %14s %10s\n" "benchmark" "old ns/op" "new ns/op" "delta%"
printf -- "------------------------------------------------------------------------\n"
for bn in $benches; do
    o="$(median "$OLD" "$bn")"; n="$(median "$NEW" "$bn")"
    [ "$o" = "NA" ] && continue   # бенча не было в базе — не наша забота
    if [ "$n" = "NA" ]; then       # был в базе, ИСЧЕЗ в новом прогоне -> регресс
        printf "%-24s %14s %14s %10s%s\n" "$bn" "$o" "ОТСУТСТВУЕТ" "-" "  <-- ИСЧЕЗ"; fail=1; continue
    fi
    delta="$(awk -v o="$o" -v n="$n" 'BEGIN{printf "%.1f", (n-o)/o*100}')"
    over="$(awk -v d="$delta" -v t="$THRESH" 'BEGIN{print (d>t)?1:0}')"
    mark=""; [ "$over" = "1" ] && { mark="  <-- РЕГРЕССИЯ"; fail=1; }
    printf "%-24s %14s %14s %9s%%%s\n" "$bn" "$o" "$n" "$delta" "$mark"
done
echo
if [ "$fail" -ne 0 ]; then
    echo "GATE: FAIL — регрессия > ${THRESH}% (в CI это уронило бы сборку)"; exit 1
fi
echo "GATE: OK — регрессий > ${THRESH}% нет"
