#!/usr/bin/env bash
# observability.sh — проверяет встроенную наблюдаемость Picodata: веб-интерфейс
# и экспорт метрик в формате Prometheus. Оба появляются от одного флага
# `--http-listen`, плагины для этого не нужны — проверено на кластере с нулём
# установленных плагинов.
#
# Отдельно проверяется то, что меняет практику эксплуатации: состояние Raft
# (роль, лидер, терм) доступно ИМЕННО ЗДЕСЬ. Через SQL его не видно —
# `_pico_instance` не содержит признака лидера, а `current_master_name` в
# `_pico_replicaset` означает другое: мастера репликасета для записи vshard.
set -euo pipefail

PICO_HTTP="${PICO_HTTP:-http://127.0.0.1:8081}"
fail=0

echo "=== веб-интерфейс ==="
root="$(curl -sf --max-time 15 "${PICO_HTTP}/" || true)"
if echo "$root" | grep -q "<title>Picodata</title>"; then
    echo "  ok: отдаётся страница веб-интерфейса Picodata"
else
    echo "!!! на ${PICO_HTTP}/ нет страницы веб-интерфейса" >&2; fail=1
fi

echo "=== экспорт метрик ==="
metrics="$(curl -sf --max-time 15 "${PICO_HTTP}/metrics" || true)"
families="$(echo "$metrics" | grep -c '^# HELP' || true)"
if [ "$families" -lt 100 ]; then
    echo "!!! семейств метрик всего $families — эндпоинт отдаёт не то" >&2; fail=1
else
    echo "  семейств метрик: $families"
    for p in pico tnt lj; do
        n="$(echo "$metrics" | grep -c "^# HELP ${p}_" || true)"
        echo "    ${p}_*: $n"
    done
fi

echo "=== состояние Raft из метрик ==="
# Ровно то, чего нет в SQL. Роль инстанса приходит меткой state у метрики
# pico_raft_state со значением 1.
for m in pico_raft_leader_id pico_raft_state pico_raft_term; do
    line="$(echo "$metrics" | grep "^${m}[ {]" | head -1)"
    if [ -z "$line" ]; then
        echo "!!! нет метрики ${m}" >&2; fail=1
    else
        echo "  $line"
    fi
done

echo "=== кластерная часть метрик ==="
online="$(echo "$metrics" | grep -c '^pico_instance_state{.*state="Online"' || true)"
echo "  инстансов в состоянии Online видно: $online"
[ "$online" -ge 4 ] || { echo "!!! ожидалось не меньше 4" >&2; fail=1; }

# ГЛАВНОЕ: из того, что pico_instance_state перечисляет весь кластер, НЕ следует,
# что достаточно опрашивать один узел. Раскладка по памяти (tnt_*), сборщик
# мусора LuaJIT (lj_*) и роль в Raft относятся к тому инстансу, который опрошен.
# Документация Picodata требует отдельный адрес сбора метрик под каждый инстанс,
# поэтому проверяем ВСЕ узлы: у каждого должен быть свой полный набор.
echo "=== метрики на каждом инстансе отдельно ==="
declare -A seen_roles=()
for port in 8081 8082 8083 8084; do
    m="$(curl -sf --max-time 15 "http://127.0.0.1:${port}/metrics" || true)"
    if [ -z "$m" ]; then
        echo "!!! инстанс на порту ${port} не отдаёт метрики" >&2; fail=1; continue
    fi
    fam="$(echo "$m" | grep -c '^# HELP' || true)"
    tnt="$(echo "$m" | grep -c '^# HELP tnt_' || true)"
    who="$(echo "$m" | grep -oE '^pico_raft_state\{state="[A-Za-z]+",instance_name="[^"]+"' \
           | head -1 | sed -E 's/.*state="([A-Za-z]+)",instance_name="([^"]+)".*/\2 \1/')"
    echo "  :${port} — семейств ${fam}, из них tnt_* ${tnt}, raft: ${who:-неизвестно}"
    [ "$fam" -ge 100 ] || { echo "!!! на порту ${port} неполный набор метрик" >&2; fail=1; }
    [ "$tnt" -ge 50 ]  || { echo "!!! на порту ${port} нет метрик уровня инстанса" >&2; fail=1; }
    [ -n "$who" ] || { echo "!!! на порту ${port} нет собственной raft-роли" >&2; fail=1; }
    seen_roles["${who##* }"]=1
done

# Роли должны РАЗЛИЧАТЬСЯ между узлами — это и доказывает, что метрики
# per-instance, а не общий кластерный снимок, одинаковый на всех.
if [ -n "${seen_roles[Leader]:-}" ] && [ -n "${seen_roles[Follower]:-}" ]; then
    echo "  ok: роли различаются между инстансами (есть и Leader, и Follower)"
else
    echo "!!! ожидались разные raft-роли на разных инстансах: $(echo "${!seen_roles[@]}")" >&2
    fail=1
fi

echo
[ "$fail" -eq 0 ] || { echo "ПРОВАЛ: встроенная наблюдаемость отвечает не так, как ожидалось" >&2; exit 1; }
echo "ok: веб-интерфейс и метрики работают; у каждого инстанса свой набор, роли различаются"
