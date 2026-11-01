-- Витрина без карантина: всё, что прошло (или не остановило) тесты staging.
select created_at::date as order_day, count(*) as orders, sum(amount) as revenue
from {{ ref('stg_orders') }} where status <> 'cancelled' group by 1
