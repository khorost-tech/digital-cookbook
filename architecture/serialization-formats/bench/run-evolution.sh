#!/usr/bin/env bash
# Снимает фикстуру оси эволюции схемы (Задача 6): гоняет --op=compat и
# --op=roundtrip обеими реализациями по всем сочетаниям «изменение ×
# плечо × направление» и складывает результат в fixtures/evolution.txt.
#
# ПОЧЕМУ ИМЕННО ЭТИ КООРДИНАТЫ. Таблица эволюции (schemas/expected.json)
# описывает семь изменений схемы (без base — у базовой версии нет второй
# половины пары, см. spec.md §4.3), три плеча (avro, protobuf,
# json-schema — контрольное json в этой таблице не участвует, spec.md
# §13) и два направления (newer_reader, newer_writer — same не входит в
# таблицу предсказаний, spec.md §3.6). --op перебирает оба вида пробы:
# compat — обычное чтение, roundtrip — перенос неизвестного читателю
# поля через повторное кодирование (осмыслено только для protobuf у
# остальных плеч это n/a, но строка всё равно печатается на каждую
# запись, spec.md §9.2).
#
# ПОЧЕМУ ОДИН СКРИПТ НА ОБЕ РЕАЛИЗАЦИИ — та же причина, что и в
# run-size.sh: числа (здесь — исходы) сравнимы, только если получены
# ОДНИМ И ТЕМ ЖЕ способом вызова на одних и тех же координатах.
#
# ЧЕГО ЗДЕСЬ НЕТ. Проба вызывается КООРДИНАТАМИ, без путей и без данных
# (schemas/spec.md §4.2) — сама проба вычисляет исход и печатает
# наблюдаемое значение (поле "got"); эта фикстура их только СНИМАЕТ и не
# трогает.
set -euo pipefail

STAND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$STAND/go"
JAVA_DIR="$STAND/java"
FIXTURE_DIR="$STAND/fixtures"
FIXTURE="$FIXTURE_DIR/evolution.txt"
JAVA_IMAGE="maven:3.9-eclipse-temurin-25"

# Таблица эволюции: плечо контроля (json) сюда не входит (spec.md §13),
# base — тоже (у него нет пары для newer_reader/newer_writer, §4.3).
#
# alias_conflict и retype_message — круг правок (задача 6bis): оба
# добавлены как целенаправленные контрпримеры к "слишком красивым нулям"
# исходных семи изменений (schemas/spec.md §15).
FORMATS=(avro protobuf json-schema)
CHANGES=(add_default add_nodefault remove rename retype reuse_tag unknown_field alias_conflict retype_message)
DIRECTIONS=(newer_reader newer_writer)
OPS=(compat roundtrip)
RECORDS_PER_CELL=5
LANGS=2

# Круг правок: разбор больше не верит фикстуре на слово — маркер
# COMPLETE ловит только ОБРЫВ, а не "тихо напечатали меньше строк и
# вышли нулём". Общее число строк данных объявляется в шапке заранее и
# сверяется разбором (scripts/analyze-evolution.py, TruncatedFixture).
EXPECTED_LINES=$(( ${#FORMATS[@]} * ${#CHANGES[@]} * ${#DIRECTIONS[@]} * ${#OPS[@]} * RECORDS_PER_CELL * LANGS ))

mkdir -p "$FIXTURE_DIR"

echo "== сборка Go-пробы ==" >&2
( cd "$GO_DIR" && go build -o probe ./cmd/probe )
GO_PROBE="$GO_DIR/probe"
if [ ! -x "$GO_PROBE" ] && [ -x "$GO_PROBE.exe" ]; then
  GO_PROBE="$GO_PROBE.exe"
fi

echo "== сборка Java-пробы (контейнер, монтируется КОРЕНЬ стенда) ==" >&2
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$STAND":/stand -w /stand/java "$JAVA_IMAGE" \
  mvn -q -B package

GO_VERSION="$(go version | awk '{print $3}')"
xml_value() { grep -m1 "<$1>" "$JAVA_DIR/pom.xml" | sed -E "s#.*<$1>(.*)</$1>.*#\1#"; }
AVRO_VERSION="$(xml_value avro.version)"
PROTOBUF_VERSION="$(xml_value protobuf.version)"
JSONSCHEMA_VERSION="$(xml_value json-schema-validator.version)"

{
  echo "# Фикстура оси эволюции схемы (serialization-formats, Задача 6/6bis)."
  echo "# Снята: $(date -u +%Y-%m-%dT%H:%M:%SZ) сценарием bench/run-evolution.sh."
  echo "# Координаты: 9 изменений (без base) x 3 плеча (avro, protobuf,"
  echo "# json-schema — контрольное json в таблицу эволюции не входит,"
  echo "# schemas/spec.md §13) x 2 направления (newer_reader, newer_writer —"
  echo "# same в таблицу предсказаний не входит, §3.6) x 2 вида пробы"
  echo "# (compat, roundtrip) x 5 канонических записей x 2 реализации ="
  echo "# $EXPECTED_LINES строк данных (без этой шапки, env-строки и COMPLETE)."
  echo "# Круг правок: это число объявлено ЗАРАНЕЕ и сверяется разбором — тихий"
  echo "# прогон, напечатавший МЕНЬШЕ строк и вышедший кодом 0, обязан быть"
  echo "# пойман, а не остаться незамеченным (см. analyze-evolution.py)."
  echo "# Исходы вычисляет проба сама (пять возможных: ok, refused, wrong,"
  echo "# n/a, error — §1); каждая строка несёт поле \"got\" — ФАКТИЧЕСКИ"
  echo "# прочитанную запись, когда декодирование состоялось, — без него"
  echo "# исход wrong ничем не подтверждён (решение Задачи 6)."
  echo "{\"kind\":\"env\",\"expected_lines\":$EXPECTED_LINES,\"go\":{\"go_version\":\"$GO_VERSION\"},\"java\":{\"image\":\"$JAVA_IMAGE\",\"avro_version\":\"$AVRO_VERSION\",\"protobuf_version\":\"$PROTOBUF_VERSION\",\"json_schema_validator_version\":\"$JSONSCHEMA_VERSION\"}}"

  echo "== Go: compat и roundtrip по всем координатам ==" >&2
  for op in "${OPS[@]}"; do
    for format in "${FORMATS[@]}"; do
      for change in "${CHANGES[@]}"; do
        for direction in "${DIRECTIONS[@]}"; do
          "$GO_PROBE" --format="$format" --change="$change" --direction="$direction" --op="$op"
        done
      done
    done
  done

  echo "== Java: compat и roundtrip по всем координатам (контейнер) ==" >&2
  # Один контейнер на весь квадрат — так дешевле, чем поднимать JVM на
  # каждую клетку, а числа получаются теми же самыми координатами вызова.
  #
  # Круг правок: поток ошибок контейнера БОЛЬШЕ НЕ ГАСИТСЯ (было
  # "2>/dev/null" в конце этого вызова). Раньше прогон, напечатавший
  # меньше строк, чем нужно, и всё равно вышедший кодом 0 (например,
  # если один java-вызов упал бы не по пути `set -e`, а поймав
  # исключение внутри и просто ничего не напечатав), не оставлял ни
  # одного видимого следа — ни в фикстуре (маркер COMPLETE ловит только
  # ОБРЫВ потока, не пропуск строк посередине), ни в консоли (стандартные
  # предупреждения JVM про native access и вся диагностика падений шли в
  # одну и ту же трубу и выбрасывались целиком). Диагностика JVM теперь
  # видна на stderr ЭТОГО скрипта — отдельно от фикстуры (которая
  # собирается только из stdout), но не потеряна молча.
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$STAND":/stand -w /stand/java "$JAVA_IMAGE" \
    bash -c '
      set -e
      for op in compat roundtrip; do
        for format in avro protobuf json-schema; do
          for change in add_default add_nodefault remove rename retype reuse_tag unknown_field alias_conflict retype_message; do
            for direction in newer_reader newer_writer; do
              java -jar target/probe.jar --format="$format" --change="$change" --direction="$direction" --op="$op"
            done
          done
        done
      done
    '

  # Маркер завершения. Разбор оборванной фикстуры обязан упасть, а не
  # выдать частичный результат — тот же принцип, что и у оси размера.
  echo "COMPLETE"
} > "$FIXTURE"

echo "Фикстура записана: $FIXTURE" >&2
