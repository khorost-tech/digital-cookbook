#!/usr/bin/env bash
#
# verify-static.sh — статический гейт стенда. Кластер не поднимает,
# ничего не измеряет: проверяет только то, что можно проверить без
# запуска, но что ломает стенд у читателя. По образцу
# databases/cockroachdb/scripts/verify-static.sh.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/verify-static.sh

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
# а не по правам в рабочей копии: на drvfs права показываются неверно,
# а git add на Windows-стороне ранее уже ронял бит (см. README, «Ловушки»).
for f in scripts/*.sh; do
    mode="$(git ls-files -s "$f" 2>/dev/null | awk '{print $1}')"
    case "$mode" in
        100755) ok "$f (100755)" ;;
        "")     bad "$f не в индексе git" ;;
        *)      bad "$f имеет режим $mode вместо 100755" ;;
    esac
done

echo
echo "=== Go: сборка, vet, тесты ==="
if ( cd go && go vet ./... >/dev/null 2>&1 ); then ok "go vet ./..."; else bad "go vet ./..."; fi
if ( cd go && go build -o /dev/null ./... >/dev/null 2>&1 ); then ok "go build ./..."; else bad "go build ./..."; fi
if ( cd go && go test ./... >/dev/null 2>&1 ); then ok "go test ./..."; else bad "go test ./..."; fi
if [ -f go/go.sum ]; then ok "go/go.sum на месте"; else bad "go/go.sum отсутствует"; fi

echo
echo "=== Compose-файл разбирается ==="
if docker compose -f compose/compose.yml config -q >/dev/null 2>&1; then
    ok "compose/compose.yml"
else
    bad "compose/compose.yml — compose не разобрал"
fi

echo
echo "=== Пины версий образов ==="
# Плавающий тег (:latest) незакреплён по определению. Дальше у каждого
# образа своя схема версионирования, поэтому "полный пин" — разное число
# сегментов у тега, не одно общее правило:
#   - postgres версионируется major.point (18.4) — двух сегментов
#     достаточно, третьего у современных образов Postgres и не бывает;
#   - kurrentplatform/kurrentdb и apache/kafka версионируются
#     major.minor.patch — им нужны РОВНО три сегмента. Тег вида :26.1 или
#     :4.3 всё ещё плавает внутри патч-версии: дрейф там меняет поведение
#     молча, а зафиксированные в FIXTURES.md числа продолжат ссылаться на
#     старый патч. Ревью поймало это на 26.1.2 -> 26.1: с одним общим
#     регэкспом (":(latest|[0-9]+)$") такой тег проходил как "закреплён".
#
# check_image_pin <название> <префикс образа> <ровно|минимум> <число сегментов>
check_image_pin() {
    local label="$1" prefix="$2" rule="$3" want="$4"
    local tag segments
    tag="$(grep -rhoE "${prefix}:[^[:space:]\"]+" compose/*.yml 2>/dev/null | sort -u | sed "s#^${prefix}:##")"
    if [ -z "$tag" ]; then
        bad "$label — образ $prefix не найден в compose/"
        return
    fi
    if [ "$tag" = "latest" ]; then
        bad "$label — $prefix:latest (плавающий тег)"
        return
    fi
    segments="$(awk -F. '{print NF}' <<<"$tag")"
    case "$rule" in
        ровно)
            if [ "$segments" -eq "$want" ]; then
                ok "$label — $prefix:$tag ($segments сегм., требуется ровно $want)"
            else
                bad "$label — $prefix:$tag недопинован ($segments сегм. вместо ровно $want — дрейф внутри незакреплённого сегмента меняет поведение молча)"
            fi
            ;;
        минимум)
            if [ "$segments" -ge "$want" ]; then
                ok "$label — $prefix:$tag ($segments сегм., требуется минимум $want)"
            else
                bad "$label — $prefix:$tag недопинован ($segments сегм. вместо минимум $want)"
            fi
            ;;
    esac
}

check_image_pin "Postgres"  "postgres"                    "минимум" 2
check_image_pin "KurrentDB" "kurrentplatform/kurrentdb"    "ровно"   3
check_image_pin "Kafka"     "apache/kafka"                 "ровно"   3

echo
echo "=== Каталог стендов ==="
CAT="../../catalog.json"
if [ -f "$CAT" ]; then
    if python3 -m json.tool "$CAT" >/dev/null 2>&1; then ok "catalog.json — валидный JSON"; else bad "catalog.json не разбирается"; fi
    if grep -q '"architecture/event-sourcing-deep"' "$CAT"; then
        ok "запись architecture/event-sourcing-deep есть"
    else
        bad "записи architecture/event-sourcing-deep НЕТ — промоутер молча пропустит стенд"
    fi
else
    bad "catalog.json не найден по пути $CAT"
fi

echo
echo "=== FIXTURES.md ==="
if [ -f FIXTURES.md ]; then
    ok "FIXTURES.md на месте"
    if grep -nE '(^|[^`[:alnum:]])(TODO|TBD|XXX)([^[:alnum:]]|$)|<!--' FIXTURES.md; then
        bad "в FIXTURES.md остались заглушки (см. строки выше)"
    else
        ok "заглушек нет"
    fi
    # Приблизительность ловим ТОЛЬКО там, где за словом сразу идёт число:
    # «~300», «около 300», «примерно 300», «порядка 300». Голое «порядка»
    # без числа рядом — это «порядок событий», «инверсия порядка» и т.п.,
    # буквальный поиск подстроки заворачивал бы честный текст (см. брифы
    # задачи 9 и предыдущий инцидент с этим самым гейтом в этом стенде).
    if grep -nEi '(~|около|примерно|порядка)[[:space:]]*[0-9]' FIXTURES.md; then
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
