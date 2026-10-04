#!/usr/bin/env bash
# Снимает фикстуру оси 3 (Задача 7): что нужно ИМЕТЬ, чтобы прочитать
# запись, и первая внешняя зависимость стенда — реестр схем Apicurio.
# Результат — fixtures/need.txt.
#
# ПОЧЕМУ РЕЕСТР ПОДНИМАЕТСЯ И ГАСИТСЯ ИМЕННО ТАК. Конфигурация —
# РЕШЕНИЕ КОНТРОЛЛЕРА, проверенное живьём (task-7-brief.md): хранилище
# «в памяти» у Apicurio не существует (падает с «No Registry storage
# variant defined for value mem»), рабочее сочетание —
# APICURIO_STORAGE_KIND=sql + APICURIO_STORAGE_SQL_KIND=h2. Порт —
# непопулярный (18094), чтобы не столкнуться с чужим стендом на той же
# машине (в частности, с реестром Kafka-стенда — см. ниже). Состояние
# между прогонами НЕ переносится: контейнер создаётся заново при каждом
# запуске сценария и уничтожается в конце (trap), а не переиспользуется.
#
# ПОЧЕМУ ЭТО НЕ ТОТ ЖЕ РЕЕСТР, ЧТО У KAFKA-СТЕНДА. В messaging/kafka
# реестр хранит схемы В САМОЙ KAFKA — трёхброкерный кластер ради проверки
# совместимости здесь не нужен и не поднимается: этот реестр — отдельный
# контейнер с собственным H2-хранилищем, не зависящий ни от какого брокера
# (task-7-brief.md, «РЕШЕНИЕ КОНТРОЛЛЕРА»).
#
# ПОЧЕМУ НЕСКОЛЬКО ВЫЗОВОВ needprobe, А НЕ ОДИН. В отличие от
# run-size.sh/run-evolution.sh, здесь измеряемое свойство — сама
# ДОСТУПНОСТЬ реестра, поэтому пробе физически нужно быть вызванной и
# ПРИ ПОДНЯТОМ, и ПРИ ПОГАШЕННОМ реестре — одна и та же попытка чтения в
# двух разных внешних условиях. См. go/cmd/needprobe/main.go — doc-
# комментарий пакета описывает все шаги по порядку.
set -euo pipefail

STAND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$STAND/go"
FIXTURE_DIR="$STAND/fixtures"
FIXTURE="$FIXTURE_DIR/need.txt"
# Тег ЗАПИНЕН, а не плавающий ("latest"): тройное расхождение на
# alias_conflict (Go-Avro -> wrong, Java-Avro -> refused, реестр ->
# schema_error; README.md, spec.md §16.2) снято на КОНКРЕТНОЙ версии
# реестра и привязано к ней документально. Плавающий тег сверял бы
# версию ПОСЛЕ того, как она уже скачалась, -- та же ловушка, что с
# версией брокера в другом стенде: тег образа не версия.
# REGISTRY_EXPECTED_VERSION ниже сверяется с тем, что реально ответил
# поднятый контейнер (см. проверку сразу после system/info).
REGISTRY_IMAGE="apicurio/apicurio-registry:3.3.1"
REGISTRY_EXPECTED_VERSION="3.3.1"
REGISTRY_CONTAINER="serialization-formats-need-registry"
REGISTRY_PORT=18094
REGISTRY_URL="http://localhost:$REGISTRY_PORT"

WORKDIR="$(mktemp -d)"
ENVELOPE_FILE="$WORKDIR/envelope.json"

cleanup() {
  docker rm -f "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$FIXTURE_DIR"

echo "== сборка Go-пробы (needprobe) ==" >&2
( cd "$GO_DIR" && go build -o needprobe ./cmd/needprobe )
NEEDPROBE="$GO_DIR/needprobe"
if [ ! -x "$NEEDPROBE" ] && [ -x "$NEEDPROBE.exe" ]; then
  NEEDPROBE="$NEEDPROBE.exe"
fi

# Java в этой оси не участвует (см. task-7-report.md): требования брифа —
# про поведение РЕЕСТРА и про конверт провода, не про расхождение двух
# библиотек одного формата, которое измеряют compat/roundtrip/size. Обе
# реализации Avro (hamba/avro и org.apache.avro) уже сведены в матрице
# эволюции — вопрос этой оси другой: что говорит ТРЕТЬЯ, независимая от
# обеих, реализация проверки совместимости (сам реестр).

echo "== запуск реестра Apicurio ($REGISTRY_IMAGE, порт $REGISTRY_PORT) ==" >&2
docker rm -f "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$REGISTRY_CONTAINER" -p "$REGISTRY_PORT:8080" \
  -e APICURIO_STORAGE_KIND=sql -e APICURIO_STORAGE_SQL_KIND=h2 \
  "$REGISTRY_IMAGE" >/dev/null

echo "== ожидание готовности реестра (поллинг, не фиксированный сон) ==" >&2
READY=""
SYSTEM_INFO=""
for _ in $(seq 1 60); do
  if SYSTEM_INFO="$(curl -s -f "$REGISTRY_URL/apis/registry/v3/system/info" 2>/dev/null)"; then
    READY=1
    break
  fi
  sleep 2
done
if [ -z "$READY" ]; then
  echo "реестр не поднялся за отведённое время" >&2
  exit 1
fi
REGISTRY_VERSION="$(printf '%s' "$SYSTEM_INFO" | grep -o '"version":"[^"]*"' | head -1 | sed -E 's/.*:"([^"]*)"/\1/')"
if [ "$REGISTRY_VERSION" != "$REGISTRY_EXPECTED_VERSION" ]; then
  # Тег теперь запинен, поэтому расхождение здесь означает не дрейф
  # плавающего тега, а то, что скачался НЕ тот образ, который объявлен
  # (испорченный локальный кеш, реестр отдал не то, опечатка в
  # REGISTRY_IMAGE выше) -- останавливаемся, а не тихо снимаем фикстуру
  # с непроверенной версией.
  echo "версия поднятого реестра ($REGISTRY_VERSION) не совпадает с запиненным тегом образа ($REGISTRY_EXPECTED_VERSION) -- $REGISTRY_IMAGE" >&2
  exit 1
fi

GO_VERSION="$(go version | awk '{print $3}')"

{
  echo "# Фикстура оси 3 (serialization-formats, Задача 7): что нужно"
  echo "# ИМЕТЬ, чтобы прочитать запись, и конверт реестра."
  echo "# Снята: $(date -u +%Y-%m-%dT%H:%M:%SZ) сценарием bench/run-need-schema.sh."
  echo "# Реестр: $REGISTRY_IMAGE, версия $REGISTRY_VERSION, порт $REGISTRY_PORT,"
  echo "# хранилище SQL/H2 (вариант «в памяти» у Apicurio не существует —"
  echo "# см. task-7-brief.md, «РЕШЕНИЕ КОНТРОЛЛЕРА»)."
  echo "# Только реализация Go: эта ось проверяет поведение РЕЕСТРА и конверта"
  echo "# провода, а не расхождение двух библиотек одного формата (то уже"
  echo "# закрыто осью эволюции, fixtures/evolution.txt)."
  echo "{\"kind\":\"env\",\"go\":{\"go_version\":\"$GO_VERSION\"},\"registry\":{\"image\":\"$REGISTRY_IMAGE\",\"version\":\"$REGISTRY_VERSION\",\"port\":$REGISTRY_PORT}}"

  echo "== требование 1: точка отказа реестра ДО записи (9 изменений x BACKWARD) ==" >&2
  "$NEEDPROBE" --step=registry-matrix --registry="$REGISTRY_URL"

  echo "== подготовка конверта (регистрация базовой схемы + кодирование записи 0) ==" >&2
  "$NEEDPROBE" --step=produce --registry="$REGISTRY_URL" --out="$ENVELOPE_FILE" >&2

  echo "== leg=registry_up: реестр поднят, читаем через него ==" >&2
  "$NEEDPROBE" --step=need --leg=registry_up --registry="$REGISTRY_URL" --envelope-file="$ENVELOPE_FILE"

  echo "== leg=schema_local: реестр не участвует, схема из локального файла ==" >&2
  "$NEEDPROBE" --step=need --leg=schema_local --envelope-file="$ENVELOPE_FILE"

  echo "== требование 3: наивный декодер конверта (без учёта 5-байтового префикса) ==" >&2
  "$NEEDPROBE" --step=envelope --envelope-file="$ENVELOPE_FILE"

  echo "== need-other: protobuf/json/json-schema без реестра ==" >&2
  "$NEEDPROBE" --step=need-other

  echo "== гашение реестра ==" >&2
  docker stop "$REGISTRY_CONTAINER" >/dev/null

  echo "== leg=registry_down: та же попытка чтения, реестр погашен (требование 2) ==" >&2
  "$NEEDPROBE" --step=need --leg=registry_down --registry="$REGISTRY_URL" --envelope-file="$ENVELOPE_FILE"

  # Маркер завершения — тот же принцип, что и у остальных осей: разбор
  # оборванной фикстуры обязан упасть, а не выдать частичный результат.
  echo "COMPLETE"
} > "$FIXTURE"

echo "Фикстура записана: $FIXTURE" >&2
