#!/usr/bin/env bash
#
# 04-messaging.sh — артефакт к статье «Signals, Queries, Updates и child
# workflows».
#
# Сюжет 1: Signal доставляется в бегущий воркфлоу и попадает в историю.
# Сюжет 2: Query читает состояние и историю НЕ растит.
# Сюжет 3: Update валидируется синхронно — отклонённый не меняет ничего.
# Сюжет 4: родитель с дочерними воркфлоу.
# Сюжет 5 (замер): история с Continue-As-New и без него при одинаковой
#          нагрузке. Различается РОВНО наличие Continue-As-New.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SIGNALS="${SIGNALS:-120}"

# Уникальный суффикс прогона. Без него WorkflowID постоянны, и повторный
# запуск скрипта попадает в ЖИВОЕ исполнение, оставшееся с прошлого раза:
# сигналы досчитываются к чужому счётчику, воркфлоу завершается раньше
# времени, а остаток сигналов уходит в закрытое исполнение. Именно на
# этом упала первая версия профиля.
RUN_ID="$(date +%s)"

cleanup() { docker rm -f m4-worker m4-run >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

events_of() {   # events_of <workflowID> [-can]
    go_run_in_net m4-run 04-messaging history -workflow "$1" "${@:2}" \
        | sed -n 's/.*events=\([0-9]*\).*/\1/p'
}

go_start_in_net m4-worker 04-messaging worker >/dev/null
wait_for_log m4-worker "очередь=messaging-tq" 300

section "Сюжеты 1-3: Signal, Query, Update"
go_run_in_net m4-run 04-messaging cart -workflow cart-basic-${RUN_ID} -max-cycles 5 -cap 100
for _ in 1 2 3; do
    go_run_in_net m4-run 04-messaging signal -workflow cart-basic-${RUN_ID} -amount 10
done
sleep 3
go_run_in_net m4-run 04-messaging query -workflow cart-basic-${RUN_ID} | sed 's/^/  /'

echo "  проверяем, что Query историю НЕ растит:"
before="$(events_of cart-basic-${RUN_ID})"
for _ in 1 2 3 4 5; do
    go_run_in_net m4-run 04-messaging query -workflow cart-basic-${RUN_ID} >/dev/null
done
after="$(events_of cart-basic-${RUN_ID})"
echo "    событий до пяти Query: ${before}, после: ${after}   (ожидается совпадение)"

echo "  Update с недопустимым значением — отклоняется валидатором синхронно:"
go_run_in_net m4-run 04-messaging update -workflow cart-basic-${RUN_ID} -new-cap -5 | sed 's/^/    /'
echo "  Update с допустимым значением:"
go_run_in_net m4-run 04-messaging update -workflow cart-basic-${RUN_ID} -new-cap 500 | sed 's/^/    /'

section "Сюжет 4: родитель и дочерние воркфлоу"
go_run_in_net m4-run 04-messaging parent -workflow parent-demo-${RUN_ID} -children 5 | sed 's/^/  /'

section "Сюжет 5 (замер): Continue-As-New против его отсутствия"
echo "  на обе конфигурации — по ${SIGNALS} сигналов"
echo "  без CAN: одна итерация на все ${SIGNALS} — вся история в одном запуске"
echo "  с CAN:   итерация по 20, то есть $(( SIGNALS / 20 )) поколений"
#
# Лимит итерации у конфигураций РАЗНЫЙ намеренно. Одинаковый лимит здесь
# невозможен: без Continue-As-New воркфлоу по достижении лимита просто
# завершается, и остаток сигналов уходит в уже закрытое исполнение
# («workflow execution already completed» — на этом первая версия замера
# и упала). Общим держится то, что сравнивается: одинаковое число
# доставленных сигналов и один и тот же код воркфлоу.
for mode in without with; do
    wf="cart-can-${mode}-${RUN_ID}"
    if [[ "${mode}" == "with" ]]; then
        go_run_in_net m4-run 04-messaging cart -workflow "${wf}" -max-cycles 20 -cap 1000000 -can
    else
        go_run_in_net m4-run 04-messaging cart -workflow "${wf}" -max-cycles "${SIGNALS}" -cap 1000000
    fi
    for _ in $(seq 1 "${SIGNALS}"); do
        go_run_in_net m4-run 04-messaging signal -workflow "${wf}" -amount 1 >/dev/null
    done
    sleep 8
    if [[ "${mode}" == "with" ]]; then
        go_run_in_net m4-run 04-messaging history -workflow "${wf}" -can | grep '^ЗАМЕР'
    else
        go_run_in_net m4-run 04-messaging history -workflow "${wf}" | grep '^ЗАМЕР'
    fi
done
echo "  при Continue-As-New история ТЕКУЩЕГО запуска остаётся короткой,"
echo "  хотя логически процесс обработал те же ${SIGNALS} сигналов"
