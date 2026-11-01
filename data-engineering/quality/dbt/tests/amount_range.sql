-- Сумма заказа — от 1 копейки до миллиона рублей.
{{ config(severity=var('severity')) }}
select id, amount from {{ ref('stg_orders') }} where amount < 1 or amount > 100000000
