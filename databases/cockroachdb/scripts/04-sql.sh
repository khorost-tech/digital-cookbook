#!/usr/bin/env bash
#
# 04-sql.sh — артефакт к статье «SQL поверх KV: DistSQL и оптимизатор».
#
# Три замера:
#   A. один запрос в локальном и распределённом режиме плана;
#   B. что делает с планом статистика (на старте её нет — видно в шапке
#      EXPLAIN ANALYZE как «missing stats»);
#   C. что делает индекс: полное сканирование против точечного чтения.
#
# Порядок именно такой: статистика меняет планы для всего, поэтому она
# идёт до индекса, а не после — иначе эффекты смешаются.
#
# Каждый замер повторяется несколько раз: на одиночных прогонах режимы
# меняются местами от запуска к запуску (проверено в 02-time).
#
# Запуск (из databases/cockroachdb/):  bash scripts/04-sql.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

require_cluster 3 120

REPS="${REPS:-5}"

Q="SELECT c.city, count(*) AS n, sum(o.total) AS total
   FROM orders o JOIN customers c ON c.customer_id = o.customer_id
   WHERE o.created_at > now() - INTERVAL '90 days'
   GROUP BY c.city ORDER BY total DESC"

# Шапка EXPLAIN ANALYZE — всё, что нужно для сравнения планов.
#
# Вывод сначала целиком забирается в переменную и только потом фильтруется.
# Конвейер вида «psql | grep | head» под set -e -o pipefail падает с кодом
# 141: head закрывает канал, psql получает SIGPIPE, и весь скрипт умирает
# посреди замера. Уже наступали.
head_of_plan() { # $1 = префикс SET-ов (может быть пустым)
    local out
    out="$(psql_c -e "${1} EXPLAIN ANALYZE ${Q};" 2>&1 || true)"
    printf '%s\n' "$out" \
        | grep -iE 'planning time|execution time|distribution|rows decoded|network usage|regions' \
        | head -6 | sed 's/^/    /' || true
}

# ФАЗА СБРОСА. Без неё второй запуск меряет не то же самое: индекс и
# статистика остаются от прошлого прогона, и фаз «до индекса» и «до
# статистики» просто не существует. Статистику отдельно удалить нечем,
# поэтому таблицы пересоздаются целиком — дороже, зато каждый прогон
# начинается из одного состояния.
if [ "${KEEP_DATA:-0}" != "1" ]; then
    log "сброс: удаляем таблицы прошлого прогона вместе с индексом и статистикой…"
    psql_c -e "DROP TABLE IF EXISTS orders;" >/dev/null
    psql_c -e "DROP TABLE IF EXISTS customers;" >/dev/null
else
    log "KEEP_DATA=1 — сброс пропущен, фазы «до индекса» и «до статистики» недостоверны"
fi

log "накатываем датасет (может занять минуты)…"
docker exec -i crdb1 cockroach sql --insecure --host=localhost:26257 < sql/04-dataset.sql >/dev/null

# orders наливается батчами: одна транзакция на 500k строк упёрлась бы в
# лимит на размер транзакции.
have="$(sql_value "SELECT count(*) FROM orders;")"
if [ "$have" -lt 500000 ]; then
    for i in $(seq 0 9); do
        from=$((i * 50000 + 1)); to=$(((i + 1) * 50000))
        psql_c -e "INSERT INTO orders (order_id, customer_id, total, created_at)
                   SELECT g, 1 + (g % 50000), (random() * 10000)::DECIMAL(12,2),
                          now() - (random() * INTERVAL '365 days')
                   FROM generate_series($from, $to) AS g
                   ON CONFLICT (order_id) DO NOTHING;" >/dev/null
        printf '\r  налито заказов: %d' "$to" >&2
    done
    printf '\n' >&2
fi

orders_n="$(sql_value "SELECT count(*) FROM orders;")"
cust_n="$(sql_value "SELECT count(*) FROM customers;")"
[ "$orders_n" -ge 500000 ] || fail "ожидали не меньше 500000 заказов, нашли: $orders_n"
log "датасет: customers=$cust_n orders=$orders_n"

echo
echo "=== A. Локальный против распределённого плана ==="
# Режимы ЧЕРЕДУЮТСЯ, а не идут блоками по пять: блоками первый режим
# греет кеши для второго, и разница между ними частично оказывается
# разницей между «холодным» и «тёплым» прогоном.
log "прогрев…"
head_of_plan "SET distsql = off;" >/dev/null
head_of_plan "SET distsql = always;" >/dev/null

log "чередуем режимы, по ${REPS} повторов каждого:"
for i in $(seq 1 "$REPS"); do
    echo "  --- повтор $i, distsql = off (local):"
    head_of_plan "SET distsql = off;"
    echo "  --- повтор $i, distsql = always (full):"
    head_of_plan "SET distsql = always;"
    echo
done

echo
echo "=== B. Статистика ==="
log "что оптимизатор знает о таблице СЕЙЧАС:"
psql_c -e "SHOW STATISTICS FOR TABLE orders;" | head -12 || true

log "план до сбора статистики (${REPS} повторов):"
for _ in $(seq 1 "$REPS"); do head_of_plan ""; echo; done

log "собираем статистику…"
psql_c -e "CREATE STATISTICS orders_fresh FROM orders;" >/dev/null
psql_c -e "CREATE STATISTICS customers_fresh FROM customers;" >/dev/null

stats_n="$(sql_value "SELECT count(*) FROM [SHOW STATISTICS FOR TABLE orders];")"
[ "$stats_n" -gt 0 ] || fail "статистика не появилась — сравнение недействительно"
log "строк статистики по orders: $stats_n"

log "план после сбора статистики (${REPS} повторов):"
for _ in $(seq 1 "$REPS"); do head_of_plan ""; echo; done

echo "--- оценка против факта в плане:"
psql_c -e "EXPLAIN ANALYZE ${Q};" | grep -iE 'estimated row count|actual row count|missing stats' | head -10 || true

echo
echo "=== C. Индекс ==="
log "есть ли сейчас полное сканирование:"
psql_c -e "EXPLAIN ${Q};" | grep -iE 'spans|table:' | head -6 || true

psql_c -e "CREATE INDEX IF NOT EXISTS orders_created_idx
           ON orders (created_at) STORING (customer_id, total);" >/dev/null

# Обычный CREATE INDEX возвращается, когда задача изменения схемы завершена
# («онлайн» — про доступность таблицы во время построения). Проверка ниже —
# страховка: сравнивать планы имеет смысл, только если индекс точно виден.
log "ждём готовности индекса…"
ready=0
for _ in $(seq 1 90); do
    n="$(sql_value "SELECT count(*) FROM [SHOW INDEXES FROM orders] WHERE index_name = 'orders_created_idx';")"
    if [ "${n:-0}" -ge 1 ]; then ready=1; break; fi
    sleep 2
done
[ "$ready" -eq 1 ] || fail "индекс orders_created_idx не появился"
log "индекс готов"

psql_c -e "CREATE STATISTICS orders_after_idx FROM orders;" >/dev/null

log "план с индексом (${REPS} повторов):"
for _ in $(seq 1 "$REPS"); do head_of_plan ""; echo; done

echo "--- какой объект читается и каким срезом:"
psql_c -e "EXPLAIN ${Q};" | grep -iE 'spans|table:' | head -6 || true

echo
echo "=== D. Устаревшая статистика ==="
#
# В разделе B статистика не изменила ни план, ни время: у запроса просто
# нет альтернатив, между которыми оптимизатору выбирать. Чтобы показать
# заявленное «устаревшая статистика уводит план», нужна статистика,
# РАСХОДЯЩАЯСЯ с данными. Создаём расхождение честно: доливаем строки,
# все в окно фильтра, и НЕ пересобираем статистику.
# Автосбор статистики надо ОТКЛЮЧИТЬ, иначе он может обновить её прямо
# во время фазы, и «устаревшая статистика» окажется свежей. Без этого
# эксперимент недетерминирован: результат зависит от того, успел ли
# фоновый сборщик отработать.
psql_c -e "ALTER TABLE orders SET (sql_stats_automatic_collection_enabled = false);" >/dev/null     || fail "не удалось отключить автосбор статистики — эксперимент был бы недетерминирован"
log "автосбор статистики на orders отключён"

before_rows="$(sql_value "SELECT count(*) FROM orders;")"
stats_before="$(sql_value "SELECT count(*) FROM [SHOW STATISTICS FOR TABLE orders];")"
log "строк статистики до долива: $stats_before"
log "оценка и факт ДО долива:"
psql_c -e "EXPLAIN ANALYZE ${Q};" \
    | grep -iE 'estimated row count|actual row count' | head -4 | sed 's/^/    /' || true

log "доливаем 300000 заказов, все за последние 30 дней…"
for i in $(seq 0 5); do
    from=$((1000000 + i * 50000 + 1)); to=$((1000000 + (i + 1) * 50000))
    psql_c -e "INSERT INTO orders (order_id, customer_id, total, created_at)
               SELECT g, 1 + (g % 50000), (random() * 10000)::DECIMAL(12,2),
                      now() - (random() * INTERVAL '30 days')
               FROM generate_series($from, $to) AS g
               ON CONFLICT (order_id) DO NOTHING;" >/dev/null
    printf '\r  долито: %d' $((to - 1000000)) >&2
done
printf '\n' >&2

after_rows="$(sql_value "SELECT count(*) FROM orders;")"
[ "$after_rows" -gt "$before_rows" ] || fail "долив не дошёл: было $before_rows, стало $after_rows"
log "строк было $before_rows, стало $after_rows"

# Проверяем, что статистика ДЕЙСТВИТЕЛЬНО не обновилась: если число
# записей выросло, автосбор всё-таки сработал и фаза недействительна.
stats_after="$(sql_value "SELECT count(*) FROM [SHOW STATISTICS FOR TABLE orders];")"
[ "$stats_after" = "$stats_before" ]     || fail "число записей статистики изменилось ($stats_before -> $stats_after) — она обновилась во время фазы, замер недействителен"
log "статистика не обновлялась: записей по-прежнему $stats_after"

log "статистика ещё СТАРАЯ — оценка против факта:"
psql_c -e "EXPLAIN ANALYZE ${Q};" \
    | grep -iE 'estimated row count|actual row count|stats collected' | head -6 | sed 's/^/    /' || true
log "план на старой статистике (${REPS} повторов):"
for _ in $(seq 1 "$REPS"); do head_of_plan ""; echo; done

log "пересобираем статистику…"
psql_c -e "CREATE STATISTICS orders_refreshed FROM orders;" >/dev/null

log "оценка против факта на свежей статистике:"
psql_c -e "EXPLAIN ANALYZE ${Q};" \
    | grep -iE 'estimated row count|actual row count|stats collected' | head -6 | sed 's/^/    /' || true
log "план на свежей статистике (${REPS} повторов):"
for _ in $(seq 1 "$REPS"); do head_of_plan ""; echo; done

psql_c -e "ALTER TABLE orders SET (sql_stats_automatic_collection_enabled = true);" >/dev/null || true

log "смотреть надо на РАСХОЖДЕНИЕ оценки с фактом, а не только на время."
log "Если план и время не изменились — это значит лишь, что в ЭТОМ прогоне"
log "цена ошибки в оценке не проявилась. Утверждать, что она нулевая для"
log "любого запроса, отсюда нельзя."

echo
echo "=== Постусловие ==="
idx="$(sql_value "SELECT count(*) FROM [SHOW INDEXES FROM orders] WHERE index_name = 'orders_created_idx';")"
[ "$idx" -ge 1 ] || fail "индекс исчез — сравнение недействительно"
log "индекс на месте; итоговый объём orders: $(sql_value "SELECT count(*) FROM orders;")"
