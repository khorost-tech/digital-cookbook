#!/usr/bin/env bash
# Сводный прогон стенда: с нуля до маркера завершения.
#
# МАРКЕР СТАВИТСЯ ТОЛЬКО ПОСЛЕ ВСЕГО.
#
# Оборванный прогон не должен выглядеть как полный — просто с меньшим
# числом строк. Поэтому маркер пишется последним и только если прошли
# все четыре оси И весь набор проверок.
#
# ПОЧЕМУ КОД ВОЗВРАТА ПРОВЕРЯЕТСЯ ПОСЛЕ КАЖДОГО ШАГА, А НЕ В КОНЦЕ.
# Конвейер глотает код возврата: шаг, отбитый на середине, дважды
# выглядел бы успешным. Здесь каждый шаг проверяется отдельно.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

MARKER="fixtures/COMPLETE"
rm -f "$MARKER"

# --- предполётная проверка -------------------------------------------
#
# Стенд объявляет свои хостовые зависимости и проверяет их ДО первого
# шага.
#
# Раньше их не проверял никто. README обещал только Docker и Go, а прогон
# звал ещё и python, и хостовый openssl. У автора нашлось и то и другое,
# и прогон «проходил целиком»; на чужой машине он падал на середине —
# сначала на отсутствии python, потом на OpenSSL 3.0, где нет ключа
# -not_before.
#
# Своё окружение, выданное за свойство стенда, — ровно та болезнь,
# против которой стенд и построен. Поэтому: объявить и проверить.
need() {
    command -v "$1" >/dev/null 2>&1 && return 0
    echo "НЕТ ЗАВИСИМОСТИ: $1 — $2" >&2
    return 1
}

missing=0
need docker "клиенты и выпуск сертификатов идут в контейнерах" || missing=1
need go "сервер, клиент на Go и посредник собираются из исходников" || missing=1

# Толкователь Python зовётся по-разному. Раньше в прогоне стояло жёсткое
# «python», и там, где есть только python3, всё падало на первом же шаге.
PY=""
for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1; then PY="$candidate"; break; fi
done
if [ -z "$PY" ]; then
    echo "НЕТ ЗАВИСИМОСТИ: python3 — на нём написаны прогоны и разборы" >&2
    missing=1
elif ! "$PY" -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 8) else 1)'; then
    echo "СТАРЫЙ PYTHON: нужен 3.8 или новее, найден $("$PY" -V 2>&1)" >&2
    missing=1
fi

if [ "$missing" -ne 0 ]; then
    echo >&2
    echo "Прогон не начат: не хватает хостовых зависимостей." >&2
    echo "Нужны docker, go и python 3.8+; остальное приезжает образами." >&2
    exit 2
fi
echo "зависимости на месте: docker, go, $PY ($("$PY" -V 2>&1))"

step() {
    local title="$1"; shift
    printf '\n=== %s\n' "$title"
    "$@"
    local rc=$?
    if [ $rc -ne 0 ]; then
        printf '\nПРОГОН ОСТАНОВЛЕН на шаге «%s» (код %d).\n' "$title" $rc
        printf 'Маркер завершения не поставлен — фикстуры неполны.\n'
        exit $rc
    fi
}

step "образ с OpenSSL"        docker build -q -t tls-stand-openssl:1 image/
step "выпуск сертификатов"    bash pki/issue.sh
step "сборка сервера"         sh -c 'cd server && go build -o server .'
step "сборка клиента на Go"   sh -c 'cd clients/go && go build -o probe .'
step "сборка посредника"    sh -c 'cd wire && go build -o recorder .'

step "ось 1: цепочка доверия"      "$PY" scripts/run-chain.py
step "ось 1: отзыв"                "$PY" scripts/run-revocation.py
step "ось 2: взаимное рукопожатие" "$PY" scripts/run-mutual.py
step "ось 3: что видно на проводе" "$PY" scripts/run-wire.py
step "ось 3: шифрованное приветствие" "$PY" scripts/run-ech.py
step "ось 3: метка о понижении и видимость сертификата" "$PY" scripts/run-downgrade.py
step "ось 4: число сообщений"      "$PY" scripts/run-handshake.py

step "разбор: цепочка"       "$PY" scripts/analyze-chain.py
step "разбор: рукопожатие"   "$PY" scripts/analyze-mutual.py
step "разбор: провод"        "$PY" scripts/analyze-wire.py
step "разбор: сообщения"     "$PY" scripts/analyze-handshake.py

# Сводка снимается ДО проверок: они же её и сверяют со сводками других
# платформ.
step "сводка прогона"        "$PY" scripts/run-manifest.py

step "весь набор проверок"   "$PY" -m unittest discover -s scripts -p 'test_*.py' -t .

date -u +"%Y-%m-%dT%H:%M:%SZ" > "$MARKER"
printf '\nПРОГОН ЗАВЕРШЁН ПОЛНОСТЬЮ. Маркер: %s\n' "$MARKER"
