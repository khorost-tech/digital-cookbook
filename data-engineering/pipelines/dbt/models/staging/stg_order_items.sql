select id as item_id, order_id, product_id, qty, price, qty * price as amount
from {{ source('raw', 'order_items') }}
