#!/usr/bin/env bash
# Общие функции стенда log-backends.
#
# Подключается всеми скриптами: source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

STAND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export MSYS_NO_PATHCONV=1

# Имя docker-сети стенда. Compose называет её по имени каталога проекта.
NETWORK="${LB_NETWORK:-log-backends_default}"

log() { echo "== $*"; }

# Интерпретатор для разбора JSON. Имя не зашито: в Git Bash на Windows команда
# называется python, в Ubuntu — только python3.
# Вывод python — всегда UTF-8. На Windows его stdout по умолчанию в кодировке
# консоли (cp1251), и тогда единицы измерения из human() попадают в отчёт
# другими байтами, чем окружающий текст от echo. Получается файл в смешанной
# кодировке: «532.6 КиБ» читается как «532.6 ???». Проверено — так вышли
# results/02-storage.txt и results/04-cardinality.txt.
export PYTHONIOENCODING=utf-8

PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for c in python3 python; do
    if command -v "$c" >/dev/null 2>&1; then PYTHON_BIN="$c"; break; fi
  done
fi

require_tools() {
  local missing=0
  command -v docker >/dev/null 2>&1 || { echo "нет docker" >&2; missing=1; }
  if [ -z "$PYTHON_BIN" ]; then
    echo "нет python: скрипты разбирают им ответы бэкендов" >&2
    missing=1
  fi
  [ "$missing" = 0 ] || exit 1
}

# Запрос ИЗНУТРИ docker-сети. Проброс портов Docker Desktop работает с
# перебоями, а из сети всё стабильно.
#
# ВНИМАНИЕ: `innet -o файл` создаст файл ВНУТРИ контейнера curl, на хосте его не
# будет. Работать через stdout. И отдельно: $PYTHON_BIN — это python для
# Windows, путей вида /tmp/... для него не существует, поэтому передавать данные
# в него надо через stdin.
innet() {
  docker run --rm --network "$NETWORK" curlimages/curl:8.11.1 "$@"
}

# Размер данных бэкенда. Меряется СНАРУЖИ вспомогательным контейнером: образы
# Loki и VictoriaLogs собраны без шелла, и `du` внутри них не работает
# (проверено, results/00-feasibility.txt). Способ один для всех трёх — иначе
# сравнивались бы методы измерения, а не бэкенды.
volume_bytes() {
  local volume="$1"
  docker run --rm -v "$volume":/data alpine:3.21 du -sb /data 2>/dev/null | awk '{print $1}'
}

# Человекочитаемый размер из байтов.
human() {
  "$PYTHON_BIN" -c "
import sys
b = float(sys.argv[1]) if len(sys.argv) > 1 else 0
for unit in ('Б', 'КиБ', 'МиБ', 'ГиБ'):
    if b < 1024 or unit == 'ГиБ':
        print('%.1f %s' % (b, unit)); break
    b /= 1024
" "$1"
}

require_tools

# Размер ПОДКАТАЛОГОВ ДАННЫХ внутри тома, без журналов.
#
# Зачем отдельно от volume_bytes: состав тома у трёх бэкендов разный, и «весь
# том» сравнивает в том числе журналы упреждающей записи, которые живут своей
# жизнью. Замеренный состав после 100 000 записей:
#
#   Loki:         chunks 549 КБ + индекс 34 КБ, при этом WAL 9 МБ
#   OpenSearch:   indices 21 МБ (translog внутри индекса)
#   VictoriaLogs: datadb 165 КБ + indexdb 9 КБ
#
# То есть у Loki журнал в пятнадцать раз больше самих данных, а у VictoriaLogs
# отдельного журнала в томе нет вовсе. Сравнение по одному только тому сказало
# бы больше о журналах, чем о моделях индексации, — поэтому меряются обе
# величины и обе показываются.
data_bytes() {
  local volume="$1"; shift
  docker run --rm -v "$volume":/data alpine:3.21 sh -c "du -sb $* 2>/dev/null | awk '{s+=\$1} END {print s+0}'"
}
