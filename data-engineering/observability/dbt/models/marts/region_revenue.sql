select region, sum(amount) as revenue from {{ ref('int_orders_enriched') }} where status <> 'cancelled' group by 1
