#!/usr/bin/env bash
#
# 01-internals.sh — артефакт к статье «Архитектура Temporal вглубь».
#
# Сюжет 1 (замер): работа sticky-кэша. Один и тот же воркфлоу гоняется
# воркером с кэшем и воркером с нулевым кэшем; различается РОВНО эта
# настройка.
#
# ИНСТРУМЕНТ — МЕТРИКИ SDK, а не end-to-end latency. Первая попытка мерила
# полное время воркфлоу и эффекта не показала: цена replay при истории в
# ~400 событий составляет миллисекунды и тонет в сетевых раундах, которые
# на этом хосте стоят сотни миллисекунд. Счётчики sticky-кэша и latency
# проигрывания workflow task существуют только на стороне SDK — сервер про
# кэш воркера не знает.
#
# Сюжет 2 (наблюдение): убиваем ТОЛЬКО роль history ПОД НАГРУЗКОЙ.
# Сюжет 3 (наблюдение): где что физически лежит — Postgres и Elasticsearch.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROUNDS="${ROUNDS:-3}"
STEPS="${STEPS:-40}"
COUNT="${COUNT:-5}"

cleanup() { docker rm -f i1-worker i1-load >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

# Значение метрики SDK или явное «НЕТ РЯДА»: отсутствие ряда и ноль —
# разные факты, и подменять одно другим нельзя.
sdk_metric() {
    local v; v="$(prom_query "$1")"
    if [[ -z "${v}" ]]; then echo "НЕТ_РЯДА"; else echo "${v}"; fi
}

measure() {                       # measure <метка> <cache> <очередь>
    local label="$1" cache="$2" queue="$3"
    # В метках Prometheus имя очереди санитизировано: дефисы становятся
    # подчёркиваниями. Фильтровать надо по санитизированному имени, иначе
    # селектор молча не найдёт ни одного ряда.
    local q="${queue//-/_}"

    docker rm -f i1-worker >/dev/null 2>&1 || true
    go_start_in_net i1-worker 01-internals worker -queue "${queue}" -cache "${cache}" >/dev/null
    wait_for_log i1-worker "MaxCachedWorkflows=${cache}" 300
    # Даём Prometheus увидеть цель до начала нагрузки.
    sleep 10

    local line
    line="$(go_run_in_net i1-load 01-internals load \
        -queue "${queue}" -cache "${cache}" -steps "${STEPS}" -count "${COUNT}" -label "${label}" \
        | grep '^ЗАМЕР')"

    # Даём Prometheus доскрести последние значения счётчиков.
    sleep 12

    # Имена метрик сняты с живого эндпоинта воркера: счётчики идут с
    # суффиксом _total, а replay-latency — summary с _sum/_count, а НЕ
    # гистограмма с _bucket. Первая версия этого замера спрашивала
    # несуществующие ряды и получала пустоту по всем конфигурациям.
    local hit tasks replay_sum replay_cnt replay_us
    hit="$(sdk_metric "sum(temporal_sticky_cache_hit_total{task_queue=\"${q}\"})")"
    tasks="$(sdk_metric "sum(temporal_workflow_task_queue_poll_succeed_total{task_queue=\"${q}\"})")"
    replay_sum="$(sdk_metric "sum(temporal_workflow_task_replay_latency_seconds_sum{task_queue=\"${q}\"})")"
    replay_cnt="$(sdk_metric "sum(temporal_workflow_task_replay_latency_seconds_count{task_queue=\"${q}\"})")"

    # Среднее время проигрывания одной workflow task, микросекунды.
    if [[ "${replay_sum}" != "НЕТ_РЯДА" && "${replay_cnt}" != "НЕТ_РЯДА" ]]; then
        replay_us="$(awk -v s="${replay_sum}" -v c="${replay_cnt}" \
            'BEGIN { if (c > 0) printf "%.1f", s / c * 1000000; else print "НЕТ_ЗАДАЧ" }')"
    else
        replay_us="НЕТ_РЯДА"
    fi

    echo "${line} sticky_hit=${hit} wf_tasks=${tasks} replay_mean_us=${replay_us} replay_n=${replay_cnt} source=sdk"
    docker rm -f i1-worker >/dev/null 2>&1 || true
}

section "Сюжет 1: sticky-кэш вкл/выкл, ${ROUNDS} раунда с чередованием"
echo "  инструмент: метрики SDK (source=sdk); нагрузка ${COUNT} воркфлоу по ${STEPS} шагов"
for r in $(seq 1 "${ROUNDS}"); do
    echo "--- раунд ${r}"
    if (( r % 2 == 1 )); then
        measure "sticky-r${r}"   1000 "internals-sticky-r${r}"
        measure "nosticky-r${r}" 0    "internals-nosticky-r${r}"
    else
        measure "nosticky-r${r}" 0    "internals-nosticky-r${r}"
        measure "sticky-r${r}"   1000 "internals-sticky-r${r}"
    fi
done

section "Сюжет 2: убиваем ТОЛЬКО роль history ПОД НАГРУЗКОЙ"
# Прежняя версия сюжета поднимала воркер на пустой очереди и гасила
# history в тишине — гасить было нечего, и лог ожидаемо не показывал
# ничего. Проверка отсутствия эффекта на пустых данных проходит всегда;
# теперь под нагрузкой.
go_start_in_net i1-worker 01-internals worker -queue internals-kill -cache 1000 >/dev/null
wait_for_log i1-worker "MaxCachedWorkflows=1000" 300
go_start_in_net i1-load 01-internals load \
    -queue internals-kill -cache 1000 -steps 200 -count 3 -label kill >/dev/null
wait_for_log i1-worker "ACTIVITY CheckAvailability" 300

# Прогресс мерим СЧЁТЧИКОМ выполненных activity, а не чтением хвоста лога.
# Хвост лога показывает последние строки безотносительно времени: остановку
# продвижения по нему видно только сверкой таймстампов, и «ничего не
# изменилось» легко принять за «ничего не сломалось».
acts_done() { docker logs i1-worker 2>&1 | grep -c 'ACTIVITY CheckAvailability' || true; }

before="$(acts_done)"
echo "  выполнено activity до гашения:            ${before}"
docker compose -f "${COMPOSE_FILE}" stop temporal-history >/dev/null 2>&1
at_stop="$(acts_done)"
sleep 30
during="$(acts_done)"
echo "  выполнено сразу после гашения history:    ${at_stop}"
echo "  выполнено ещё через 30 секунд без history: ${during}"
echo "  прирост за 30 секунд без роли history:    $(( during - at_stop ))   (ожидается 0 или почти 0)"
echo "  последние строки лога воркера в этот момент:"
docker logs --tail 4 i1-worker 2>&1 | sed 's/^/    /'

docker compose -f "${COMPOSE_FILE}" start temporal-history >/dev/null 2>&1
sleep 45
after="$(acts_done)"
echo "  выполнено через 45 секунд после возврата:  ${after}"
echo "  прирост после возврата роли:              $(( after - during ))   (ожидается заметно больше нуля)"
echo "  воркфлоу НЕ упали и НЕ начались заново — они просто не продвигались,"
echo "  пока роль, владеющая их шардами, отсутствовала"
docker rm -f i1-worker i1-load >/dev/null 2>&1 || true

section "Сюжет 3: что физически лежит в persistence"
echo "  таблицы Temporal в Postgres (первые 15):"
pg_query "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1 LIMIT 15" | sed 's/^/    /'
echo "  размер таблицы истории:"
pg_query "SELECT pg_size_pretty(pg_total_relation_size('history_node'))" | sed 's/^/    /'
echo "  число записей истории:"
pg_query "SELECT count(*) FROM history_node" | sed 's/^/    /'
echo "  число шардов history:"
pg_query "SELECT count(*) FROM shards" | sed 's/^/    /'
echo "  visibility ЖИВЁТ ОТДЕЛЬНО, в Elasticsearch:"
es_get "/temporal_visibility_v1_dev/_count" | sed 's/^/    /'
echo
echo "  а таблицы visibility в Postgres нет вовсе:"
pg_query "SELECT count(*) FROM executions_visibility" 2>/dev/null | sed 's/^/    /' || \
    echo "    executions_visibility отсутствует — visibility целиком в ES"
