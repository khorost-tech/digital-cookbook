select id as product_id, category from {{ ref('stg_products') }}
