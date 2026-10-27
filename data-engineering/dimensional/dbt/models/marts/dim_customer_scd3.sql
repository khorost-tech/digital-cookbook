-- SCD3: текущий и предыдущий регион в одной строке. Запускается каждый день после
-- обновления источника: при смене региона предыдущим становится бывший текущий.
{{ config(materialized='incremental', unique_key='customer_id', incremental_strategy='delete+insert') }}
select c.id as customer_id, c.region as current_region,
{% if is_incremental() %}
       case when t.current_region is distinct from c.region then t.current_region else t.previous_region end
{% else %}
       null::text
{% endif %} as previous_region
from {{ ref('stg_customers') }} c
{% if is_incremental() %}
left join {{ this }} t on t.customer_id = c.id
{% endif %}
