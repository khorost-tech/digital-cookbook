-- Периодический снимок: остаток товара на конец дня. Мера полуаддитивна.
select day, product_id, stock from {{ ref('stg_inventory') }}
