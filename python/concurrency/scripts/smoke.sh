#!/usr/bin/env bash
# Проверка каркаса: в образе действительно ДВА РАЗНЫХ интерпретатора — одной
# версии, но с разной моделью памяти. Если сборки окажутся одинаковыми, стенд
# будет мерить одно и то же дважды, и по числам это неотличимо от результата.
set -euo pipefail

# Git Bash на Windows иначе превращает /opt/... в путь вида C:/Program Files/...
export MSYS_NO_PATHCONV=1

cd "$(dirname "$0")/.."

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

probe() {
  docker compose exec -T bench "$1" -c \
    'import sys; print(sys.version.split()[0], sys._is_gil_enabled())'
}

gil=$(probe /opt/py/gil/bin/python | tr -d '\r')
nogil=$(probe /opt/py/nogil/bin/python | tr -d '\r')

echo "сборка с GIL:   $gil"
echo "сборка без GIL: $nogil"

case "$gil"   in *" True")  ;; *) fail "обычная сборка не сообщает о включённом GIL: $gil" ;; esac
case "$nogil" in *" False") ;; *) fail "free-threaded сборка сообщает о ВКЛЮЧЁННОМ GIL: $nogil" ;; esac

# Версии обязаны совпадать. Иначе замер сравнивал бы не наличие GIL,
# а разные версии интерпретатора — и это не было бы видно по числам.
[ "${gil%% *}" = "${nogil%% *}" ] \
  || fail "версии различаются (${gil%% *} против ${nogil%% *}) — сравнение мешало бы версию с моделью памяти"

echo "SMOKE OK: Python ${gil%% *}, две сборки — GIL включён и выключен"
