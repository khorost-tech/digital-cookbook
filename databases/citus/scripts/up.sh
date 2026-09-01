#!/usr/bin/env bash
#
# up.sh — единая точка входа в стенд Citus: поднять координатор + 2 воркера,
# зарегистрировать воркеры на координаторе, распределить таблицы, наполнить
# данными. Идемпотентен: повторный запуск на уже поднятом стенде не падает
# и не дублирует данные.
#
# Схема — sql/01-schema.sql (применяется PostgreSQL-образом автоматически
# из docker-entrypoint-initdb.d при первом старте координатора). Этот скрипт
# делает то, что схема сама сделать не может: регистрацию воркеров и
# create_distributed_table/create_reference_table (Citus API, не DDL).
#
# Требования: Docker (+ compose) в WSL2. Порт хоста: 15435 (координатор,
# воркеры наружу не публикуются).
#
# Запуск (из databases/citus/):
#   bash scripts/up.sh

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

COMPOSE_FILE="compose/compose.yml"
COMPOSE=(docker compose -f "$COMPOSE_FILE")
PORT=15435
DB_USER=shard
DB_NAME=shard

log()  { echo "[up] $*" >&2; }
fail() { echo "[up] ОШИБКА: $*" >&2; exit 1; }

# --- Preflight -------------------------------------------------------------

command -v docker >/dev/null 2>&1 || fail "docker не найден в PATH. Стенд поднимается только через WSL2 (см. README/бриф)."
docker info >/dev/null 2>&1 || fail "docker недоступен (демон не отвечает). Проверьте, что Docker Desktop/движок запущен в WSL2."

# Порт 15435 должен быть либо свободен, либо занят нашим же координатором.
occupant="$(docker ps --filter "publish=${PORT}" --format '{{.Names}}' 2>/dev/null || true)"
if [ -n "$occupant" ] && [ "$occupant" != "citus-coord" ]; then
  fail "порт ${PORT} уже занят чужим контейнером '${occupant}'. Освободите порт или остановите этот контейнер."
fi
if [ -z "$occupant" ] && command -v ss >/dev/null 2>&1; then
  if ss -ltn 2>/dev/null | grep -q ":${PORT} "; then
    fail "порт ${PORT} занят процессом вне Docker. Освободите порт перед запуском стенда."
  fi
fi

# --- Поднимаем контейнеры ---------------------------------------------------

log "docker compose up -d (coordinator, worker1, worker2)…"
"${COMPOSE[@]}" up -d coordinator worker1 worker2

wait_healthy() { # $1 = имя контейнера, $2 = попыток (по 2с)
  local name="$1" tries="${2:-60}" st
  for _ in $(seq 1 "$tries"); do
    st="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null || echo missing)"
    if [ "$st" = healthy ]; then
      log "$name: healthy"
      return 0
    fi
    if [ "$st" = missing ]; then
      fail "контейнер $name не создан — docker compose up не прошёл (см. docker compose -f $COMPOSE_FILE logs $name)"
    fi
    sleep 2
  done
  fail "контейнер $name не стал healthy за отведённое время (см. docker compose -f $COMPOSE_FILE logs $name)"
}

wait_healthy citus-coord 60
wait_healthy citus-w1 60
wait_healthy citus-w2 60

psql_c() { # выполняет SQL на координаторе, падает при ошибке
  docker exec -i citus-coord psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}

# --- Регистрация воркеров ---------------------------------------------------
#
# ФАКТ (проверено api-probe.sh на живом Citus 14.1.0-pg17): актуальная
# функция — citus_add_node. Она идемпотентна: повторный вызов на уже
# зарегистрированном узле не падает, а возвращает существующий nodeid.

log "Регистрируем воркеров на координаторе (citus_add_node)…"
psql_c -c "SELECT citus_add_node('citus-w1', 5432);" >/dev/null || fail "не удалось зарегистрировать citus-w1"
psql_c -c "SELECT citus_add_node('citus-w2', 5432);" >/dev/null || fail "не удалось зарегистрировать citus-w2"

registered="$(psql_c -At -c "SELECT count(*) FROM pg_dist_node WHERE isactive;")"
[ "$registered" -ge 2 ] || fail "после регистрации в pg_dist_node меньше 2 активных узлов (нашли: $registered)"
log "Воркеры зарегистрированы: $registered активных узлов в pg_dist_node."

# --- Распределение таблиц ---------------------------------------------------
#
# customers и orders — по customer_id, с колокацией (типичный запрос на
# покупателя не уходит с одного шарда). shipments — по order_id, ЯВНО БЕЗ
# колокации с customers/orders (colocate_with => 'none'): иначе Citus
# автоматически колоцировал бы её с группой customers/orders, потому что
# у обеих групп совпадают тип и число шардов (32 по умолчанию) — совпадение,
# которое смазало бы демонстрацию "разные ключи шардирования = repartition
# join" в статье. carriers — референсная таблица (копия на каждом воркере).

log "Распределяем таблицы (create_distributed_table/create_reference_table, идемпотентно)…"
psql_c <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_dist_partition WHERE logicalrelid = 'customers'::regclass) THEN
    PERFORM create_distributed_table('customers', 'customer_id');
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_dist_partition WHERE logicalrelid = 'orders'::regclass) THEN
    PERFORM create_distributed_table('orders', 'customer_id', colocate_with => 'customers');
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_dist_partition WHERE logicalrelid = 'shipments'::regclass) THEN
    PERFORM create_distributed_table('shipments', 'order_id', colocate_with => 'none');
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_dist_partition WHERE logicalrelid = 'carriers'::regclass) THEN
    PERFORM create_reference_table('carriers');
  END IF;
END $$;
SQL

dist_count="$(psql_c -At -c "SELECT count(*) FROM citus_tables WHERE table_name::text IN ('customers','orders','shipments','carriers');")"
[ "$dist_count" = 4 ] || fail "ожидали 4 распределённые/референсные таблицы в citus_tables, нашли: $dist_count"

# --- Наполнение --------------------------------------------------------------
#
# 200 покупателей × ~20 заказов = 4000 заказов, 4000 отгрузок (по одной на
# заказ), 5 перевозчиков. Объём подобран так, чтобы типовой запрос "заказы
# одного покупателя" шёл единицы-десятки миллисекунд (проверено вживую:
# 9-15 мс на одношардовый join customers+orders), а не микросекунды — иначе
# следующие артефакты будут мерить шум, а не разницу локального/сетевого
# запроса. ON CONFLICT DO NOTHING делает наполнение идемпотентным.

log "Наполняем данными (идемпотентно, ON CONFLICT DO NOTHING)…"
psql_c <<'SQL'
INSERT INTO carriers (carrier, country)
SELECT c, co FROM (VALUES
  ('DHL','DE'), ('FedEx','US'), ('CDEK','RU'), ('Post','RU'), ('UPS','US')
) AS t(c, co)
ON CONFLICT (carrier) DO NOTHING;

INSERT INTO customers (customer_id, name, city)
SELECT i, 'customer_' || i,
       (ARRAY['Moscow','SPb','Kazan','Novosibirsk','Ekaterinburg'])[1 + (i % 5)]
FROM generate_series(1, 200) AS i
ON CONFLICT (customer_id) DO NOTHING;

INSERT INTO orders (customer_id, order_id, total, created_at)
SELECT c.i, c.i * 1000 + o.j,
       (random() * 10000)::numeric(12,2),
       now() - (random() * interval '365 days')
FROM generate_series(1, 200) AS c(i), generate_series(1, 20) AS o(j)
ON CONFLICT (customer_id, order_id) DO NOTHING;

-- shipment_id = order_id: синтетический 1:1 демо-датасет, каждому заказу
-- одна отгрузка. order_id уже уникален глобально (customer_id*1000+j).
INSERT INTO shipments (shipment_id, order_id, carrier)
SELECT o.order_id, o.order_id,
       (ARRAY['DHL','FedEx','CDEK','Post','UPS'])[1 + (o.order_id % 5)]
FROM orders o
ON CONFLICT (shipment_id, order_id) DO NOTHING;
SQL

customers_n="$(psql_c -At -c "SELECT count(*) FROM customers;")"
orders_n="$(psql_c -At -c "SELECT count(*) FROM orders;")"
shipments_n="$(psql_c -At -c "SELECT count(*) FROM shipments;")"
carriers_n="$(psql_c -At -c "SELECT count(*) FROM carriers;")"

[ "$customers_n" -ge 200 ]  || fail "ожидали не меньше 200 строк в customers, нашли: $customers_n"
[ "$orders_n" -ge 4000 ]    || fail "ожидали не меньше 4000 строк в orders, нашли: $orders_n"
[ "$shipments_n" -ge 4000 ] || fail "ожидали не меньше 4000 строк в shipments, нашли: $shipments_n"
[ "$carriers_n" -ge 5 ]     || fail "ожидали не меньше 5 строк в carriers, нашли: $carriers_n"

log "Готово: customers=$customers_n orders=$orders_n shipments=$shipments_n carriers=$carriers_n"
log "Координатор: postgresql://${DB_USER}:${DB_USER}@localhost:${PORT}/${DB_NAME}"
log "Дальше: bash scripts/api-probe.sh — зафиксировать фактический API Citus."
