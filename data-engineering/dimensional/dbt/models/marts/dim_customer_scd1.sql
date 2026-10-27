-- SCD1: только текущее состояние; смена региона перезаписывает прошлое.
select id as customer_id, name, region from {{ ref('stg_customers') }}
