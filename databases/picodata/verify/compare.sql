-- Эталонный топ категории из источника истины.
--
-- В источнике категория лежит в JSONB-поле attrs, а просмотры считаются по
-- отдельной таблице views — плоскую форму (category, views) собирает этот
-- запрос. Порядок сортировки обязан совпадать с тем, что делает плагин:
-- views по убыванию, тай-брейк по убыванию id. Без явного тай-брейка строки с
-- одинаковым числом просмотров встали бы в произвольном порядке, и сверка
-- ловила бы несуществующее расхождение.
SELECT p.id, p.title, COALESCE(v.views, 0) AS views
FROM products p
LEFT JOIN (
    SELECT product_id, count(*) AS views FROM views GROUP BY product_id
) v ON v.product_id = p.id
WHERE p.attrs->>'category' = :'category'
ORDER BY views DESC, p.id DESC
LIMIT :lim;
