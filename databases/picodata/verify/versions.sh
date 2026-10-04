#!/usr/bin/env bash
# versions.sh — версия кластера должна совпадать с версией picodata-plugin, на
# которую собран плагин, иначе Picodata откажется его включать. Проверка версий
# отключается переменной окружения, но документация этого не рекомендует, и
# стенд показывает штатный путь: подобрать совпадающие версии.
#
# Шаблон pike 5.4.0 пинит picodata-plugin = "=26.1.3", поэтому кластер поднят на
# образе 26.1.3. На crates.io на 2026-07-23 уже есть 26.1.5, а в реестре
# доступны образы 26.1.3, 26.1.5 и 26.1.6 — при обновлении двигать обе стороны
# одновременно.
set -euo pipefail
STAND_DIR="$(cd "$(dirname "$0")/.." && pwd)"

CLUSTER_VER="$(docker exec pd-1 picodata --version | awk '/^picodata/ {print $2}' | tr -d ',')"

PLUGIN_TOML="$STAND_DIR/plugin/Cargo.toml"
if [ ! -f "$PLUGIN_TOML" ]; then
    echo "!!! нет $PLUGIN_TOML — плагин ещё не сгенерирован (см. Task 3)" >&2
    exit 1
fi
PLUGIN_VER="$(grep -oE 'picodata-plugin *= *"=?[0-9]+\.[0-9]+\.[0-9]+"' "$PLUGIN_TOML" \
              | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')"

echo "кластер:         $CLUSTER_VER"
echo "picodata-plugin: $PLUGIN_VER"

if [ -z "$PLUGIN_VER" ]; then
    echo "!!! не нашёл версию picodata-plugin в $PLUGIN_TOML" >&2
    exit 1
fi
if [ "$CLUSTER_VER" != "$PLUGIN_VER" ]; then
    echo "!!! версии расходятся — плагин не включится штатным путём" >&2
    exit 1
fi
echo "ok: версии совпадают"
