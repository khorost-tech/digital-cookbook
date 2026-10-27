-- Факт на уровне позиции.
select i.id as item_id, i.order_id, i.product_id, o.status, o.created_at::date as order_day,
       case when o.status = 'cancelled' then 0 else i.qty * i.price end as amount
from {{ ref('stg_order_items') }} i join {{ ref('stg_orders') }} o on o.id = i.order_id
