#!/usr/bin/env bash
# matrix.sh — снимает бенчи crypto-операций на РАЗНЫХ версиях Go через Docker.
#
# Методика против шума (Docker-на-хосте, троттлинг):
#  - ПОЛНЫЕ патч-теги образов (короткие golang:1.X изменяемы);
#  - ЧЕРЕДОВАНИЕ версий по раундам (round-robin), а не «все повторы одной версии
#    подряд» + РОТАЦИЯ порядка между раундами — так тепловой дрейф и фон
#    распределяются между версиями, а не оседают на одной;
#  - несколько независимых раундов (ROUNDS), benchstat агрегирует выборку;
#  - отдельный прогревочный проход перед замером (WARMUP=1, в результат не пишется).
# Гоняйте на НЕнагруженной машине. Все различия читаются через benchstat с
# оговоркой «одна машина»; кратный спайк публичных операций надёжен и без него.
#
# ENV: VERSIONS, ROUNDS (дефолт 12 — кратно числу версий), BENCHTIME (дефолт 1s),
#      WARMUP (0/1), GOMODCACHE_HOST (опц.).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT_DIR_DOCKER="$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd))"
export MSYS_NO_PATHCONV=1

# Полные патч-теги — воспроизводимо (короткие теги мутабельны).
VERSIONS="${VERSIONS:-1.19.13 1.20.14 1.21.13 1.23.12 1.25.12 1.26.4}"
# ROUNDS по умолчанию кратно числу версий (6) — тогда каждая версия занимает каждую
# позицию в раунде РОВНО одинаковое число раз (полностью сбалансированный план).
ROUNDS="${ROUNDS:-12}"
BENCHTIME="${BENCHTIME:-1s}"
WARMUP="${WARMUP:-1}"
GOMODCACHE_HOST="${GOMODCACHE_HOST:-}"

EXPECTED="BenchmarkRSA2048Sign BenchmarkRSA4096Sign BenchmarkRSA2048Verify \
BenchmarkRSA4096Verify BenchmarkRSA2048Encrypt BenchmarkRSA4096Encrypt \
BenchmarkECDSAP256Sign BenchmarkECDSAP256Verify BenchmarkHMACSHA256 BenchmarkRS256Roundtrip"

mkdir -p "$SCRIPT_DIR/results"

mount_cache=""
[ -n "$GOMODCACHE_HOST" ] && mount_cache="-v ${GOMODCACHE_HOST}:/go/pkg/mod"

run_ver() { # <tag> <bench-args...> -> печатает вывод go test
    # shellcheck disable=SC2086
    docker run --rm $mount_cache -v "${SCRIPT_DIR_DOCKER}:/src" -w /src \
        -e GOFLAGS=-mod=mod "golang:$1" \
        sh -c "go test -bench=. -benchmem -run='^\$' $2 ./..."
}

# Инициализируем файлы: строка go version один раз.
for v in $VERSIONS; do
    out="$SCRIPT_DIR/results/go${v}.txt"
    # shellcheck disable=SC2086
    docker run --rm $mount_cache "golang:$v" go version > "$out"
done

# Прогрев (не пишем в результат) — стабилизирует частоты/кэш.
if [ "$WARMUP" = "1" ]; then
    echo "=== прогрев (WARMUP) ==="
    for v in $VERSIONS; do run_ver "$v" "-count=1 -benchtime=200x" >/dev/null 2>&1 || true; done
fi

# Раунды с ЧЕРЕДОВАНИЕМ + РОТАЦИЕЙ порядка версий: каждый раунд начинается со
# следующей версии (циклический сдвиг), чтобы ни одна версия не занимала всегда
# один и тот же тепловой слот — дрейф распределяется между версиями (поровну, если
# ROUNDS кратно числу версий; иначе часть позиций повторяется чаще).
vers=($VERSIONS)
nver=${#vers[@]}
for r in $(seq 1 "$ROUNDS"); do
    # Порядок раунда r: сдвиг на (r-1) позиций.
    order=""
    for i in $(seq 0 $((nver - 1))); do
        order="$order ${vers[$(( (i + r - 1) % nver ))]}"
    done
    echo "=== раунд ${r}/${ROUNDS} (порядок:${order} ) ==="
    for v in $order; do
        out="$SCRIPT_DIR/results/go${v}.txt"
        run_ver "$v" "-count=1 -benchtime=${BENCHTIME}" 2>>"$out" \
            | grep '^Benchmark' >> "$out" \
            || { echo "!!! golang:$v раунд $r упал" >&2; exit 1; }
    done
done

# Валидация: у КАЖДОЙ версии все ожидаемые бенчи должны встречаться ровно ROUNDS раз.
echo "=== валидация полноты выборки ==="
fail=0
for v in $VERSIONS; do
    out="$SCRIPT_DIR/results/go${v}.txt"
    grep -q "^go version go${v} " "$out" || grep -q "^go version go${v}$" "$out" \
        || { echo "  FAIL go$v: нет строки go version go${v}" >&2; fail=1; }
    for bn in $EXPECTED; do
        n=$(grep -cE "^${bn}-" "$out")
        [ "$n" -eq "$ROUNDS" ] || { echo "  FAIL go$v: ${bn} = ${n} сэмплов, ожидалось ${ROUNDS}" >&2; fail=1; }
    done
done
[ "$fail" -eq 0 ] || { echo "!!! неполная выборка — замер недействителен" >&2; exit 1; }
echo "  ok: все версии × $(echo $EXPECTED | wc -w) бенчей × ${ROUNDS} сэмплов"

echo
echo "=== сводка: медиана ns/op (RSA2048Verify — СЮДА бьёт регрессия 1.20) ==="
median() { grep "$2" "$1" | awk '{print $3}' | sort -n \
    | awk '{a[NR]=$1} END{if(NR)print (NR%2? a[(NR+1)/2] : int((a[NR/2]+a[NR/2+1])/2))}'; }
for v in $VERSIONS; do
    f="$SCRIPT_DIR/results/go${v}.txt"
    printf "  go %-8s Vfy2048 %10s  Enc2048 %10s  Sign2048 %10s  ECDSAsign %8s  HMAC %7s\n" \
        "$v" \
        "$(median "$f" BenchmarkRSA2048Verify)" "$(median "$f" BenchmarkRSA2048Encrypt)" \
        "$(median "$f" BenchmarkRSA2048Sign)" "$(median "$f" BenchmarkECDSAP256Sign)" \
        "$(median "$f" BenchmarkHMACSHA256)"
done
echo "(ns/op; одна машина — важны соотношения и порядок, не абсолют)"
echo "патч-версии: $VERSIONS"
