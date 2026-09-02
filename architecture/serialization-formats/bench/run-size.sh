#!/usr/bin/env bash
# Снимает фикстуру оси размера (Задача 5): гоняет --op=size обеими
# реализациями по всем четырём плечам и складывает результат в
# fixtures/size.txt.
#
# ПОЧЕМУ --change=base --direction=same и больше НИЧЕГО. Ось размера
# меряет «сколько весит запись в этом формате», а не эволюцию схемы —
# поэтому по каждому плечу снимается ровно одна клетка: писатель и
# читатель — одна и та же схема версии 1 (schemas/spec.md, §10.3).
# Прогонять весь квадрат «изменение × направление», как для compat,
# здесь незачем: у оси размера нет для него применения, а раздувание
# фикстуры затруднило бы чтение таблицы.
#
# ПОЧЕМУ ОДИН СКРИПТ НА ОБЕ РЕАЛИЗАЦИИ. Числа сравнимы, только если
# получены ОДНИМ И ТЕМ ЖЕ способом вызова на одних и тех же координатах;
# два отдельных скрипта разошлись бы по мелочам (уровень сжатия, набор
# плеч) молча.
set -euo pipefail

STAND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$STAND/go"
JAVA_DIR="$STAND/java"
FIXTURE_DIR="$STAND/fixtures"
FIXTURE="$FIXTURE_DIR/size.txt"
JAVA_IMAGE="maven:3.9-eclipse-temurin-25"

# Плечи, а не клетки: ось размера сравнивает форматы между собой, а не
# изменения схемы.
FORMATS=(json json-schema avro protobuf)

mkdir -p "$FIXTURE_DIR"

echo "== сборка Go-пробы ==" >&2
# Внутри дерева стенда — go/cmd/probe сам найдёт schemas/manifest.json
# поиском вверх по каталогам (см. cmd/probe/main.go, resolveStandRoot).
( cd "$GO_DIR" && go build -o probe ./cmd/probe )
GO_PROBE="$GO_DIR/probe"
if [ ! -x "$GO_PROBE" ] && [ -x "$GO_PROBE.exe" ]; then
  GO_PROBE="$GO_PROBE.exe"
fi

echo "== сборка Java-пробы (контейнер, монтируется КОРЕНЬ стенда) ==" >&2
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$STAND":/stand -w /stand/java "$JAVA_IMAGE" \
  mvn -q -B package

# Версии — в фикстуру, а не только в лог: без них число не
# воспроизводится (spec.md, §10.3 — оговорка про zstd отдельно).
GO_VERSION="$(go version | awk '{print $3}')"
KLAUSPOST_VERSION="$(grep 'github.com/klauspost/compress ' "$GO_DIR/go.mod" | head -1 | awk '{print $2}')"
xml_value() { grep -m1 "<$1>" "$JAVA_DIR/pom.xml" | sed -E "s#.*<$1>(.*)</$1>.*#\1#"; }
AVRO_VERSION="$(xml_value avro.version)"
PROTOBUF_VERSION="$(xml_value protobuf.version)"
JSONSCHEMA_VERSION="$(xml_value json-schema-validator.version)"
ZSTDJNI_VERSION="$(xml_value zstd-jni.version)"

{
  echo "# Фикстура оси размера (serialization-formats, Задача 5)."
  echo "# Снята: $(date -u +%Y-%m-%dT%H:%M:%SZ) сценарием bench/run-size.sh."
  echo "# Координаты: --change=base --direction=same на всех четырёх плечах"
  echo "# (json, json-schema, avro, protobuf) — ось размера меряет ОДНУ схему"
  echo "# (версию 1), а не эволюцию схемы; см. schemas/spec.md, §10.3."
  echo "# Уровень сжатия zstd зафиксирован: 3 (обычный уровень «по умолчанию»)."
  echo "# bytes, schema_bytes (КАНОНИЧЕСКАЯ форма — Parsing Canonical Form у"
  echo "# Avro, дескриптор без source_code_info у Protobuf, минифицированный"
  echo "# JSON без title у JSON Schema), schema_file_bytes (вес файла как есть)"
  echo "# и batch_hash (SHA-256 содержимого пачки) сравнимы МЕЖДУ реализациями"
  echo "# побайтово — это часть контракта (круг ревью 2, находки C1/C2/M1)."
  echo "# zstd/batch_zstd — НЕТ: у Go и Java разные библиотеки сжатия"
  echo "# (klauspost/compress против libzstd через zstd-jni), и они не обязаны"
  echo "# давать побайтово одинаковый результат — см. оговорку в"
  echo "# schemas/spec.md, §10.3. Кодирование Protobuf детерминировано"
  echo "# (proto.MarshalOptions{Deterministic: true} в Go) — без этого"
  echo "# содержимое пачки не воспроизводится даже внутри одной реализации"
  echo "# (круг ревью 2, находка C3)."
  echo "{\"kind\":\"env\",\"zstd_level\":3,\"go\":{\"go_version\":\"$GO_VERSION\",\"klauspost_compress_version\":\"$KLAUSPOST_VERSION\"},\"java\":{\"image\":\"$JAVA_IMAGE\",\"avro_version\":\"$AVRO_VERSION\",\"protobuf_version\":\"$PROTOBUF_VERSION\",\"json_schema_validator_version\":\"$JSONSCHEMA_VERSION\",\"zstd_jni_version\":\"$ZSTDJNI_VERSION\"}}"

  echo "== Go: --op=size по всем плечам ==" >&2
  for format in "${FORMATS[@]}"; do
    "$GO_PROBE" --format="$format" --change=base --direction=same --op=size
  done

  echo "== Java: --op=size по всем плечам (контейнер) ==" >&2
  # Один контейнер на все четыре плеча — так дешевле, чем поднимать JVM
  # четыре раза, а числа получаются теми же самыми координатами вызова.
  # Предупреждения JVM о native access идут в stderr, поэтому 2>&1 не
  # используется нигде ниже — только штатное stdout попадает в фикстуру.
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$STAND":/stand -w /stand/java "$JAVA_IMAGE" \
    bash -c 'set -e; for f in json json-schema avro protobuf; do
      java -jar target/probe.jar --format="$f" --change=base --direction=same --op=size
    done' 2>/dev/null

  # Маркер завершения. Разбор оборванной фикстуры обязан упасть, а не
  # выдать частичный результат — без него недописанный прогон (сеть
  # легла посреди docker run, диск кончился) выглядел бы как валидные,
  # но неполные данные.
  echo "COMPLETE"
} > "$FIXTURE"

echo "Фикстура записана: $FIXTURE" >&2
