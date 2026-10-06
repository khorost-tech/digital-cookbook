#!/usr/bin/env bash
# blue-green.sh <старая> <новая> — обновление плагина без остановки кластера.
#
# Что установлено живым прогоном на этом стенде:
#
# 1. Обе версии физически лежат в share_dir и обе известны кластеру, но
#    ВКЛЮЧЕНА может быть только одна. Попытка включить новую, не выключив
#    старую, отвергается:
#      "plugin `near_data:0.2.0` is already enabled with a different version
#       0.1.0"
#    Поэтому порядок именно такой: CREATE -> ADD SERVICE -> MIGRATE ->
#    DISABLE старой -> ENABLE новой.
#
# 2. MIGRATE TO на версию с тем же набором файлов миграций проходит без
#    повторного применения — уже применённые миграции кластер помнит.
#
# 3. RPC-эндпоинты ВЕРСИОНИРОВАНЫ: в журнале видно, как снимается
#    `near_data.near_data_service:v0.1.0/top_products` и регистрируется
#    `...:v0.2.0/top_products`. Переключение по RPC происходит сразу.
#
# 4. А вот HTTP-маршруты из шаблона pike версии НЕ несут: они регистрируются в
#    общем HTTP-сервере, и после переключения запрос продолжает обслуживать
#    обработчик из СТАРОЙ библиотеки — ответ приходит с "served_by_version"
#    предыдущей версии, хотя в _pico_plugin включена новая. Маршрут отдаёт новую
#    версию только после перезапуска инстанса. Если blue-green нужен по-честному
#    и без рестарта — точкой входа должен быть RPC, а не HTTP.
set -euo pipefail

OLD="${1:?usage: blue-green.sh <старая версия> <новая версия>}"
NEW="${2:?usage: blue-green.sh <старая версия> <новая версия>}"
SERVICE="${SERVICE:-near_data_service}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"
PICO_HTTP="${PICO_HTTP:-http://127.0.0.1:8081}"

q() { MSYS_NO_PATHCONV=1 docker exec picodata-origin psql "$PICO_DSN" -c "$1"; }
state() {
    MSYS_NO_PATHCONV=1 docker exec picodata-origin psql "$PICO_DSN" \
        -c "SELECT name, version, enabled FROM _pico_plugin"
}
served() {
    curl -sf --max-time 60 "${PICO_HTTP}/top_products?category=tools&limit=1" \
        | grep -oE '"served_by_version":"[^"]+"' || echo "(нет ответа)"
}

# Что именно этот скрипт доказывает про RPC — и чего не доказывает.
#
# Функционально здесь проверяется ТОЛЬКО HTTP: по нему видно, какая версия
# реально обслуживает запрос. Вызвать RPC плагина из доступных стенду каналов
# не получилось: снаружи он требует непокрытой документацией связки прав, а в
# admin-консоли штатной функции вызова RPC нет (перебор `pico` её не содержит).
#
# Поэтому про RPC скрипт печатает КОСВЕННЫЙ признак — строки журнала о снятии
# версионированного эндпоинта старой версии и регистрации новой. Это говорит,
# что путь перерегистрирован, но не что он ответил. Настоящий вызов RPC живёт в
# интеграционном тесте плагина (`tests/integration.rs`, execute_rpc) — там он
# проверяется по-честному, но требует окружения picotest.
#
# Чего скрипт не измеряет вовсе: окно недоступности между DISABLE и ENABLE.
# Команды выполняются последовательно, и промежуток между ними никак не
# замеряется — утверждать «переключение без простоя» на этом основании нельзя.
rpc_journal() {
    docker logs pd-1 2>&1 | grep -E "(un)?registered RPC endpoint" | tail -4
}

echo "=== до переключения ==="
state
echo "  HTTP обслуживает: $(served)"

q "CREATE PLUGIN near_data ${NEW}"
q "ALTER PLUGIN near_data ${NEW} ADD SERVICE ${SERVICE} TO TIER default"
q "ALTER PLUGIN near_data MIGRATE TO ${NEW}"
q "ALTER PLUGIN near_data ${OLD} DISABLE"
q "ALTER PLUGIN near_data ${NEW} ENABLE"

echo "=== после переключения ==="
state
echo "  HTTP обслуживает: $(served)"
echo
echo "=== журнал регистрации RPC-эндпоинтов (косвенный признак) ==="
rpc_journal | sed 's/^/  /'
echo
echo "Если версия в HTTP-ответе осталась прежней — это не сбой переключения, а"
echo "неверсионированный HTTP-маршрут: судя по журналу, версионированный"
echo "RPC-путь уже перерегистрирован, а HTTP подхватит только после перезапуска"
echo "инстанса."
echo
echo "Границы доказательства: функционально проверен только HTTP. Про RPC здесь"
echo "показан журнал, а не ответ; настоящий вызов — в tests/integration.rs."
echo "Окно недоступности между DISABLE и ENABLE не измерялось."
