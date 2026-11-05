#!/bin/sh
# Сборка сервиса — ОДНА И ТА ЖЕ команда для трёх слоёв.
#
# Единственное, что приходит снаружи, — имя слоя: его получает CMake,
# чтобы выбрать точку входа и библиотеку слоя. Флаги, стандарт языка и
# режим сборки здесь общие, и разойтись им негде: файл один на три
# варианта.
set -eu

layer="${1:-}"
if [ -z "$layer" ]; then
    echo "слой HTTP не задан: сборка не выбирает его сама" >&2
    exit 2
fi

cmake -S /src -B /build -DCMAKE_BUILD_TYPE=Release -DSTAND_LAYER="$layer"
cmake --build /build -j
install -m 0755 /build/stand_service /usr/local/bin/stand_service
