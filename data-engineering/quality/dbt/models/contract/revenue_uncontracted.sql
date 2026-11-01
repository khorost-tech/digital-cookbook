-- Витрина над моделью без контракта: здесь ломается то, что модель выше пропустила.
select sum(amount) as revenue from {{ ref('orders_uncontracted') }} where status <> 'cancelled'
