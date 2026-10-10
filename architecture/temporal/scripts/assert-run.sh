#!/usr/bin/env bash
#
# assert-run.sh — проверка СМЫСЛА прогона, а не факта его завершения.
#
# Профильные скрипты печатают «ожидается …», но сами ничего не сравнивают.
# Пока проверялся только код возврата, зелёный сквозной прогон означал
# «команды отработали», а не «гипотезы подтвердились»: любая регрессия,
# при которой скрипт по-прежнему доходит до конца, оставалась незамеченной.
#
# Здесь по логам из scripts/.runs проверяется каждое утверждение, на
# которое опираются статьи серии.
#
# Пороги двух видов, и это важно при чтении вывода:
#
#   СТРУКТУРНЫЕ величины сверяются ТОЧНО. Число строк эффекта, событий
#   истории на вызов, статусы версионирования, равенство истории до и
#   после Query — они определяются механикой, а не машиной, и обязаны
#   совпасть до знака. Их расхождение означает, что поведение Temporal
#   изменилось и статью надо переписывать.
#
#   ЗАВИСЯЩИЕ ОТ ХОСТА величины (времена и отношения времён) сверяются с
#   запасом, но не «лишь бы что-то»: порог выбран так, чтобы подтверждать
#   именно ОПУБЛИКОВАННЫЙ вывод. Публикуется 436-648x у sticky — гейт
#   требует не менее 300x; публикуется 9,8x у ёмкости воркера — гейт
#   требует не менее 7x. Более мягкий порог пропускал бы прогон, из
#   которого опубликованное утверждение уже не следует.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
RUNS="scripts/.runs"

fails=0
ok()   { printf '  ok    %s\n' "$1"; }
bad()  { printf '  FAIL  %s — %s\n' "$1" "$2"; fails=$((fails + 1)); }

# have <файл> — лог профиля существует и непуст
have() { [[ -s "${RUNS}/$1.log" ]]; }

# grep_has <файл> <что> <подстрока>
grep_has() {
    if grep -qF -- "$3" "${RUNS}/$1.log"; then ok "$2"; else bad "$2" "нет строки: $3"; fi
}

# num <файл> <regex с одной группой> — первое числовое совпадение
num() { sed -n "s/.*$2.*/\1/p" "${RUNS}/$1.log" | head -1; }

# all_nums <файл> <regex с одной группой>
all_nums() { sed -n "s/.*$2.*/\1/p" "${RUNS}/$1.log"; }

# cmp_num <что> <факт> <оператор> <эталон>
cmp_num() {
    local what="$1" got="$2" op="$3" want="$4"
    if [[ -z "${got}" ]]; then bad "${what}" "значение не найдено в логе"; return; fi
    if awk -v a="${got}" -v b="${want}" "BEGIN{exit !(a ${op} b)}"; then
        ok "${what} (${got} ${op} ${want})"
    else
        bad "${what}" "${got} не ${op} ${want}"
    fi
}

echo "=== проверка смысла прогона"

# ── 00-paradigm ───────────────────────────────────────────────────────
if have 00-paradigm; then
    echo "-- 00-paradigm"
    grep_has 00-paradigm "наивный воркер потерял прогресс" "выполнено Allocate: 0"
    grep_has 00-paradigm "самодельный автомат дошёл до allocated" "reserved -> allocated"
    grep_has 00-paradigm "второй воркер не переисполнил activity" "0 / 0"
    n=$(grep -c 'РЕЗУЛЬТАТ: outcome=allocated' "${RUNS}/00-paradigm.log")
    cmp_num "исполнение пережило и воркер, и сервер (два РЕЗУЛЬТАТА)" "${n}" ">=" 2
else bad "00-paradigm" "нет лога"; fi

# ── 01-internals ──────────────────────────────────────────────────────
if have 01-internals; then
    echo "-- 01-internals"
    hits=$(grep -c 'sticky_hit=400' "${RUNS}/01-internals.log")
    cmp_num "три раунда со включённым кэшем дали попадания" "${hits}" "==" 3
    miss=$(grep -c 'sticky_hit=НЕТ_РЯДА' "${RUNS}/01-internals.log")
    cmp_num "три раунда без кэша не дали ни одного попадания" "${miss}" "==" 3

    # Самый медленный sticky должен быть на порядки быстрее самого
    # быстрого nosticky — иначе утверждение статьи про 436-648x неверно.
    worst_sticky=$(grep 'sticky_hit=400' "${RUNS}/01-internals.log" \
        | sed -n 's/.*replay_mean_us=\([0-9.]*\).*/\1/p' | sort -g | tail -1)
    best_nosticky=$(grep 'sticky_hit=НЕТ_РЯДА' "${RUNS}/01-internals.log" \
        | sed -n 's/.*replay_mean_us=\([0-9.]*\).*/\1/p' | sort -g | head -1)
    if [[ -n "${worst_sticky}" && -n "${best_nosticky}" ]]; then
        ratio=$(awk -v a="${best_nosticky}" -v b="${worst_sticky}" 'BEGIN{printf "%.0f", a/b}')
        # Публикуется 436-648x; порог 300 оставляет запас на разброс
        # машины, но отсекает прогон, из которого «три порядка» уже не
        # следует.
        cmp_num "выигрыш sticky подтверждает опубликованный порядок" "${ratio}" ">=" 300
    else bad "отношение replay" "не удалось извлечь величины"; fi

    growth_off=$(num 01-internals 'прирост за 30 секунд без роли history: *\([0-9]*\)')
    cmp_num "без роли history прогресса нет" "${growth_off}" "==" 0
    growth_on=$(num 01-internals 'прирост после возврата роли: *\([0-9]*\)')
    cmp_num "после возврата роли прогресс идёт" "${growth_on}" ">" 0

    shards=$(sed -n '/число шардов history/{n;s/ *\([0-9]*\)/\1/p}' "${RUNS}/01-internals.log" | head -1)
    cmp_num "шардов history" "${shards}" ">" 0
    grep_has 01-internals "visibility вынесена из Postgres" "executions_visibility отсутствует"
else bad "01-internals" "нет лога"; fi

# ── 02-determinism ────────────────────────────────────────────────────
if have 02-determinism; then
    echo "-- 02-determinism"
    grep_has 02-determinism "исправленный воркфлоу проигрывается чисто" "PASS: TestFixedWorkflowReplaysCleanly"
    grep_has 02-determinism "сломанный воркфлоу расходится при replay" "PASS: TestBrokenWorkflowFailsReplay"
    grep_has 02-determinism "получена настоящая ошибка детерминизма" "TMPRL1100"

    # Цена replay обязана расти с длиной истории. Если рост пропал —
    # линейность из статьи 3 больше не подтверждается.
    mapfile -t meds < <(all_nums 02-determinism 'ЗАМЕР replay .*median_us=\([0-9]*\)')
    if (( ${#meds[@]} >= 4 )); then
        if (( meds[0] < meds[1] && meds[1] < meds[2] && meds[2] < meds[3] )); then
            ok "цена replay растёт с длиной истории (${meds[0]} < ${meds[1]} < ${meds[2]} < ${meds[3]})"
        else
            bad "цена replay" "ряд немонотонен: ${meds[*]}"
        fi
    else bad "цена replay" "меньше четырёх точек замера"; fi
else bad "02-determinism" "нет лога"; fi

# ── 03-activities ─────────────────────────────────────────────────────
if have 03-activities; then
    echo "-- 03-activities"
    # Структурная величина: две упавшие попытки плюс успешная дают ровно
    # три строки. Статья публикует «3», и проверять надо именно 3.
    unsafe=$(num 03-activities 'строк в side_effects для order-unsafe: *\([0-9]*\)')
    cmp_num "неидемпотентная activity дала ровно три эффекта" "${unsafe}" "==" 3
    safe=$(num 03-activities 'строк в side_effects для order-safe: *\([0-9]*\)')
    cmp_num "ключ идемпотентности оставил один эффект" "${safe}" "==" 1

    mid=$(num 03-activities 'прогресс до убийства воркера: *\([0-9]*\)')
    resumed=$(num 03-activities 'ПРОДОЛЖАЕМ с \([0-9]*\)')
    cmp_num "импорт успел продвинуться до убийства" "${mid}" ">" 0
    cmp_num "возобновление началось не с нуля" "${resumed}" ">" 0
    # Ключевое утверждение статьи 4: heartbeat отстаёт от факта.
    cmp_num "heartbeat отстал от фактического прогресса" "${resumed}" "<=" "${mid}"
    grep_has 03-activities "импорт доведён до конца" "done=60 total=60"

    reg=$(grep 'kind=regular' "${RUNS}/03-activities.log" | sed -n 's/.*events_per_call=\([0-9.]*\).*/\1/p' | head -1)
    loc=$(grep 'kind=local' "${RUNS}/03-activities.log" | sed -n 's/.*events_per_call=\([0-9.]*\).*/\1/p' | head -1)
    # Структурные величины: обычный вызов пишет шесть событий на вызов,
    # local — одно. Публикуются как 6,05 и 1,05.
    cmp_num "обычная activity пишет 6 событий истории на вызов" "${reg}" "==" 6.05
    cmp_num "local activity пишет 1 событие истории на вызов" "${loc}" "==" 1.05
else bad "03-activities" "нет лога"; fi

# ── 04-messaging ──────────────────────────────────────────────────────
if have 04-messaging; then
    echo "-- 04-messaging"
    before=$(num 04-messaging 'событий до пяти Query: *\([0-9]*\)')
    after=$(sed -n 's/.*событий до пяти Query: [0-9]*, после: \([0-9]*\).*/\1/p' "${RUNS}/04-messaging.log" | head -1)
    if [[ -n "${before}" && "${before}" == "${after}" ]]; then
        ok "Query не растит историю (${before} = ${after})"
    else bad "Query не растит историю" "до=${before}, после=${after}"; fi

    grep_has 04-messaging "Update с недопустимым значением отклонён" "ОТКЛОНЁН"
    grep_has 04-messaging "Update с допустимым значением принят" "ПРИНЯТ"
    grep_has 04-messaging "дочерние воркфлоу отработали" "children=5 results=5"

    # Пробел после mode=with обязателен: без него шаблон совпадает и со
    # строкой mode=without, и в переменную попадают ОБА значения.
    ev_without=$(grep 'ЗАМЕР can mode=without ' "${RUNS}/04-messaging.log" | sed -n 's/.*events=\([0-9]*\).*/\1/p')
    ev_with=$(grep 'ЗАМЕР can mode=with ' "${RUNS}/04-messaging.log" | sed -n 's/.*events=\([0-9]*\).*/\1/p')
    cmp_num "без Continue-As-New история набралась" "${ev_without}" ">" 100
    # Утверждение статьи 5 — «ограничена одной итерацией», а не «в N раз
    # меньше»: проверяем именно границу, а не отношение.
    cmp_num "с Continue-As-New история в пределах одной итерации" "${ev_with}" "<=" 100
else bad "04-messaging" "нет лога"; fi

# ── 05-versioning ─────────────────────────────────────────────────────
if have 05-versioning; then
    echo "-- 05-versioning"
    mapfile -t st < <(all_nums 05-versioning 'СТАТУС workflow=[^ ]* status=\([A-Za-z]*\)')
    mapfile -t mm < <(all_nums 05-versioning 'упоминаний расхождения в логе воркера: *\([0-9]*\)')
    if (( ${#st[@]} >= 3 && ${#mm[@]} >= 3 )); then
        [[ "${st[0]}" == "Running"   ]] && ok "наивная правка застряла" || bad "наивная правка" "статус ${st[0]}"
        [[ "${st[1]}" == "Completed" ]] && ok "патч под GetVersion починил" || bad "починка патчем" "статус ${st[1]}"
        [[ "${st[2]}" == "Running"   ]] && ok "рано снятый патч сломал снова" || bad "рано снятый патч" "статус ${st[2]}"
        cmp_num "расхождения при наивной правке" "${mm[0]}" ">" 0
        cmp_num "расхождений под патчем нет" "${mm[1]}" "==" 0
        cmp_num "расхождения при рано снятом патче" "${mm[2]}" ">" 0
    else bad "05-versioning" "в логе меньше трёх сценариев"; fi
else bad "05-versioning" "нет лога"; fi

# ── 06-operations ─────────────────────────────────────────────────────
if have 06-operations; then
    echo "-- 06-operations"
    mapfile -t s2s < <(all_nums 06-operations 'ЗАМЕР ops .*sched_to_start_mean_s=\([0-9.]*\)')
    if (( ${#s2s[@]} >= 3 )); then
        if awk -v a="${s2s[0]}" -v b="${s2s[1]}" -v c="${s2s[2]}" 'BEGIN{exit !(a>b && b>=c)}'; then
            ok "расширение воркера сокращает ожидание (${s2s[0]} > ${s2s[1]} >= ${s2s[2]})"
        else bad "ожидание задач" "ряд не убывает: ${s2s[*]}"; fi
        ratio=$(awk -v a="${s2s[0]}" -v b="${s2s[1]}" 'BEGIN{printf "%.1f", a/b}')
        # Публикуется 9,8x; порог 7 подтверждает именно этот вывод.
        cmp_num "выигрыш от расширения воркера подтверждает опубликованный" "${ratio}" ">=" 7
    else bad "06-operations" "меньше трёх конфигураций"; fi
    grep_has 06-operations "тест с промоткой времени прошёл" "PASS: TestDayLongWorkflowSkipsTime"
else bad "06-operations" "нет лога"; fi

# ── 07-languages ──────────────────────────────────────────────────────
if have 07-languages; then
    echo "-- 07-languages"
    n=$(grep -c '^ЯЗЫК .*outcome=allocated' "${RUNS}/07-languages.log")
    cmp_num "все пять SDK дошли до allocated" "${n}" "==" 5
    for l in go java ts python dotnet; do
        grep_has 07-languages "SDK ${l} отчитался о версии" "lang=${l} sdk="
    done
else bad "07-languages" "нет лога"; fi

echo
if (( fails )); then
    echo "assert-run: ГИПОТЕЗЫ НЕ ПОДТВЕРДИЛИСЬ (${fails}) — числа в FIXTURES и статьях устарели"
    exit 1
fi
echo "assert-run: все утверждения серии воспроизведены"
