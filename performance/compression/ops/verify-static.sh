#!/usr/bin/env bash
# Статический гейт: то, что проверяется без прогона замеров.
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0

echo "== go vet по всем модулям"
for d in dataset codecs analysis httpdemo; do
    [ -f "$d/go.mod" ] || continue
    (cd "$d" && go vet ./...) || fail=1
done

echo "== go test по модулям с тестами"
for d in dataset codecs analysis; do
    [ -f "$d/go.mod" ] || continue
    (cd "$d" && go test ./... >/dev/null) || { echo "БЛОК: тесты $d" >&2; fail=1; }
done

echo "== запрет 'latest' в compose"
if [ ! -d compose/ ]; then
    echo "БЛОК: каталог compose/ отсутствует" >&2; fail=1
else
    rc=0
    grep -rn 'image:.*:latest' compose/ || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "БЛОК: незапиненный образ" >&2; fail=1
    elif [ "$rc" -ne 1 ]; then
        echo "БЛОК: проверка 'latest' не смогла отработать (grep rc=$rc)" >&2; fail=1
    fi
fi

echo "== exec-бит на ops/*.sh"
for f in ops/*.sh; do
    [ -x "$f" ] || { echo "БЛОК: $f без exec-бита" >&2; fail=1; }
done

echo "== корпус не разошёлся со стендом inmemory"
# Копия генератора в dataset/ обязана давать те же байты, что и стенд
# inmemory. Проверка опирается на тест TestProductsJSONDeterministic в пакете
# dataset, а не на конкретное имя файла генератора — переименование или
# разнесение gen.go по нескольким файлам не должно молча выключать проверку.
EXPECTED_CORPUS_SHA="1f73c2ea439c043b8ead2dc3daee85b2ac2f1b7080587e76d81ff6830f83d65d"
got=$(cd dataset && go test -run TestProductsJSONDeterministic -v ./... 2>&1 \
      | grep -oE 'sha256\(products\.ndjson\) = [0-9a-f]+' | awk '{print $NF}') || true
if [ -z "$got" ]; then
    echo "БЛОК: контрольная сумма корпуса не получена — тест TestProductsJSONDeterministic не отработал (сломан генератор dataset?)" >&2
    fail=1
elif [ "$got" != "$EXPECTED_CORPUS_SHA" ]; then
    echo "БЛОК: контрольная сумма корпуса $got, ожидалась $EXPECTED_CORPUS_SHA" >&2
    fail=1
fi

echo "== FIXTURES не должен содержать плейсхолдеров"
if [ ! -f FIXTURES.md ]; then
    echo "БЛОК: FIXTURES.md отсутствует" >&2; fail=1
elif grep -nE 'TODO|TBD|XXX|ПОДСТАВИТЬ|<число>' FIXTURES.md; then
    echo "БЛОК: плейсхолдер в FIXTURES.md" >&2; fail=1
fi

echo "== FIXTURES обязан описывать железо: без него скорости непубликуемы"
grep -qi 'lscpu\|CPU(s)\|модель процессора\|Ryzen\|CPU:' FIXTURES.md 2>/dev/null || {
    echo "БЛОК: в FIXTURES нет раздела о железе, а скорости публикуются" >&2; fail=1; }

echo "== все сценарии представлены в FIXTURES"
for s in matrix zstd-impl dictionary columnar incompressible http-transport; do
    grep -q "$s" FIXTURES.md 2>/dev/null || { echo "БЛОК: сценарий $s не описан в FIXTURES" >&2; fail=1; }
done

echo "== evidence: файлы РЕАЛЬНО отслеживаются git"
tracked=$(git ls-files evidence/ 2>/dev/null | wc -l)
ondisk=$(ls evidence/ 2>/dev/null | wc -l)
if [ "$tracked" -eq 0 ]; then
    echo "БЛОК: evidence/ не отслеживается git — проверьте .gitignore" >&2; fail=1
elif [ "$tracked" -ne "$ondisk" ]; then
    echo "БЛОК: в evidence/ на диске $ondisk файлов, в git — $tracked" >&2; fail=1
fi

echo "== evidence: manifest не ссылается на отсутствующие файлы"
if [ ! -f evidence/README.md ]; then
    echo "БЛОК: evidence/README.md (манифест) отсутствует" >&2; fail=1
else
    grep -oE '`[a-zA-Z0-9._-]+\.(log|csv)`' evidence/README.md | tr -d '`' | sort -u | while read -r f; do
        [ -f "evidence/$f" ] || { echo "БЛОК: manifest ссылается на evidence/$f, которого нет" >&2; exit 1; }
    done || fail=1
fi

exit $fail
