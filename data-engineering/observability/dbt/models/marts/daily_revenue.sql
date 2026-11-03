select created_at::date as order_day, sum(amount) as revenue from {{ ref('stg_orders') }}
where status <> 'cancelled' group by 1
