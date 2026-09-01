#!/usr/bin/env bash
# Три колонки: v1-old (encoding/json, прежний движок), v1-on-v2 (encoding/json,
# умолчание Go 1.27) и v2 API (encoding/json/v2 напрямую, либо n/a под откатом).
#
# Первая версия этого скрипта сравнивала только v1-old и v1-on-v2 и падала с
# rc=1, когда они совпадали, — как будто GOEXPERIMENT не применился. Но
# GOEXPERIMENT применялся исправно: encoding/json действительно не меняет
# терпимость, работая поверх v2. Строгость v2 не пропала — она живёт в другом
# API (encoding/json/v2), а не в движке под encoding/json. Отсюда ДВЕ РАЗНЫЕ
# проверки, а не одна:
#
#   1. v1-old и v1-on-v2 совпадают -> encoding/json не изменил поведение,
#      совместимость сохранена. Это ОЖИДАЕМЫЙ исход, не отказ.
#   2. v2 API строже: хотя бы один случай, который encoding/json принимает,
#      encoding/json/v2 (в сборке v1-on-v2, где пакет v2 доступен) отвергает.
#      Если это не так — либо список случаев не задевает реальную строгость
#      v2, либо собрали не то. Это ДОЛЖНО падать с rc=1: без строгого случая
#      замер не состоялся.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
mkdir -p results

OLD="$(mktemp)"; NEW="$(mktemp)"
trap 'rm -f "$OLD" "$NEW"' EXIT

GOTOOLCHAIN=go1.27.0 GOEXPERIMENT=nojsonv2 go run ./cmd/strictness-demo > "$OLD"
GOTOOLCHAIN=go1.27.0                       go run ./cmd/strictness-demo > "$NEW"

# Контроль применения режима: без него любой из выводов ниже неотличим от
# «флаг не применился». Проверяем buildmode.Raw(), вшитый в заголовок каждой
# выдачи.
if ! grep -q 'GOEXPERIMENT="nojsonv2"' "$OLD"; then
  echo "ОШИБКА: v1-old не подтвердил GOEXPERIMENT=nojsonv2 — сравнивать нечего." >&2
  exit 1
fi
if grep -q 'GOEXPERIMENT="nojsonv2"' "$NEW"; then
  echo "ОШИБКА: v1-on-v2 тоже видит GOEXPERIMENT=nojsonv2 — режимы не различаются на входе." >&2
  exit 1
fi

# Порядок случаев в обоих файлах одинаков (один и тот же срез cases в main.go),
# поэтому колонки можно сопоставлять построчно без поиска по имени.
OLD_ROWS="$(grep -v '^#' "$OLD")"
NEW_ROWS="$(grep -v '^#' "$NEW")"

# Проверка 1: совместимость encoding/json (колонка 2) между режимами.
COMPAT_DIFF="$(diff <(echo "$OLD_ROWS" | awk '{print $1, $2}') \
                     <(echo "$NEW_ROWS" | awk '{print $1, $2}') || true)"
COMPAT_VERDICT="совпадают (совместимость сохранена)"
[ -n "$COMPAT_DIFF" ] && COMPAT_VERDICT="различаются (совместимость НЕ сохранена)"

# Проверка 2: v2 API (колонка 3 в NEW) строже encoding/json (колонка 2 в NEW)
# хотя бы в одном случае.
STRICTER_ROWS="$(paste -d' ' <(echo "$NEW_ROWS" | awk '{print $1}') \
                             <(echo "$NEW_ROWS" | awk '{print $2}') \
                             <(echo "$NEW_ROWS" | awk '{print $3}') \
                 | awk '$2=="accepted" && $3=="rejected" {printf "%-24s encoding/json=%-10s v2=%s\n", $1, $2, $3}')"
V2_STRICTER=0
[ -n "$STRICTER_ROWS" ] && V2_STRICTER=1

{
  echo "Строгость: v1-old / v1-on-v2 / v2 API на одном наборе случаев"
  echo "================================================================"
  echo
  echo "--- v1-old (GOEXPERIMENT=nojsonv2): <случай> <encoding/json> <v2 API> ---"
  cat "$OLD"
  echo
  echo "--- v1-on-v2 (умолчание Go 1.27): <случай> <encoding/json> <v2 API> ---"
  cat "$NEW"
  echo
  echo "--- совместимость: encoding/json, v1-old против v1-on-v2 ---"
  if [ -z "$COMPAT_DIFF" ]; then
    echo "(нет различий: $COMPAT_VERDICT)"
  else
    echo "$COMPAT_DIFF"
  fi
  echo
  echo "--- строгость: v1-on-v2 (encoding/json) против v2 API в той же сборке ---"
  if [ -n "$STRICTER_ROWS" ]; then
    echo "$STRICTER_ROWS"
  else
    echo "(нет случаев, где v2 API отверг бы то, что принял encoding/json)"
  fi
  echo
  echo "--- вердикт ---"
  echo "Совместимость encoding/json (v1-old vs v1-on-v2): $COMPAT_VERDICT."
  if [ "$V2_STRICTER" = "1" ]; then
    echo "Строгость v2 API против encoding/json: v2 API строже (см. блок выше)."
  else
    echo "Строгость v2 API против encoding/json: НЕ обнаружена — v2 API не отверг ни одного случая, принятого encoding/json."
  fi
} > results/02-strictness.txt

if [ "$V2_STRICTER" != "1" ]; then
  echo "ОТКАЗ: v2 API не строже encoding/json ни по одному случаю — либо список случаев" >&2
  echo "       не задевает реальную строгость v2, либо собрали не то. Замер не состоялся." >&2
  exit 1
fi

echo "Отчёт: results/02-strictness.txt"
echo "  совместимость encoding/json: $COMPAT_VERDICT"
echo "  v2 API строже: да"
