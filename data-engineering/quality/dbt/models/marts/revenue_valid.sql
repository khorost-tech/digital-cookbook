select created_at::date as order_day, count(*) as orders, sum(amount) as revenue
from {{ ref('stg_orders_flagged') }} where is_valid and status <> 'cancelled' group by 1
