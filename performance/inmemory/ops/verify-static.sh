#!/usr/bin/env bash
# Статический гейт: то, что можно проверить без поднятия систем.
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0

echo "== go vet по всем модулям"
for d in dataset redis-cache tarantool picodata aerospike etcd-coord benchmark; do
    [ -f "$d/go.mod" ] || continue
    (cd "$d" && go vet ./...) || fail=1
done

echo "== запрет 'latest' в compose"
if grep -rn 'image:.*:latest' compose/; then
    echo "БЛОК: незапиненный образ" >&2; fail=1
fi

echo "== exec-бит на ops/*.sh"
for f in ops/*.sh; do
    [ -x "$f" ] || { echo "БЛОК: $f без exec-бита" >&2; fail=1; }
done

echo "== FIXTURES не должен содержать плейсхолдеров"
if grep -nE 'TODO|TBD|XXX|<число>|\.\.\.ms' FIXTURES.md; then
    echo "БЛОК: плейсхолдер в FIXTURES.md" >&2; fail=1
fi

echo "== в FIXTURES не должно быть абсолютной latency как материала статьи"
# Ловим шаблоны вида "p99 = 1.2ms" / "median 340µs" в разделах статьи.
if grep -nE '^\s*(p50|p95|p99|median|среднее)\s*[:=]' FIXTURES.md; then
    echo "БЛОК: абсолютная latency в FIXTURES — запрещена Global Constraints" >&2; fail=1
fi

echo "== все стенды представлены в FIXTURES"
for s in redis tarantool picodata aerospike ignite etcd working-set compute-locality etcd-misuse; do
    grep -q "$s" FIXTURES.md || { echo "БЛОК: стенд $s не описан в FIXTURES" >&2; fail=1; }
done

# ── evidence-контракт ─────────────────────────────────────────────────────
# Появился после реального инцидента: 20 логов лежали на диске, но в git не
# попали (глобальное правило *.log), а manifest и FIXTURES уже утверждали,
# что они отслеживаются. Проверяли `ls`, а не `git ls-files`. Здесь —
# машинная защита от повторения.

echo "== evidence: файлы РЕАЛЬНО отслеживаются git (не просто лежат на диске)"
tracked=$(git ls-files evidence/ 2>/dev/null | wc -l)
ondisk=$(ls evidence/ 2>/dev/null | wc -l)
if [ "$tracked" -eq 0 ]; then
    echo "БЛОК: evidence/ не отслеживается git — проверьте .gitignore" >&2; fail=1
elif [ "$tracked" -ne "$ondisk" ]; then
    echo "БЛОК: в evidence/ на диске $ondisk файлов, в git — $tracked" >&2
    git status --porcelain --ignored evidence/ 2>/dev/null | grep '^!!' >&2
    fail=1
fi

echo "== evidence: manifest не ссылается на отсутствующие файлы"
grep -oE '`[a-z0-9._-]+\.(log|csv)`' evidence/README.md 2>/dev/null | tr -d '`' | sort -u | while read f; do
    [ -f "evidence/$f" ] || { echo "БЛОК: manifest ссылается на evidence/$f, которого нет" >&2; exit 1; }
done || fail=1

echo "== evidence: CSV содержат заявленное число строк"
check_csv() {  # файл, ожидаемое число строк данных (без заголовка)
    [ -f "evidence/$1" ] || { echo "БЛОК: нет evidence/$1" >&2; return 1; }
    n=$(( $(wc -l < "evidence/$1") - 1 ))
    [ "$n" -eq "$2" ] || { echo "БЛОК: evidence/$1 — $n строк данных, ожидалось $2" >&2; return 1; }
}
check_csv aerospike-nsup-period-25runs.csv 25 || fail=1
check_csv aerospike-nsup-period-summary.csv 5 || fail=1
check_csv compute-locality-ratios.csv 24 || fail=1

exit $fail
