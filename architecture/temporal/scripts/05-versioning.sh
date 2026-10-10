#!/usr/bin/env bash
#
# 05-versioning.sh — артефакт к статье «Версионирование workflow».
#
# Контрпример 1: правка кода при живом экземпляре → non-determinism error.
# Починка:       тот же шаг под GetVersion → старый экземпляр доходит.
# Контрпример 2: маркер патча снят, пока живой экземпляр со старой
#                историей ещё существует → та же ошибка возвращается,
#                но теперь код выглядит безупречно.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

RUN_ID="$(date +%s)"

cleanup() { docker rm -f v5-w v5-run >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

start_worker() {                  # start_worker <версия>
    docker rm -f v5-w >/dev/null 2>&1 || true
    go_start_in_net v5-w 05-versioning worker -version "$1" >/dev/null
    wait_for_log v5-w "версия=$1" 300
}

status_of() { go_run_in_net v5-run 05-versioning status -workflow "$1" | grep '^СТАТУС'; }

# Число упоминаний расхождения в логе воркера. Ошибка детерминизма
# приходит именно в лог воркера: сам воркфлоу при этом жив.
mismatches() {
    docker logs v5-w 2>&1 | grep -ci 'nondeterministic\|non-determinism\|mismatch\|history event is' || true
}

run_case() {                      # run_case <имя> <версия-старта> <версия...> — цепочка подмен
    # Отдельным объявлением: аргументы одного `local` раскрываются ДО его
    # выполнения, поэтому ссылка на name внутри той же строки падает с
    # «unbound variable».
    local name="$1"; shift
    local wf="ver-${name}-${RUN_ID}"
    local first="$1"; shift

    start_worker "${first}"
    go_run_in_net v5-run 05-versioning start -workflow "${wf}"
    # Даём воркфлоу дойти до паузы — окна, в котором подменяется воркер.
    sleep 8

    local v
    for v in "$@"; do
        docker kill v5-w >/dev/null 2>&1 || true
        echo "  подменяем воркер на ${v}"
        start_worker "${v}"
        sleep 12
    done

    # Пауза 90s должна истечь, и воркфлоу продолжится уже новым кодом.
    sleep 95
    status_of "${wf}" | sed 's/^/    /'
    echo "    упоминаний расхождения в логе воркера: $(mismatches)"
    docker logs v5-w 2>&1 | grep -i 'nondeterministic\|mismatch\|history event is' | head -2 | sed 's/^/      /' || true
    docker kill v5-w >/dev/null 2>&1 || true
    docker rm -f v5-w >/dev/null 2>&1 || true
}

section "Контрпример 1: правка кода при живом экземпляре (v1 → v2, без патча)"
run_case naive v1 v2

section "Починка: тот же шаг под GetVersion (v1 → v3)"
run_case patched v1 v3

section "Контрпример 2: маркер патча снят слишком рано (v1 → v3 → v4)"
echo "  Цепочка из ТРЁХ версий, а не из двух, и это принципиально."
echo "  Экземпляр, родившийся уже на v3, записывает версию патча и идёт"
echo "  новой веткой — для него удаление маркера ничего не ломает."
echo "  Ломается тот, кто родился ДО патча: его история говорит"
echo "  «старая ветка», а код v4 знает только новую."
run_case removed v1 v3 v4

section "Что это значит"
echo "  non-determinism error НЕ убивает воркфлоу: статус остаётся Running,"
echo "  а workflow task падает по кругу с растущим номером попытки."
echo "  Процесс ждёт, пока выкатят код, совместимый с его историей."
