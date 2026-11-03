select id as order_id, customer_id, status, amount, created_at from {{ source('raw', 'orders') }}
