#!/usr/bin/env bash
#
# 00-paradigm.sh — артефакт к статье «Durable execution: почему код должен
# переживать падения».
#
# Четыре сюжета, все с одинаковым ударом — убийством процесса в середине:
#   A. наивный воркер          → прогресс потерян, бронь осталась висеть
#   B. самодельный автомат     → прогресс пережил, но механику писали руками
#   C. Temporal, убит ВОРКЕР   → прогресс пережил, механики в коде нет
#   D. Temporal, убит СЕРВЕР   → исполнение продолжается после подъёма
#
# Сюжет D — то, чего прежняя версия стенда на `server start-dev` показать
# не могла: там история жила в памяти сервера.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

cleanup() {
    docker rm -f p0-naive p0-sm p0-worker p0-worker2 p0-start \
                 p0-signal p0-signal2 >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

section "A. Наивный воркер: состояние в памяти процесса"
go_start_in_net p0-naive 00-paradigm naive -pause 30s >/dev/null
wait_for_log p0-naive "бронь получена" 180
sleep 3
docker kill p0-naive >/dev/null
echo "  воркер убит после Reserve, до Allocate"
naive_alloc="$(docker logs p0-naive 2>&1 | grep -c 'ACTIVITY Allocate' || true)"
echo "  выполнено Allocate: ${naive_alloc}   (ожидается 0 — прогресс потерян)"
echo "  бронь во внешней системе осталась, знания о ней нет ни у кого"
docker rm -f p0-naive >/dev/null 2>&1 || true

section "B. Самодельный автомат на таблице Postgres"
go_start_in_net p0-sm 00-paradigm statemachine -workflow sm-demo -pause 30s >/dev/null
wait_for_log p0-sm "бронь" 180
docker kill p0-sm >/dev/null
sm_step="$(pg_query "SELECT step FROM provisioning_state WHERE id='sm-demo'")"
echo "  процесс убит; шаг в таблице: ${sm_step}   (ожидается reserved)"
docker rm -f p0-sm >/dev/null 2>&1 || true
go_run_in_net p0-sm 00-paradigm statemachine -workflow sm-demo -pause 30s
sm_step2="$(pg_query "SELECT step FROM provisioning_state WHERE id='sm-demo'")"
echo "  после перезапуска шаг: ${sm_step2}   (ожидается allocated)"
echo "  прогресс пережил — ценой таблицы, цикла опроса и ручного таймера"

section "C. Temporal: убийство ВОРКЕРА"
go_start_in_net p0-worker 00-paradigm temporal-worker >/dev/null
go_start_in_net p0-start 00-paradigm temporal-start -workflow paradigm-demo >/dev/null
wait_for_log p0-worker "ресурс зарезервирован" 180
run_id="$(docker logs p0-start 2>&1 | sed -n 's/.*RunID=\([0-9a-f-]*\).*/\1/p' | head -1)"
docker kill p0-worker >/dev/null
echo "  воркер #1 убит; RunID=${run_id}"
go_start_in_net p0-worker2 00-paradigm temporal-worker >/dev/null
wait_for_log p0-worker2 "ждём сигнал подтверждения" 180
w2_check="$(docker logs p0-worker2 2>&1 | grep -c 'ACTIVITY CheckAvailability' || true)"
w2_reserve="$(docker logs p0-worker2 2>&1 | grep -c 'ACTIVITY Reserve' || true)"
echo "  в логе воркера #2: CheckAvailability=${w2_check}, Reserve=${w2_reserve}   (ожидается 0 и 0)"
go_run_in_net p0-signal 00-paradigm temporal-signal -workflow paradigm-demo -approve
wait_for_log p0-start "РЕЗУЛЬТАТ" 180
docker logs p0-start 2>&1 | grep "РЕЗУЛЬТАТ" | sed 's/^/  /'

section "D. Temporal: убийство ВСЕГО СЕРВЕРА"
docker rm -f p0-start >/dev/null 2>&1 || true
go_start_in_net p0-start 00-paradigm temporal-start -workflow paradigm-server-demo >/dev/null
wait_for_log p0-start "воркфлоу запущен" 180
sleep 8
echo "  гасим все четыре роли Temporal"
docker compose -f "${COMPOSE_FILE}" stop temporal-frontend temporal-history temporal-matching temporal-worker
sleep 10
echo "  поднимаем обратно"
docker compose -f "${COMPOSE_FILE}" start temporal-frontend temporal-history temporal-matching temporal-worker
sleep 20
go_run_in_net p0-signal2 00-paradigm temporal-signal -workflow paradigm-server-demo -approve
wait_for_log p0-start "РЕЗУЛЬТАТ" 240
docker logs p0-start 2>&1 | grep "РЕЗУЛЬТАТ" | sed 's/^/  /'
echo "  исполнение продолжилось после подъёма: история лежит в Postgres,"
echo "  а не в памяти сервера"

section "Сводка профиля 00"
printf '  %-38s %s\n' "наивный: Allocate после убийства" "${naive_alloc}"
printf '  %-38s %s -> %s\n' "автомат: шаг до/после" "${sm_step}" "${sm_step2}"
printf '  %-38s %s / %s\n' "Temporal: Check/Reserve у воркера #2" "${w2_check}" "${w2_reserve}"
