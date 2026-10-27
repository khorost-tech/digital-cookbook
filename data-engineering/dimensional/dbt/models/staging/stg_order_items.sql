select * from {{ source('app', 'order_items') }}
