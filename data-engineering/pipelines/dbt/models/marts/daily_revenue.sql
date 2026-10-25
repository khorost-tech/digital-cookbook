-- Дневная выручка (копейки) по дате заказа. Правило — переменная rule:
-- v1 — без отменённых, v2 — ещё и без возвратов.
{% set excluded = ['cancelled'] if var('rule') == 'v1' else ['cancelled', 'refunded'] %}
select o.order_day,
       sum(case when o.status not in ('{{ excluded | join("','") }}') then i.amount else 0 end) as revenue,
       count(distinct o.order_id) as orders,
       count(i.item_id) as items
from {{ ref('stg_orders') }} o
left join {{ ref('stg_order_items') }} i on i.order_id = o.order_id
group by o.order_day
