select id, customer_id, status, amount, created_at from {{ source('raw', 'orders') }}
