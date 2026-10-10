#!/usr/bin/env bash
# Фактчек-гейт стенда: снимает фактические версии компонентов и сверяет с
# зафиксированными. Любое расхождение — падение, а не предупреждение:
# версия, записанная в FIXTURES.md и в статью, должна быть той, на которой
# реально сделан прогон.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXPECT_SERVER="1.29.7"
EXPECT_UI="2.53.3"
EXPECT_PG_MAJOR="18"
EXPECT_ES_MAJOR="8"

fail=0
check() {
    local what="$1" got="$2" want="$3"
    if [[ "${got}" == *"${want}"* ]]; then
        printf '  %-28s %s\n' "${what}" "${got}"
    else
        printf '  %-28s %s   ОЖИДАЛОСЬ: %s  <-- РАСХОЖДЕНИЕ\n' "${what}" "${got}" "${want}"
        fail=1
    fi
}

section "версии стенда, снятые живьём"

server_ver="$(cli_in_net probe-cli \
    operator cluster describe --address "${TEMPORAL_ADDR}" --output json \
    | sed -n 's/.*"serverVersion"[^"]*"\([^"]*\)".*/\1/p')"
check "Temporal Server" "${server_ver}" "${EXPECT_SERVER}"

ui_ver="$(docker inspect temporal-ui --format '{{index .Config.Image}}')"
check "Temporal UI (образ)" "${ui_ver}" "${EXPECT_UI}"

pg_ver="$(pg_query 'SELECT version();')"
check "PostgreSQL" "${pg_ver}" "PostgreSQL ${EXPECT_PG_MAJOR}"

es_ver="$(es_get / | sed -n 's/.*"number"[^"]*"\([^"]*\)".*/\1/p')"
check "Elasticsearch" "${es_ver}" "${EXPECT_ES_MAJOR}."

go_ver="$(run_in_net probe-go "${GO_IMAGE}" go version)"
check "Go (образ сборки)" "${go_ver}" "go1.26"

# В go.mod зависимость встречается и одиночной строкой `require pkg vX`,
# и внутри блока require — берём версию, идущую сразу за именем модуля.
sdk_ver="$(grep -oE 'go\.temporal\.io/sdk v[0-9][^ ]*' "${STAND_DIR}/go/go.mod" | head -1 | awk '{print $2}')"
[[ -n "${sdk_ver}" ]] || { echo "  НЕ НАЙДЕНА версия go.temporal.io/sdk в go.mod"; fail=1; }
printf '  %-28s %s\n' "Temporal Go SDK" "${sdk_ver}"

section "роли Temporal подняты раздельно"
for role in frontend history matching worker; do
    state="$(docker inspect "temporal-${role}" --format '{{.State.Status}}' 2>/dev/null || echo "НЕТ КОНТЕЙНЕРА")"
    printf '  %-28s %s\n' "temporal-${role}" "${state}"
    [[ "${state}" == "running" ]] || fail=1
done

section "visibility в Elasticsearch, а не в Postgres"
idx="$(es_get "/_cat/indices/temporal_visibility_v1_dev?h=index" || true)"
if [[ -n "${idx}" ]]; then
    printf '  %-28s %s\n' "индекс visibility" "${idx}"
else
    printf '  %-28s НЕТ ИНДЕКСА  <-- РАСХОЖДЕНИЕ\n' "индекс visibility"
    fail=1
fi

echo
if (( fail )); then
    echo "probe: ЕСТЬ РАСХОЖДЕНИЯ — числа с этого прогона в фикстуры не берём"
    exit 1
fi
echo "probe: всё сходится"
