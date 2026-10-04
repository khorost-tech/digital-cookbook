#!/usr/bin/env bash
# Артефакт 6: чем SKIP LOCKED отличается от голого FOR UPDATE при N воркерах.
# Демонстрация чисто на SQL — без Go, чтобы разница была видна на самом примитиве.
#
# ВАЖНО, что этот артефакт НЕ доказывает. В Задаче 2 (worker/queue_test.go,
# TestClaimedJobIsNotHandedOutAgain) экспериментально показано: тест «одна
# джоба не выдаётся дважды» проходит и с полностью убранным SKIP LOCKED.
# Эксклюзивность захвата даёт блокировка строки плюс переход состояния
# queued -> running, а не SKIP LOCKED. SKIP LOCKED ничего не добавляет к
# корректности — он покупает ПРОПУСКНУЮ СПОСОБНОСТЬ: без него N воркеров,
# заставших одну и ту же первую по порядку строку, выстраиваются в очередь
# друг за другом; с ним каждый сразу берёт свою и они идут параллельно.
# Ниже это измеряется по ВРЕМЕНИ ОЖИДАНИЯ, а не по тому, кто что захватил.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# --- Проверка окружения. Без неё недоступный Postgres выглядел бы как
# «замеры почему-то нулевые» — молчаливый ложный результат демонстрации,
# а не её явный отказ. ---
if ! docker exec bj-postgres pg_isready -U jobs -d jobs >/dev/null 2>&1; then
    echo "ОШИБКА: PostgreSQL (контейнер bj-postgres) недоступен." >&2
    echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
    exit 1
fi

# trap: обе сессии-держатели блокировки запускаются через "docker exec -d" —
# это ОТДЕЛЬНЫЙ psql-процесс ВНУТРИ контейнера, а не фоновое задание локального
# bash (у "docker exec -d" нет своего $!, kill $(jobs -p) его не достанет).
# При прерывании скрипта посреди прогона такой psql остался бы жить в
# контейнере и удерживал бы блокировку строки все оставшиеся из pg_sleep(4)
# секунды. pg_terminate_backend по WHERE на сам текст запроса (pg_sleep — здесь
# единственный демо-скрипт стенда, который его использует) закрывает такую
# сессию сразу же, а не через таймаут.
cleanup() {
    docker exec bj-postgres psql -U jobs -d jobs -q -c \
        "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
         WHERE datname='jobs' AND query LIKE 'SELECT pg_sleep%' AND pid <> pg_backend_pid()" \
        >/dev/null 2>&1 || true
}
trap cleanup EXIT

seed() {
    docker exec bj-postgres psql -U jobs -d jobs -q -c "TRUNCATE jobs RESTART IDENTITY"
    bash scripts/seed.sh 5
}

# Счётчик ожиданий блокировок из лога PostgreSQL (log_lock_waits=on,
# deadlock_timeout=200ms — см. compose/compose.yml). ВАЖНО: docker logs
# отдаёт лог за ВСЮ жизнь контейнера, включая предыдущие прогоны этого же
# скрипта и чужие эксперименты. Если просто грепать весь лог, число будет
# расти от прогона к прогону и ничего не скажет о ТЕКУЩЕМ эксперименте.
# Поэтому меряем ДЕЛЬТУ: снимаем счётчик до блока и после — разница и есть
# число ожиданий, случившихся именно в этом блоке этого прогона.
count_lock_waits() {
    docker logs bj-postgres 2>&1 | grep -ci "still waiting for" || true
}

echo "=== БЕЗ SKIP LOCKED: вторая сессия ЖДЁТ первую ==="
seed
BASELINE_NOSKIP=$(count_lock_waits)
# Сессия A берёт первую по порядку queued-строку и держит транзакцию открытой 4с.
docker exec -d bj-postgres psql -U jobs -d jobs -c \
    "BEGIN; SELECT id FROM jobs WHERE state='queued' ORDER BY id FOR UPDATE LIMIT 1; SELECT pg_sleep(4); COMMIT;"
sleep 1
echo "--- сессия B тем же запросом БЕЗ SKIP LOCKED пытается взять ту же строку, замеряем время ---"
START=$(date +%s%3N)
docker exec bj-postgres psql -U jobs -d jobs -t -A -c \
    "BEGIN; SELECT id FROM jobs WHERE state='queued' ORDER BY id FOR UPDATE LIMIT 1; COMMIT;" >/dev/null
END=$(date +%s%3N)
WAIT_NOSKIP_MS=$((END - START))
echo "сессия B ждала: ${WAIT_NOSKIP_MS} мс (ожидание блокировки первой строки, удерживаемой сессией A)"
sleep 4
AFTER_NOSKIP=$(count_lock_waits)
WAITS_NOSKIP=$((AFTER_NOSKIP - BASELINE_NOSKIP))

echo
echo "=== СО SKIP LOCKED: вторая сессия берёт СЛЕДУЮЩУЮ строку сразу ==="
seed
BASELINE_SKIP=$(count_lock_waits)
docker exec -d bj-postgres psql -U jobs -d jobs -c \
    "BEGIN; SELECT id FROM jobs WHERE state='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1; SELECT pg_sleep(4); COMMIT;"
sleep 1
START=$(date +%s%3N)
# Вывод -t -A всё равно содержит служебные статусы BEGIN/COMMIT (это команды
# управления транзакцией, а не результат запроса, -t их не подавляет) —
# при трёх операторах в -c это ровно 3 строки: BEGIN / id / COMMIT.
# Нужна вторая — сам id, а не первая строка ("BEGIN").
GOT=$(docker exec bj-postgres psql -U jobs -d jobs -t -A -c \
    "BEGIN; SELECT id FROM jobs WHERE state='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1; COMMIT;" | sed -n '2p')
END=$(date +%s%3N)
WAIT_SKIP_MS=$((END - START))
echo "сессия B получила строку id=${GOT} за ${WAIT_SKIP_MS} мс — не дожидаясь первой (строка сессии A пропущена)"
sleep 4
AFTER_SKIP=$(count_lock_waits)
WAITS_SKIP=$((AFTER_SKIP - BASELINE_SKIP))

echo
echo "--- ожидания блокировок в логе PostgreSQL за ТЕКУЩИЙ прогон (дельта, не сумма с начала контейнера) ---"
echo "без SKIP LOCKED: ${WAITS_NOSKIP} ожидание(й)"
echo "со SKIP LOCKED:  ${WAITS_SKIP} ожидание(й)"
echo
echo "Вывод: без SKIP LOCKED воркеры выстраиваются в очередь ЗА ОДНОЙ строкой и"
echo "работают по очереди (сессия B ждала ${WAIT_NOSKIP_MS} мс); со SKIP LOCKED каждый берёт"
echo "свою строку и они идут параллельно (сессия B ждала ${WAIT_SKIP_MS} мс, id другой)."
echo "Это разница в ПРОПУСКНОЙ СПОСОБНОСТИ, а не в корректности: обе строки уже"
echo "заблокированы ровно тем воркером, который их обрабатывает, независимо от"
echo "SKIP LOCKED — вопрос лишь в том, ждут ли остальные воркеры своей очереди"
echo "за уже занятой строкой или сразу переходят к свободной."
echo
echo "ПАДАЮЩИЙ ВАРИАНТ: если бы SKIP LOCKED не влиял на поведение, время ожидания"
echo "и число ожиданий блокировок со SKIP LOCKED были бы сопоставимы с вариантом"
echo "без него (сессия B так же ждала бы секунды и лог показывал бы >=1 ожидание"
echo "вместо 0), а не отличались на порядки."
