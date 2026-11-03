select id as customer_id, region from {{ source('raw', 'customers') }}
