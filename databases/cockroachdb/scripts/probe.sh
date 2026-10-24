#!/usr/bin/env bash
#
# probe.sh — снимает с ЖИВОГО кластера всё, что в статьях нельзя утверждать
# по памяти: версию, состояние лицензии, уровень изоляции по умолчанию,
# ключевые настройки и допустимые границы zone config.
#
# Ничего не меняет в кластере, кроме временной таблицы для пробы zone config,
# которую сам же и удаляет.
#
# Запуск (из databases/cockroachdb/):  bash scripts/probe.sh

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

echo "=== Версия ==="
psql_c -e "SELECT version();"
docker inspect crdb1 --format 'образ: {{.Config.Image}}'
docker exec crdb1 cockroach version --format=tsv 2>/dev/null | head -5

echo
echo "=== Лицензия и телеметрия ==="
# Названия настроек менялись между ветками — печатаем всё, что похоже,
# и разбираем глазами, а не гадаем по одному имени.
psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
           WHERE variable LIKE '%license%' OR variable LIKE '%diagnostics%'
              OR variable LIKE '%telemetry%';"

echo
echo "=== Уровень изоляции по умолчанию ==="
psql_c -e "SHOW default_transaction_isolation;"
psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
           WHERE variable LIKE '%read_committed%' OR variable LIKE '%isolation%';"

echo "--- принимается ли READ COMMITTED и остаётся ли им:"
psql_c -e "BEGIN TRANSACTION ISOLATION LEVEL READ COMMITTED;
           SHOW transaction_isolation;
           COMMIT;"

echo
echo "=== Часы и время ==="
psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
           WHERE variable LIKE 'kv.closed_timestamp%';"
psql_c -e "SHOW ZONE CONFIGURATION FROM RANGE default;"
docker inspect crdb1 --format 'аргументы старта: {{json .Config.Cmd}}'

echo
echo "=== Нижний предел range_max_bytes ==="
# Нужен для сценария split: если минимально допустимый размер диапазона
# больше, чем мы готовы налить данных, split придётся делать явным
# SPLIT AT, а не ростом таблицы. Пробуем заведомо малые значения
# и печатаем текст отказа — он и есть ответ.
psql_c -e "CREATE TABLE IF NOT EXISTS probe_zone (id INT8 PRIMARY KEY);" >/dev/null

probe_zone() { # $1 = min байт, $2 = max байт, $3 = человекочитаемо
    if psql_c -e "ALTER TABLE probe_zone CONFIGURE ZONE USING range_min_bytes = $1, range_max_bytes = $2;" >/dev/null 2>&1; then
        echo "  $3: ПРИНЯТ"
    else
        echo "  $3: ОТВЕРГНУТ"
        psql_c -e "ALTER TABLE probe_zone CONFIGURE ZONE USING range_min_bytes = $1, range_max_bytes = $2;" 2>&1 \
            | grep -iE 'error|ERROR' | head -2 | sed 's/^/      /'
    fi
}

probe_zone 1048576   8388608   "8 МиБ"
probe_zone 16777216  33554432  "32 МиБ"
probe_zone 33554432  67108864  "64 МиБ"
probe_zone 67108864  134217728 "128 МиБ"

psql_c -e "DROP TABLE probe_zone;" >/dev/null

echo
echo "=== Готово ==="
echo "Ответить письменно на три вопроса гейта:"
echo "  1. SERIALIZABLE — уровень по умолчанию?"
echo "  2. READ COMMITTED принимается и остаётся READ COMMITTED?"
echo "  3. Минимально допустимый range_max_bytes — какой?"
