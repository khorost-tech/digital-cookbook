-- Первая версия флага (до исправления): при NULL в статусе или сумме условие даёт NULL,
-- и строка не проходит ни «where is_valid», ни «where not is_valid». Оставлена для оси 2.
select o.*,
       o.customer_id is not null
       and c.customer_id is not null
       and o.status in ('paid', 'cancelled', 'refunded')
       and o.amount between 1 and 100000000
       and count(*) over (partition by o.id) = 1 as is_valid
from {{ ref('stg_orders') }} o
left join {{ ref('stg_customers') }} c on c.customer_id = o.customer_id
