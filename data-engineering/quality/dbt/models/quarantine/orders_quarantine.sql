select id, customer_id, status, amount, created_at from {{ ref('stg_orders_flagged') }} where not is_valid
