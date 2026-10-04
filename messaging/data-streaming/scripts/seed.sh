#!/usr/bin/env bash
# Пишет N заказов; каждый — заказ и событие outbox в ОДНОЙ транзакции.
# Это и есть смысл outbox: событие не может разойтись с бизнес-данными.
set -euo pipefail
N="${1:-50}"
docker exec -i ds-postgres psql -U shop -d shop -v ON_ERROR_STOP=1 <<SQL
DO \$\$
DECLARE i int; oid bigint; cust bigint; amt numeric(12,2);
BEGIN
  FOR i IN 1..$N LOOP
    cust := 1 + (i % 5);
    amt  := round((random()*100 + 1)::numeric, 2);
    INSERT INTO orders (customer_id, amount) VALUES (cust, amt) RETURNING id INTO oid;
    INSERT INTO outbox (aggregatetype, aggregateid, type, payload)
    VALUES ('orders', oid::text, 'OrderCreated',
            jsonb_build_object('order_id', oid, 'customer_id', cust, 'amount', amt));
  END LOOP;
END \$\$;
SQL
echo "seeded: $N заказов + $N событий outbox"
