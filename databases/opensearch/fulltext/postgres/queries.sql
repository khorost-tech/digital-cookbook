-- Эквивалент OpenSearch match(body:"поиск"): полнотекстовый поиск + ранжирование.
-- plainto_tsquery('russian', ...) стеммит запрос той же конфигурацией, что и столбец fts.
SELECT id,
       title,
       round(ts_rank(fts, plainto_tsquery('russian', 'поиск'))::numeric, 4) AS rank
FROM   articles
WHERE  fts @@ plainto_tsquery('russian', 'поиск')
ORDER  BY rank DESC;

-- Как выглядит запрос после стемминга (для сравнения с _analyze OpenSearch):
SELECT plainto_tsquery('russian', 'поиск по большим объёмам логов') AS tsquery;
