#!/usr/bin/env bash
#
# verify-static.sh — статический гейт стенда. Кластер не поднимает,
# ничего не измеряет: проверяет только то, что можно проверить без
# запуска, но что ломает стенд у читателя.
#
# Запуск (из databases/cockroachdb/):  bash scripts/verify-static.sh

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

RC=0
ok()   { printf '  OK    %s\n' "$1"; }
bad()  { printf '  СБОЙ  %s\n' "$1"; RC=1; }

echo "=== Синтаксис shell-скриптов ==="
for f in scripts/*.sh; do
    if bash -n "$f" 2>/dev/null; then ok "$f"; else bad "$f — синтаксическая ошибка"; fi
done

echo
echo "=== Флаг исполнения на скриптах ==="
# .sh без exec-бита не запустится у читателя. Проверяем по индексу git,
# а не по правам в рабочей копии: на drvfs права показываются неверно.
for f in scripts/*.sh; do
    mode="$(git ls-files -s "$f" 2>/dev/null | awk '{print $1}')"
    case "$mode" in
        100755) ok "$f (100755)" ;;
        "")     bad "$f не в индексе git" ;;
        *)      bad "$f имеет режим $mode вместо 100755" ;;
    esac
done

echo
echo "=== Go: сборка и vet ==="
if ( cd loadgen && go vet ./... >/dev/null 2>&1 ); then ok "go vet loadgen"; else bad "go vet loadgen"; fi
if ( cd loadgen && go build -o /dev/null . >/dev/null 2>&1 ); then ok "go build loadgen"; else bad "go build loadgen"; fi
if [ -f loadgen/go.sum ]; then ok "loadgen/go.sum на месте"; else bad "loadgen/go.sum отсутствует"; fi

echo
echo "=== Compose-файлы разбираются ==="
# time.yml — ОВЕРЛЕЙ: в нём только command, без image, и сам по себе он
# невалиден по определению. Проверять его в одиночку означало бы получать
# ложный сбой, поэтому оверлеи проверяются в связке с базой.
for f in compose/*.yml; do
    case "$f" in
        compose/compose.yml)
            if docker compose -f "$f" config -q >/dev/null 2>&1; then ok "$f"; else bad "$f — compose не разобрал"; fi
            ;;
        compose/time.yml)
            if docker compose -f compose/compose.yml -f "$f" config -q >/dev/null 2>&1; then
                ok "$f (оверлей поверх compose.yml)"
            else
                bad "$f — не разбирается в связке с compose.yml"
            fi
            ;;
        *)
            if docker compose -f "$f" config -q >/dev/null 2>&1; then ok "$f"; else bad "$f — compose не разобрал"; fi
            ;;
    esac
done

echo
echo "=== Пины версий образов ==="
# Плавающий тег означает, что через месяц стенд соберётся на другой
# версии, а числа в статье останутся от старой.
if grep -rqE 'cockroachdb/cockroach:(latest|v?[0-9]+\.[0-9]+)$' compose/ 2>/dev/null; then
    bad "в compose есть неполный или плавающий тег образа"
else
    ok "теги образов закреплены до патча"
fi
for img in $(grep -rhoE 'cockroachdb/cockroach:v[0-9.]+' compose/ | sort -u); do
    printf '        %s\n' "$img"
done

echo
echo "=== Каталог стендов ==="
CAT="../../catalog.json"
if [ -f "$CAT" ]; then
    if python3 -m json.tool "$CAT" >/dev/null 2>&1; then ok "catalog.json — валидный JSON"; else bad "catalog.json не разбирается"; fi
    if grep -q '"databases/cockroachdb"' "$CAT"; then
        ok "запись databases/cockroachdb есть"
    else
        bad "записи databases/cockroachdb НЕТ — промоутер молча пропустит стенд"
    fi
else
    bad "catalog.json не найден по пути $CAT"
fi

echo
echo "=== FIXTURES.md ==="
if [ -f FIXTURES.md ]; then
    ok "FIXTURES.md на месте"
    if grep -nEi '\b(TODO|TBD|XXX|уточнить при написании)\b' FIXTURES.md; then
        bad "в FIXTURES.md остались заглушки (см. строки выше)"
    else
        ok "заглушек нет"
    fi
    # «Примерно 300 мс» в источнике чисел — это не число, а пересказ.
    if grep -nEi '(примерно|около|порядка|~)[[:space:]]*[0-9]' FIXTURES.md; then
        bad "в FIXTURES.md есть приблизительные числа (см. строки выше)"
    else
        ok "приблизительных чисел нет"
    fi
else
    bad "FIXTURES.md отсутствует — источника чисел для статей нет"
fi

echo
if [ "$RC" -eq 0 ]; then
    echo "ГЕЙТ ПРОЙДЕН"
else
    echo "ГЕЙТ НЕ ПРОЙДЕН"
fi
exit "$RC"
