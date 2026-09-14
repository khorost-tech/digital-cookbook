#!/usr/bin/env bash
# Артефакт 1: воркер умирает посередине джобы — что происходит с ней.
# Джобу удерживает состояние state='running', а НЕ живая блокировка строки и
# НЕ живая аренда: claimSQL (worker/queue.go) — один UPDATE в автокоммите,
# блокировка строки снимается сразу по завершении этого запроса, задолго до
# конца «аренды»; сам claimSQL фильтрует кандидатов только по
# state='queued' — leased_until в его WHERE вообще не участвует. Поэтому
# второй воркер получает 0 джоб и при живой аренде, и при истёкшей — пока
# джоба state=running, её не видно НИКАК, кроме явного reclaim. Аренда
# (leased_until/leased_by) не про «не отдать её сейчас» — она про «когда-нибудь
# вернуть», и делает это ReclaimExpired, переводя running-джобы с истёкшей
# арендой обратно в queued. Ниже это проверено дважды: сразу после kill -9
# (аренда ещё жива) и ещё раз после истечения аренды, но ДО запуска reclaim
# (аренда уже мертва, а второй воркер всё равно получает 0 джоб) — только
# после этого выполняется reclaim и джоба возвращается в очередь.
#
# Артефакт 2: heartbeat — это то, что не даёт аренде истечь, пока воркер жив
# и работает. Долгая джоба (6с) при коротком lease (2с) переживает свой
# исходный срок аренды ТОЛЬКО если воркер продлевает её в процессе работы.
# Контраст построен как в shutdown-demo.sh (тот же приём, найденный в Задаче 5,
# коммит badc1ff): в ОБЕИХ ветках (heartbeat включён/выключен) конкурирующий
# `-reclaim` запускается в ОДИН и тот же момент (4-я секунда работы) — иначе
# разница доказывала бы не флаг -heartbeat, а то, что reclaim вообще
# запускался только в одной из веток.
#
# Оба артефакта показаны в форме, которую можно сломать и увидеть другой
# вывод (см. echo-пояснения после каждого блока) — это и есть фальсификация,
# а не утверждение на веру.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOPROXY=https://go.khorost.tech,direct

PSQL=(docker exec bj-postgres psql -U jobs -d jobs -t -A -F'|')

# --- Проверка окружения. Без неё недоступный Postgres или несобранный
# воркер выглядели бы как «0 джоб взято» — то есть как молчаливый ложный
# успех демонстрации, а не как её отказ. ---
if ! docker exec bj-postgres pg_isready -U jobs -d jobs >/dev/null 2>&1; then
    echo "ОШИБКА: PostgreSQL (контейнер bj-postgres) недоступен." >&2
    echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
    exit 1
fi

WORKER_BIN="/tmp/bj-lease-demo-worker.$$"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -f "$WORKER_BIN"' EXIT

echo "--- сборка воркера ---"
if ! ( cd worker && go build -o "$WORKER_BIN" . ); then
    echo "ОШИБКА: воркер не собрался — демонстрация невозможна." >&2
    exit 1
fi
# Дальше используем собранный бинарник напрямую (не go run): это делает
# таймout'ы предсказуемыми (нет переменной задержки на компиляцию) и
# позволяет убивать процесс воркера напрямую, без промежуточного go-build.

echo
echo "=== АРТЕФАКТ 1: смерть воркера посередине джобы ==="
docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY"
bash scripts/seed.sh 1 >/dev/null

# Аренда 6с, «работа» 60с — заведомо дольше аренды: если бы воркер дожил до
# конца, он продлевал бы её heartbeat'ом (см. артефакт 2), но мы убьём его
# раньше, не дав это сделать.
"$WORKER_BIN" -id dying -work 60s -lease 6s -for 60s >/tmp/bj-dying.log 2>&1 &
DEMO_PID=$!
sleep 3
echo "--- состояние ПОКА воркер жив (аренда взята ~3с назад из 6с, ещё действует) ---"
"${PSQL[@]}" -c "SELECT id, state, leased_by, leased_until > now() AS lease_alive, attempt FROM jobs"

kill -9 "$DEMO_PID" 2>/dev/null || true
# wait ЗДЕСЬ, с подавленным stderr — намеренно: без него bash напечатает в
# stderr асинхронное "lease-demo.sh: line N: PID Killed ..." при следующей
# проверке статуса фоновых заданий (см. отчёт финального ревью, находка
# minor 9) — это не относится к демонстрации и портит записанный вывод.
wait "$DEMO_PID" 2>/dev/null || true
echo "--- воркер убит; джоба ВСЁ ЕЩЁ в state=running (аренда пока не истекла) ---"
"${PSQL[@]}" -c "SELECT id, state, leased_by, leased_until > now() AS lease_alive FROM jobs"
echo "--- другой воркер получает 0 джоб — но это ПОКА не доказательство: аренда ещё жива ---"
"$WORKER_BIN" -id other-1 -work 100ms -for 2s | tail -2

echo "--- ждём истечения аренды; reclaim ПОКА НЕ запускаем ---"
sleep 4
echo "--- аренда истекла (lease_alive=f), но state всё ещё running — reclaim ещё не запускался ---"
"${PSQL[@]}" -c "SELECT id, state, leased_by, leased_until > now() AS lease_alive FROM jobs"
echo "--- КОНТРОЛЬНЫЙ эксперимент: второй воркер СНОВА получает 0 джоб — при УЖЕ ИСТЁКШЕЙ"
echo "    аренде и без единой удерживаемой блокировки строки (claimSQL — однократный"
echo "    автокоммит-UPDATE). Держит джобу только state=running. ---"
"$WORKER_BIN" -id other-2 -work 100ms -for 2s | tail -2

echo "--- только теперь запускаем reclaim: это ОН, а не сам факт истечения аренды,"
echo "    переводит джобу обратно в queued ---"
"$WORKER_BIN" -reclaim | tail -1
"${PSQL[@]}" -c "SELECT id, state, leased_by, attempt FROM jobs"
echo "--- теперь джобу берёт другой воркер, попытка вторая ---"
"$WORKER_BIN" -id other-3 -work 100ms -for 4s | tail -2
"${PSQL[@]}" -c "SELECT id, state, attempt FROM jobs"
echo "Что это отбивает (не общие слова, а конкретные ложные тезисы):"
echo "  тезис «джобу держит блокировка строки» — опровергнут: claimSQL — однократный"
echo "  автокоммит-UPDATE, блокировка снята задолго до kill -9, а джоба всё ещё running;"
echo "  тезис «джобу держит живая аренда» — опровергнут: второй воркер получает 0 джоб"
echo "  ДАЖЕ когда leased_until уже в прошлом (lease_alive=f) — держит именно state, а"
echo "  claimSQL фильтрует по state, не по leased_until;"
echo "  тезис «джоба потеряна навсегда» — опровергнут: после явного reclaim state=queued,"
echo "  и джоба выполняется (attempt=2)."

echo
echo "=== АРТЕФАКТ 2: heartbeat против конкурирующего reclaim ==="
docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY"
bash scripts/seed.sh 1 >/dev/null
echo "--- джоба 6с при аренде 2с, heartbeat ВКЛЮЧЁН; конкурирующий -reclaim — на 4-й секунде ---"
"$WORKER_BIN" -id hb-on -work 6s -lease 2s -for 10s -heartbeat=true \
    >/tmp/bj-lease-hbon.log 2>&1 &
HBON_PID=$!
sleep 4
"$WORKER_BIN" -reclaim | tail -1
wait "$HBON_PID" 2>/dev/null || true
tail -2 /tmp/bj-lease-hbon.log
"${PSQL[@]}" -c "SELECT id, state, attempt FROM jobs"

docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY"
bash scripts/seed.sh 1 >/dev/null
echo "--- то же самое, heartbeat ВЫКЛЮЧЕН (падающий вариант); тот же reclaim на той же 4-й секунде ---"
"$WORKER_BIN" -id hb-off -work 6s -lease 2s -for 10s -heartbeat=false &
HBOFF_PID=$!
sleep 4
"$WORKER_BIN" -reclaim | tail -1
wait "$HBOFF_PID" 2>/dev/null || true
"${PSQL[@]}" -c "SELECT id, state, attempt FROM jobs"
echo "Контраст и есть доказательство, не утверждение на веру:"
echo "  ЕДИНСТВЕННОЕ отличие между ветками — флаг -heartbeat; work/lease/for и момент"
echo "  конкурирующего reclaim (4-я секунда в ОБЕИХ ветках) одинаковы;"
echo "  с heartbeat (тики каждые lease/3=~0.67с) аренда продлевается быстрее, чем истекает —"
echo "  reclaim на 4-й секунде находит 0 истёкших аренд, джоба доработана тем же воркером,"
echo "  attempt остаётся 1;"
echo "  без heartbeat лизинг 2с короче работы 6с — аренда истекает ПОСРЕДИ работы, тот же"
echo "  reclaim в тот же момент находит истёкшую аренду и возвращает джобу в очередь, тот же"
echo "  воркер (ещё дорабатывающий свою копию) успевает забрать её повторно — attempt=2."
echo "  Если бы читатель просто флипнул -heartbeat на false в первой ветке, он увидел бы"
echo "  РОВНО вторую ветку — attempt=2, а не attempt=1: разница определяется флагом, а не"
echo "  тем, что reclaim запускался только в одной из веток."
