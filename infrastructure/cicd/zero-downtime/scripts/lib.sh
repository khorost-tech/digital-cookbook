#!/usr/bin/env bash
# Общие функции для демо уровня A (compose + loadgen). Подключается через source.
# Контейнер и DSN совпадают с compose/compose.yml.
CONTAINER=cicd-zdt-postgres
DSN="postgres://zdt:zdt@localhost:5457/zdt"

# Каталог zero-downtime/ (на уровень выше scripts/).
ZDT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Проверка доступности PostgreSQL. Без неё недоступный контейнер выглядел бы как
# «загрузчик почему-то не даёт ошибок» — молчаливый ложный результат, а не отказ.
require_pg() {
    if ! docker exec "$CONTAINER" pg_isready -U zdt -d zdt >/dev/null 2>&1; then
        echo "ОШИБКА: PostgreSQL (контейнер $CONTAINER) недоступен." >&2
        echo "Подними стенд: docker compose -f compose/compose.yml up -d" >&2
        exit 1
    fi
}

# psql внутри контейнера.
psql_c() { docker exec "$CONTAINER" psql -U zdt -d zdt "$@"; }
psql_q() { docker exec "$CONTAINER" psql -U zdt -d zdt -t -A "$@"; }

# psql_ddl "SQL" — выполнить ТЯЖЁЛЫЙ DDL под lock_timeout с повторами.
#
# Любой ALTER TABLE, берущий ACCESS EXCLUSIVE (ADD/DROP COLUMN, SET NOT NULL,
# ADD/DROP CONSTRAINT), ждёт завершения активных транзакций и, пока ждёт, копит
# за собой очередь новых запросов. Без lock_timeout одна долгая транзакция
# превращает «мгновенную» операцию в остановку приёма трафика. Поэтому каждый
# такой DDL идёт через этот helper: ограниченное ожидание, ограниченное число
# попыток и fail-closed — не смогли взять блокировку, значит миграция НЕ
# состоялась, а не «как-нибудь потом».
#
# Стенд однопользовательский, поэтому здесь это ветка страховки, а не замер;
# в проде — основной режим работы.
psql_ddl() {
    local sql="$1" attempts="${2:-3}" i out
    for ((i = 1; i <= attempts; i++)); do
        # Присваивание с подстановкой команды ОБЯЗАНО стоять в условном
        # контексте. Голое `out="$(...)"; rc=$?` при set -e не работает:
        # статусом присваивания становится статус команды, и shell завершается
        # ДО строки с $? — ни классификации ошибки, ни печати $out не будет.
        if out="$(psql_c -v ON_ERROR_STOP=1 -c "SET lock_timeout = '2s'; $sql" 2>&1)"; then
            return 0
        fi
        # Повторять осмысленно ТОЛЬКО таймаут блокировки. Синтаксическая ошибка,
        # нарушение ограничения или обрыв соединения от повтора не исчезнут, а
        # описывать их как «блокировка не взята» — вводить в заблуждение.
        if printf '%s' "$out" | grep -qiE 'lock timeout|55P03'; then
            echo "  попытка ${i}/${attempts}: блокировка не взята за 2с — ${sql:0:56}…" >&2
            sleep 2
            continue
        fi
        echo "ОШИБКА PostgreSQL (не таймаут блокировки) — повтор бессмыслен:" >&2
        printf '%s\n' "$out" >&2
        return 1
    done
    echo "ОШИБКА: блокировку не удалось взять за ${attempts} попыток: ${sql}" >&2
    echo "Проверь долгие транзакции: SELECT * FROM pg_stat_activity WHERE state <> 'idle';" >&2
    return 1
}



# Возврат схемы к исходной перед демо.
reset_schema() {
    docker exec -i "$CONTAINER" psql -U zdt -d zdt -q < "$ZDT_ROOT/sql/reset.sql"
}

# Сборка бинаря загрузчика в указанный путь (быстрее и предсказуемее, чем go run
# в фоне при каждом прогоне).
build_loadgen() {
    local out="$1"
    ( cd "$ZDT_ROOT/loadgen" && GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}" \
        GOSUMDB="${GOSUMDB:-off}" go build -o "$out" . )
}

# Извлечь число ошибок из строки `выполнено=N ошибок=M`.
parse_errors() { grep -oE 'ошибок=[0-9]+' "$1" | grep -oE '[0-9]+'; }
parse_done()   { grep -oE 'выполнено=[0-9]+' "$1" | grep -oE '[0-9]+'; }
