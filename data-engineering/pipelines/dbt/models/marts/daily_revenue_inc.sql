-- Та же дневная выручка, но инкрементально: при каждом запуске пересчитываются
-- только дни не раньше последнего посчитанного минус lookback_days.
-- lookback_days = 0 — пересчёт начиная с последнего дня; поздние строки за
-- более ранние дни в витрину не попадут.
{{ config(materialized='incremental', unique_key='order_day', incremental_strategy='delete+insert') }}
{% set excluded = ['cancelled'] if var('rule') == 'v1' else ['cancelled', 'refunded'] %}
select o.order_day,
       sum(case when o.status not in ('{{ excluded | join("','") }}') then i.amount else 0 end) as revenue,
       count(distinct o.order_id) as orders,
       count(i.item_id) as items
from {{ ref('stg_orders') }} o
left join {{ ref('stg_order_items') }} i on i.order_id = o.order_id
{% if is_incremental() %}
where o.order_day >= (select max(order_day) from {{ this }}) - {{ var('lookback_days', 0) }}
{% endif %}
group by o.order_day
