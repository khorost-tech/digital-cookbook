-- Заказ с регионом клиента.
select o.order_id, o.customer_id, o.status, o.amount, o.created_at, c.region
from {{ ref('stg_orders') }} o join {{ ref('stg_customers') }} c on c.customer_id = o.customer_id
