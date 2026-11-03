-- Не использует сумму: на уровне таблиц задета инцидентом в amount, на уровне колонок — нет.
select status, count(*) as orders from {{ ref('stg_orders') }} group by 1
