{# SCD2: каждая смена региона — новая версия строки с периодом действия.
   Время версии — updated_at источника, а не момент запуска снапшота. #}
{% snapshot customers_snapshot %}
{{ config(target_schema='snapshots', unique_key='id', strategy='timestamp', updated_at='updated_at') }}
select * from {{ source('app', 'customers') }}
{% endsnapshot %}
