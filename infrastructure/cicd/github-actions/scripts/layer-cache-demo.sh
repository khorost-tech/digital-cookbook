#!/usr/bin/env bash
# Раздел github-actions: эффект Docker layer cache и цена порядка инструкций.
#
# BuildKit кэширует РЕЗУЛЬТАТ каждого слоя и переиспользует его, пока не изменился
# ни сам слой, ни один из предыдущих. Отсюда два наблюдения:
#   1. тёплая сборка без изменений почти мгновенна против холодной;
#   2. порядок инструкций решает ГЛУБИНУ инвалидации. В правильном Dockerfile
#      зависимости качаются в слое ВЫШЕ копирования исходников, поэтому правка
#      кода не роняет слой `go mod download`. В «плохом» порядке (COPY . . до
#      download) правка любой строки кода обнуляет и скачивание зависимостей.
#
# Цену порядка меряем не временем (для одной мелкой зависимости разница в секундах
# мала и шумна), а ФАКТОМ: сколько слоёв BuildKit пометил CACHED при пересборке
# после правки кода. В правильном порядке слой download остаётся кэшированным, в
# плохом — нет.
#
# ⚠️ Не тайминги GitHub Actions. Переносим характер: тёплый слой-кэш кратно дешевле
# холодного; правильный порядок слоёв защищает дорогие слои от правок кода.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../app"

docker buildx version >/dev/null 2>&1 || {
    echo "ОШИБКА: docker buildx недоступен." >&2; exit 1; }

TAG_GOOD="cicd-layercache-good:demo"
TAG_BAD="cicd-layercache-bad:demo"
BAD_DOCKERFILE="$(mktemp)"
SRC="main.go"
SRC_BAK="$(mktemp)"
cp "$SRC" "$SRC_BAK"
cleanup() {
    cp "$SRC_BAK" "$SRC"; rm -f "$SRC_BAK" "$BAD_DOCKERFILE"
    docker image rm -f "$TAG_GOOD" "$TAG_BAD" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# «Плохой» Dockerfile: исходники копируются ДО скачивания зависимостей, поэтому
# любая правка кода инвалидирует слой go mod download.
cat > "$BAD_DOCKERFILE" <<'DOCKER'
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 go build -o /out/app .
FROM alpine:3.21
COPY --from=builder /out/app /app
ENTRYPOINT ["/app"]
DOCKER

measure() {
    local start end
    start=$(date +%s.%N)
    "$@" >/dev/null 2>&1
    end=$(date +%s.%N)
    awk "BEGIN{printf \"%.3f\", $end-$start}"
}

edit_code() { printf '\n// cache-bust %s\n' "$RANDOM$RANDOM" >> "$SRC"; }
restore_code() { cp "$SRC_BAK" "$SRC"; }

echo "=== (1) Правильный Dockerfile: холодная против тёплой ==="
T_COLD=$(measure docker buildx build --no-cache -t "$TAG_GOOD" -f Dockerfile --load .)
echo "холодная (--no-cache): ${T_COLD} с"
T_WARM=$(measure docker buildx build -t "$TAG_GOOD" -f Dockerfile --load .)
echo "тёплая (без изменений): ${T_WARM} с"

echo
echo "=== (2) Цена порядка слоёв: пересборка после правки КОДА ==="
# Правильный порядок: правим код, пересобираем, считаем CACHED-слои.
edit_code
GOOD_OUT=$(docker buildx build --progress=plain -t "$TAG_GOOD" -f Dockerfile --load . 2>&1)
GOOD_CACHED=$(printf '%s\n' "$GOOD_OUT" | grep -c 'CACHED' || true)
restore_code

# Плохой порядок: заполняем кэш, потом та же правка кода.
docker buildx build -t "$TAG_BAD" -f "$BAD_DOCKERFILE" --load . >/dev/null 2>&1
edit_code
BAD_OUT=$(docker buildx build --progress=plain -t "$TAG_BAD" -f "$BAD_DOCKERFILE" --load . 2>&1)
BAD_CACHED=$(printf '%s\n' "$BAD_OUT" | grep -c 'CACHED' || true)
restore_code

# Проверяем, кэширован ли конкретно слой скачивания зависимостей.
good_download_cached=no
bad_download_cached=no
printf '%s\n' "$GOOD_OUT" | grep -A1 'go mod download' | grep -q 'CACHED' && good_download_cached=yes
printf '%s\n' "$BAD_OUT"  | grep -A1 'go mod download' | grep -q 'CACHED' && bad_download_cached=yes

echo "правильный порядок: CACHED-слоёв=${GOOD_CACHED}, слой go mod download кэширован=${good_download_cached}"
echo "плохой порядок:     CACHED-слоёв=${BAD_CACHED}, слой go mod download кэширован=${bad_download_cached}"

echo
echo "=== СВОДКА ==="
awk -v c="$T_COLD" -v w="$T_WARM" 'BEGIN{
    printf "холодная:  %6.3f с\n", c
    printf "тёплая:    %6.3f с  (x%.1f быстрее холодной)\n", w, (w>0? c/w : 0)
}'
echo "после правки кода слой go mod download: правильный=${good_download_cached}, плохой=${bad_download_cached}"

echo
echo "ПАДАЮЩИЙ ВАРИАНТ: в плохом порядке (COPY . . до download) правка кода обнуляет"
echo "скачивание зависимостей — слой download пересобирается. В правильном он остаётся"
echo "кэшированным. Если бы порядок не влиял, оба варианта дали бы одинаковый набор"
echo "CACHED-слоёв."

# Самопроверки.
[ "$good_download_cached" = yes ] || {
    echo "ОШИБКА: в правильном порядке слой download НЕ кэширован — демо не показало эффект." >&2; exit 1; }
[ "$bad_download_cached" = no ] || {
    echo "ОШИБКА: в плохом порядке слой download кэширован — контраст не воспроизвёлся." >&2; exit 1; }
awk -v c="$T_COLD" -v w="$T_WARM" 'BEGIN{ exit !(c > w) }' || {
    echo "ОШИБКА: тёплая сборка не быстрее холодной." >&2; exit 1; }
echo
echo "OK: тёплый слой-кэш быстрее холодного; правильный порядок защитил слой download."
