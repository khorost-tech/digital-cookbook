#!/usr/bin/env bash
# run.sh <postgresql|picodata> — прогоняет пробы одним из двух JDBC-драйверов.
#
# Оба драйвера лежат в одном jar-with-dependencies, выбор происходит по схеме
# URL. Фактически использованный драйвер раннер печатает в stderr, чтобы в
# отчёте нельзя было перепутать, кто отвечал.
#
# Сборка идёт в контейнере Maven: локальный JDK не требуется, а кэш ~/.m2
# вынесен в именованный том, иначе каждая сборка заново тянет зависимости.
set -euo pipefail
DIR_DOCKER="$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd))"
COMPAT_DOCKER="$(cd "$(dirname "$0")/../.." && (pwd -W 2>/dev/null || pwd))"
export MSYS_NO_PATHCONV=1

WHICH="${1:?usage: run.sh <postgresql|picodata> [--metadata]}"
case "$WHICH" in
    postgresql) URL="jdbc:postgresql://picodata-1:5432/picodata" ;;
    picodata)   URL="jdbc:picodata://picodata-1:5432/picodata" ;;
    *) echo "!!! неизвестный драйвер: $WHICH (ожидалось postgresql или picodata)" >&2; exit 1 ;;
esac

# Второй аргумент --metadata переключает раннер с SQL-проб на пробы уровня
# DatabaseMetaData — то, чем пользуется GUI-клиент, строя дерево объектов.
ARG2="${2:-/compat/probes.tsv}"

docker run --rm --network picodata-net \
    -v "${DIR_DOCKER}:/app" -v "${COMPAT_DOCKER}:/compat" \
    -v pg-compat-m2:/root/.m2 -w /app maven:3.9-eclipse-temurin-21 \
    sh -c "mvn -q -DskipTests package && \
           java -jar target/pg-compat-jdbc-1.0.0-jar-with-dependencies.jar \
                '${URL}' '${ARG2}'"
