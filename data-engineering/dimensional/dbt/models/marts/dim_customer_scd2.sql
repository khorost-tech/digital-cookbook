-- SCD2: версия клиента с периодом действия; customer_sk — суррогатный ключ версии.
select dbt_scd_id as customer_sk, id as customer_id, name, region,
       dbt_valid_from as valid_from, coalesce(dbt_valid_to, '9999-12-31'::timestamp) as valid_to
from {{ ref('customers_snapshot') }}
