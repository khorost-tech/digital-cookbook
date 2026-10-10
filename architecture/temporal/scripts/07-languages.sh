#!/usr/bin/env bash
#
# 07-languages.sh — артефакт к статье «Temporal по языкам».
#
# Один и тот же сценарий (activity → таймер → сигнал с таймаутом →
# компенсация) на пяти SDK, против ОДНОГО сервера. У каждого языка своя
# task queue: одна очередь — один язык воркера.
#
# Версия каждого SDK снимается в момент прогона и печатается: статья
# заявляет только то, что прогнано на этих версиях.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

LANGS=(java ts python dotnet)

cleanup() {
    docker rm -f lang-go lang-go-run >/dev/null 2>&1 || true
    for l in "${LANGS[@]}"; do docker rm -f "lang-${l}" >/dev/null 2>&1 || true; done
}
trap cleanup EXIT
cleanup

section "Сборка образов воркеров"
for l in "${LANGS[@]}"; do
    printf '  %-8s ' "${l}"
    docker build -q -t "temporal-cookbook/lang-${l}:local" "${STAND_DIR}/clients/${l}" >/dev/null
    echo "готов"
done

section "Подъём пяти воркеров против одного сервера"
go_start_in_net lang-go 07-languages worker -queue lang-go-tq >/dev/null
wait_for_log lang-go "lang=go" 300
for l in "${LANGS[@]}"; do
    docker run -d --name "lang-${l}" --network "${NET}" \
        -e TEMPORAL_ADDRESS="${TEMPORAL_ADDR}" \
        -e TASK_QUEUE="lang-${l}-tq" \
        "temporal-cookbook/lang-${l}:local" >/dev/null
    wait_for_log "lang-${l}" "lang=${l}" 300
done
echo "  все пять воркеров слушают свои очереди"

section "Один сценарий, пять реализаций"
# Go запускается тем же способом, что и остальные, — контейнером на сети
# стенда: с хоста бинарь до сервера не достучится.
go_run_in_net lang-go-run 07-languages run -queue lang-go-tq -workflow lang-go -approve \
    | grep '^ЯЗЫК'
for l in "${LANGS[@]}"; do
    docker run --rm --network "${NET}" \
        -e TEMPORAL_ADDRESS="${TEMPORAL_ADDR}" \
        -e TASK_QUEUE="lang-${l}-tq" \
        "temporal-cookbook/lang-${l}:local" run --approve 2>&1 | grep '^ЯЗЫК'
done

section "Версии SDK, на которых сделан прогон"
# grep -m1 закрывает канал после первого совпадения, docker logs получает
# SIGPIPE, и при pipefail падает весь скрипт. Поэтому сначала полный вывод
# grep, и только потом head.
sdk_line() { docker logs "$1" 2>&1 | grep 'sdk=' | head -1 | sed 's/^/  /'; }
sdk_line lang-go
for l in "${LANGS[@]}"; do sdk_line "lang-${l}"; done

section "Как достигается детерминизм в каждом SDK"
echo "  go      — собственная корутинная модель: workflow.Sleep, Selector,"
echo "            каналы воркфлоу; обычные горутины и time использовать нельзя"
echo "  java    — планировщик поверх потоков: Workflow.sleep и Workflow.await"
echo "            не блокируют настоящий поток, их проигрывает рантайм"
echo "  ts      — изолированное окружение: таймеры, промисы, Date.now и"
echo "            Math.random ПОДМЕНЕНЫ, а не запрещены"
echo "  python  — собственный event loop: asyncio.sleep перехвачен и"
echo "            превращён в таймер Temporal"
echo "  dotnet  — свои конструкции: Workflow.DelayAsync вместо Task.Delay,"
echo "            Workflow.UtcNow вместо DateTime.UtcNow"
