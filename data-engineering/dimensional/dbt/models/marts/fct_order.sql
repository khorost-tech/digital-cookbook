-- Факт на уровне заказа: сумма заказа и ключ версии клиента на момент заказа.
-- customer_sk пуст, если на момент заказа версии клиента не было (клиент пришёл позже).
with amounts as (
  select order_id, sum(qty * price) as amount, count(*) as items
  from {{ ref('stg_order_items') }} i join (select id as order_id_ from {{ ref('stg_orders') }}) o on o.order_id_ = i.order_id
  group by order_id
)
select o.id as order_id, o.customer_id, o.created_at, o.created_at::date as order_day, o.status,
       case when o.status = 'cancelled' then 0 else a.amount end as amount, a.items,
       d.customer_sk
from {{ ref('stg_orders') }} o
join amounts a on a.order_id = o.id
left join {{ ref('dim_customer_scd2') }} d
  on d.customer_id = o.customer_id and o.created_at >= d.valid_from and o.created_at < d.valid_to
