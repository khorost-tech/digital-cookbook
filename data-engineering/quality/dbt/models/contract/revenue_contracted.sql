select sum(amount) as revenue from {{ ref('orders_contracted') }} where status <> 'cancelled'
