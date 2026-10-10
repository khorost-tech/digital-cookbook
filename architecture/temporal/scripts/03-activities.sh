#!/usr/bin/env bash
#
# 03-activities.sh — артефакт к статье «Activities вглубь».
#
# Сюжет 1: ретрай неидемпотентной activity задваивает эффект.
# Сюжет 2: ключ идемпотентности убирает задвоение при тех же ретраях.
# Сюжет 3: heartbeat — убийство воркера посреди долгой задачи, возобновление.
# Сюжет 4 (замер): local activity против обычной — время и объём истории.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CALLS="${CALLS:-100}"

cleanup() { docker rm -f a3-worker a3-worker2 a3-run a3-imp >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

pg_count() { pg_query "SELECT count(*) FROM side_effects WHERE order_id='$1'"; }

go_start_in_net a3-worker 03-activities worker -fail-times 2 >/dev/null
wait_for_log a3-worker "fail-times=2" 300

section "Сюжет 1: неидемпотентная activity, две первые попытки падают ПОСЛЕ записи"
go_run_in_net a3-run 03-activities charge -order order-unsafe -workflow ch-unsafe
unsafe_rows="$(pg_count order-unsafe)"
echo "  строк в side_effects для order-unsafe: ${unsafe_rows}"
echo "  (три попытки — три строки: эффект задвоен ретраями)"

section "Сюжет 2: та же нагрузка с ключом идемпотентности"
go_run_in_net a3-run 03-activities charge -safe -order order-safe -workflow ch-safe
safe_rows="$(pg_count order-safe)"
echo "  строк в side_effects для order-safe: ${safe_rows}   (ожидается 1)"

section "Сюжет 3: heartbeat и возобновление долгой activity"
go_start_in_net a3-imp 03-activities import -job imp-1 -total 60 -workflow imp-demo >/dev/null
wait_for_log a3-worker "LongImport(imp-1) начинаем с нуля" 300
sleep 6
mid="$(pg_query "SELECT done FROM import_progress WHERE job_id='imp-1'")"
echo "  прогресс до убийства воркера: ${mid} из 60"
docker kill a3-worker >/dev/null
docker rm -f a3-worker >/dev/null 2>&1 || true
# Новый воркер без инъекции ошибок: интересует возобновление, а не ретраи.
go_start_in_net a3-worker2 03-activities worker -fail-times 0 >/dev/null
wait_for_log a3-worker2 "ПРОДОЛЖАЕМ с" 300
resumed="$(docker logs a3-worker2 2>&1 | grep -o 'ПРОДОЛЖАЕМ с [0-9]*' | head -1)"
echo "  новый воркер: ${resumed}   (не с нуля — это и есть смысл heartbeat)"
wait_for_log a3-imp "ИМПОРТ" 600
docker logs a3-imp 2>&1 | grep '^ИМПОРТ' | sed 's/^/  /'

section "Сюжет 4 (замер): local activity против обычной"
# Порядок чередуется по раундам: фиксированный порядок не отличает
# эффект способа вызова от прогрева.
for r in 1 2 3; do
    echo "--- раунд ${r}"
    if (( r % 2 == 1 )); then
        go_run_in_net a3-run 03-activities ping -calls "${CALLS}" -workflow "ping-reg-${r}" | grep '^ЗАМЕР'
        go_run_in_net a3-run 03-activities ping -calls "${CALLS}" -local -workflow "ping-loc-${r}" | grep '^ЗАМЕР'
    else
        go_run_in_net a3-run 03-activities ping -calls "${CALLS}" -local -workflow "ping-loc-${r}" | grep '^ЗАМЕР'
        go_run_in_net a3-run 03-activities ping -calls "${CALLS}" -workflow "ping-reg-${r}" | grep '^ЗАМЕР'
    fi
done

section "Сводка профиля 03"
printf '  %-40s %s\n' "неидемпотентная: строк эффекта" "${unsafe_rows}"
printf '  %-40s %s\n' "идемпотентная: строк эффекта"   "${safe_rows}"
printf '  %-40s %s из 60\n' "импорт: прогресс до убийства"   "${mid}"
