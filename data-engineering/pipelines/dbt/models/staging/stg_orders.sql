select id as order_id, customer_id, status, created_at, ingested_at, created_at::date as order_day
from {{ source('raw', 'orders') }}
