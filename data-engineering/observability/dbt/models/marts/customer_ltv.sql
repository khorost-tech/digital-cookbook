select customer_id, sum(amount) as ltv from {{ ref('int_orders_enriched') }} where status <> 'cancelled' group by 1
