#!/usr/bin/env bash
#
# run-all.sh — сквозной прогон стенда НАЧИСТО.
#
# Гасит стенд вместе с томом Postgres, поднимает заново, сверяет версии и
# прогоняет все восемь профилей подряд, складывая сырые логи в
# scripts/.runs/. Именно из этих логов собирается FIXTURES.md.
#
# Зачем отдельно от прогона профилей поштучно: числа, снятые в разное
# время против по-разному наполненного стенда, сравнивать между собой
# нельзя. Сквозной прогон даёт один согласованный срез.
#
# Идёт примерно час: профиль 04 шлёт 240 сигналов, каждый — отдельный
# контейнер, и это самая медленная часть.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
STAND_DIR="$(pwd)"
RUNS="${STAND_DIR}/scripts/.runs"
mkdir -p "${RUNS}"

SUMMARY="${RUNS}/run-all-summary.txt"
: > "${SUMMARY}"

note() { echo "$*" | tee -a "${SUMMARY}"; }

note "=== сквозной прогон, старт: $(date -Is)"

note "--- гашу стенд вместе с томом"
# Код возврата проверяем: если том не удалился, следующий прогон пойдёт
# НЕ начисто, а числа из смешанного состояния уедут в FIXTURES как
# результат чистого прогона. Молчаливое подавление здесь опаснее всего.
if ! bash scripts/down.sh > "${RUNS}/down.log" 2>&1; then
    note "ОЧИСТКА НЕ УДАЛАСЬ — прогон остановлен, см. ${RUNS}/down.log"
    tail -5 "${RUNS}/down.log" | tee -a "${SUMMARY}"
    exit 1
fi

note "--- поднимаю заново"
if ! bash scripts/up.sh > "${RUNS}/up.log" 2>&1; then
    note "ПОДЪЁМ НЕ УДАЛСЯ — см. ${RUNS}/up.log"
    exit 1
fi

note "--- сверяю версии"
if bash scripts/probe.sh > "${RUNS}/probe.log" 2>&1; then
    note "    probe: всё сходится"
else
    note "    probe: ЕСТЬ РАСХОЖДЕНИЯ — числа этого прогона в фикстуры не берём"
    exit 1
fi

PROFILES=(00-paradigm 01-internals 02-determinism 03-activities
          04-messaging 05-versioning 06-operations 07-languages)

failed=0
for p in "${PROFILES[@]}"; do
    started="$(date +%s)"
    note "--- профиль ${p}: старт $(date -Is)"
    if bash "scripts/${p}.sh" > "${RUNS}/${p}.log" 2>&1; then
        note "    ${p}: ок, $(( $(date +%s) - started ))s"
    else
        note "    ${p}: ПРОВАЛ, $(( $(date +%s) - started ))s — см. ${RUNS}/${p}.log"
        failed=1
    fi
done

note "=== сквозной прогон, финиш: $(date -Is)"
if (( failed )); then
    note "ЕСТЬ ПРОВАЛИВШИЕСЯ ПРОФИЛИ — фикстуры по этому прогону не обновляем"
    exit 1
fi
note "все восемь профилей завершились без ошибок"

# Код возврата профиля говорит лишь о том, что команды отработали.
# Утверждения серии проверяет отдельный скрипт по логам: без него
# регрессия, при которой скрипт по-прежнему доходит до конца, осталась
# бы зелёной.
note "--- проверяю смысл прогона"
if bash scripts/assert-run.sh 2>&1 | tee -a "${SUMMARY}"; then
    note "прогон пригоден для обновления FIXTURES"
else
    note "ГИПОТЕЗЫ НЕ ПОДТВЕРДИЛИСЬ — фикстуры по этому прогону не обновляем"
    exit 1
fi
