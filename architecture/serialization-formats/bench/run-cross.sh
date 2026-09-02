#!/usr/bin/env bash
# Снимает фикстуру оси перекрёстного чтения (Задача 8): байты кодирует
# ОДНА реализация, читает — ДРУГАЯ, через файл обмена в рабочем каталоге
# прогона, а не внутри одного процесса. Плюс проба байтовой идентичности
# с контролем (schemas/spec.md §17).
#
# ПОЧЕМУ ИМЕННО ЭТИ КООРДИНАТЫ. Полный перебор всех девяти изменений на
# всех трёх нотациях для четырёх сочетаний писатель/читатель не обязателен
# (spec.md §17.5) — матрица обязана остаться обозримой. Выбраны
# изменения, где два независимых читателя ОДНОЙ нотации могут разойтись
# между собой (зависит от алгоритма разрешения схем библиотеки, а не
# только от формата провода): умолчания при отсутствующем поле
# (add_default, add_nodefault), неизвестное поле (unknown_field),
# переименование через псевдоним (rename), конфликт псевдонимов
# (alias_conflict). alias_conflict у Protobuf вырожден по построению
# (schemas/spec.md §6.4, зеркально reuse_tag у Avro/JSON Schema — см.
# user_v2_alias_conflict.proto) — включать его в протобуфную часть
# матрицы значило бы просто печатать n/a на каждой из четырёх строк
# писатель/читатель, поэтому протобуфный список изменений его не несёт.
#
# ПОЧЕМУ ОДИН СКРИПТ НА ОБЕ РЕАЛИЗАЦИИ — та же причина, что и в
# run-evolution.sh: числа сравнимы, только если получены ОДНИМ И ТЕМ ЖЕ
# способом вызова на одних и тех же координатах.
#
# КАТАЛОГ ОБМЕНА — рабочий каталог ПРОЦЕССА пробы на момент вызова
# (spec.md §17.2), НЕ аргумент и НЕ каталог стенда: этот сценарий явно
# переключается в него (`cd`) перед вызовом Go-пробы и передаёт его
# контейнеру Java как рабочий каталог (`-w`). Каталог ОЧИЩАЕТСЯ перед
# прогоном — иначе дайджест внутри устаревшего файла может оказаться
# самосогласованным (посчитан при прошлой записи) и не поймает подмену
# координатами старого прогона (spec.md §17.3).
set -euo pipefail

STAND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$STAND/go"
JAVA_DIR="$STAND/java"
FIXTURE_DIR="$STAND/fixtures"
FIXTURE="$FIXTURE_DIR/cross.txt"
EXCHANGE_DIR="$FIXTURE_DIR/cross-exchange"
JAVA_IMAGE="maven:3.9-eclipse-temurin-25"

# Изменения, где читатели одной нотации имеют право разойтись между
# собой (spec.md §17.5) — раздельно по нотации, потому что alias_conflict
# вырожден у Protobuf (см. преамбулу выше).
AVRO_CHANGES=(add_default add_nodefault unknown_field rename alias_conflict)
PROTOBUF_CHANGES=(add_default add_nodefault unknown_field rename)
DIRECTIONS=(newer_reader newer_writer)
RECORDS_PER_CELL=5
# Четыре сочетания писатель/читатель на клетку (spec.md §17.4): два
# контрольных (go/go, java/java) — обязаны построчно совпасть с осью
# эволюции; два перекрёстных (go/java, java/go) — собственно находка оси.
WRITER_READER_PAIRS=4

IDENTITY_FORMATS=(json json-schema avro protobuf)
IDENTITY_LANGS=2

CROSS_CELLS=$(( ${#AVRO_CHANGES[@]} + ${#PROTOBUF_CHANGES[@]} ))
CROSS_LINES=$(( CROSS_CELLS * ${#DIRECTIONS[@]} * RECORDS_PER_CELL * WRITER_READER_PAIRS ))
IDENTITY_LINES=$(( ${#IDENTITY_FORMATS[@]} * IDENTITY_LANGS ))
EXPECTED_LINES=$(( CROSS_LINES + IDENTITY_LINES ))

rm -rf "$EXCHANGE_DIR"
mkdir -p "$FIXTURE_DIR" "$EXCHANGE_DIR"

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

# changes_for — список изменений для нотации: alias_conflict вырожден у
# Protobuf (см. преамбулу), поэтому список раздельный.
changes_for() {
  case "$1" in
    avro) printf '%s\n' "${AVRO_CHANGES[@]}" ;;
    protobuf) printf '%s\n' "${PROTOBUF_CHANGES[@]}" ;;
  esac
}

{
  echo "# Фикстура оси перекрёстного чтения (serialization-formats, Задача 8)."
  echo "# Снята: $(date -u +%Y-%m-%dT%H:%M:%SZ) сценарием bench/run-cross.sh."
  echo "# Координаты cross: ${#AVRO_CHANGES[@]} изменения x avro + ${#PROTOBUF_CHANGES[@]}"
  echo "# изменения x protobuf (alias_conflict вырожден у protobuf, schemas/spec.md"
  echo "# §6.4 — исключён из его списка, а не измерен как n/a-клетка) x 2 направления"
  echo "# x 4 сочетания писатель/читатель (go/go, go/java, java/go, java/java) x 5"
  echo "# канонических записей = $CROSS_LINES строк kind=cross. Плюс проба идентичности:"
  echo "# ${#IDENTITY_FORMATS[@]} плеча x 2 реализации = $IDENTITY_LINES строк"
  echo "# kind=identity-probe. Итого $EXPECTED_LINES строк данных (без этой шапки,"
  echo "# env-строки и COMPLETE) — число объявлено ЗАРАНЕЕ и сверяется разбором"
  echo "# (scripts/analyze-cross.py), тем же принципом, что и в run-evolution.sh."
  echo "# Два контрольных сочетания (писатель и читатель — один язык) обязаны"
  echo "# построчно совпасть с колонкой compat оси эволюции (fixtures/evolution.txt)"
  echo "# на тех же координатах — расхождение здесь Critical (spec.md §17.4)."
  echo "{\"kind\":\"env\",\"expected_lines\":$EXPECTED_LINES,\"go\":{\"go_version\":\"$GO_VERSION\"},\"java\":{\"image\":\"$JAVA_IMAGE\",\"avro_version\":\"$AVRO_VERSION\",\"protobuf_version\":\"$PROTOBUF_VERSION\"}}"

  echo "== отдать байты: go (writer=go, все координаты) ==" >&2
  ( cd "$EXCHANGE_DIR" && for format in avro protobuf; do
      for change in $(changes_for "$format"); do
        for direction in "${DIRECTIONS[@]}"; do
          "$GO_PROBE" --format="$format" --change="$change" --direction="$direction" --op=cross-emit
        done
      done
    done )

  echo "== отдать байты: java (writer=java, все координаты, контейнер) ==" >&2
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$STAND":/stand -w /stand/fixtures/cross-exchange "$JAVA_IMAGE" \
    bash -c '
      set -e
      emit() {
        format="$1"; shift
        for change in "$@"; do
          for direction in newer_reader newer_writer; do
            java -jar /stand/java/target/probe.jar --format="$format" --change="$change" --direction="$direction" --op=cross-emit
          done
        done
      }
      emit avro add_default add_nodefault unknown_field rename alias_conflict
      emit protobuf add_default add_nodefault unknown_field rename
    '

  echo "== принять чужие байты: go читает go и java ==" >&2
  ( cd "$EXCHANGE_DIR" && for format in avro protobuf; do
      for change in $(changes_for "$format"); do
        for direction in "${DIRECTIONS[@]}"; do
          for writer_lang in go java; do
            "$GO_PROBE" --format="$format" --change="$change" --direction="$direction" --op=cross-accept --writer-lang="$writer_lang"
          done
        done
      done
    done )

  echo "== принять чужие байты: java читает go и java (контейнер) ==" >&2
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$STAND":/stand -w /stand/fixtures/cross-exchange "$JAVA_IMAGE" \
    bash -c '
      set -e
      accept() {
        format="$1"; shift
        for change in "$@"; do
          for direction in newer_reader newer_writer; do
            for writer_lang in go java; do
              java -jar /stand/java/target/probe.jar --format="$format" --change="$change" --direction="$direction" --op=cross-accept --writer-lang="$writer_lang"
            done
          done
        done
      }
      accept avro add_default add_nodefault unknown_field rename alias_conflict
      accept protobuf add_default add_nodefault unknown_field rename
    '

  echo "== проба идентичности: go (контроль + дайджест на четыре плеча) ==" >&2
  ( cd "$EXCHANGE_DIR" && for format in "${IDENTITY_FORMATS[@]}"; do
      "$GO_PROBE" --format="$format" --change=base --direction=same --op=identity
    done )

  echo "== проба идентичности: java (контейнер) ==" >&2
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$STAND":/stand -w /stand/fixtures/cross-exchange "$JAVA_IMAGE" \
    bash -c '
      set -e
      for format in json json-schema avro protobuf; do
        java -jar /stand/java/target/probe.jar --format="$format" --change=base --direction=same --op=identity
      done
    '

  echo "COMPLETE"
} > "$FIXTURE"

rm -rf "$EXCHANGE_DIR"
echo "Фикстура записана: $FIXTURE" >&2
