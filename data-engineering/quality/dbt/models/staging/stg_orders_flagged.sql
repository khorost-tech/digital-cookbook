-- Те же правила, что у тестов, — построчно: is_valid решает, витрина или карантин.
-- coalesce: при NULL в статусе или сумме условие даёт NULL, а не false, и строка
-- без него не попала бы ни в витрину, ни в карантин.
select o.*,
       coalesce(o.customer_id is not null
         and c.customer_id is not null
         and o.status in ('paid', 'cancelled', 'refunded')
         and o.amount between 1 and 100000000
         and count(*) over (partition by o.id) = 1, false) as is_valid
from {{ ref('stg_orders') }} o
left join {{ ref('stg_customers') }} c on c.customer_id = o.customer_id
