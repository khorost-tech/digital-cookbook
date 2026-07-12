-- Те же демо-статьи, что и в OpenSearch/MongoDB — для честного сравнения FTS.
-- Полнотекст: сгенерированный tsvector-столбец (конфигурация 'russian') + GIN-индекс.
DROP TABLE IF EXISTS articles;
CREATE TABLE articles (
    id           int PRIMARY KEY,
    title        text NOT NULL,
    body         text NOT NULL,
    tags         text[]      NOT NULL,
    category     text        NOT NULL,
    author       text        NOT NULL,
    published_at date        NOT NULL,
    fts tsvector GENERATED ALWAYS AS (
        to_tsvector('russian', coalesce(title,'') || ' ' || coalesce(body,''))
    ) STORED
);
CREATE INDEX articles_fts_gin ON articles USING gin (fts);

INSERT INTO articles (id, title, body, tags, category, author, published_at) VALUES
(1, 'Массовая загрузка данных через bulk API',
    'Чтобы быстро загрузить большие объёмы документов в OpenSearch, используют bulk API вместо одиночных запросов. Пакетная запись снижает накладные расходы на сеть и увеличивает пропускную способность в десятки раз. Важно контролировать размер пачки и обрабатывать частичные ошибки.',
    '{opensearch,bulk,ingestion}', 'databases', 'Иван Петров', '2026-01-15'),
(2, 'Полнотекстовый поиск и релевантность',
    'Полнотекстовый поиск находит документы по словам и их формам, а не по точному совпадению строки. Релевантность результатов вычисляется по алгоритму BM25 с учётом частоты термов. Правильный анализатор для языка критично влияет на качество поиска.',
    '{opensearch,search,relevance}', 'search', 'Мария Соколова', '2026-03-10'),
(3, 'Репликация и отказоустойчивость кластера',
    'Реплики шардов защищают данные от потери при отказе узла и распределяют нагрузку на чтение. Кластер OpenSearch автоматически перераспределяет шарды, когда узел выходит из строя. Число реплик настраивается на уровне индекса.',
    '{opensearch,replication,cluster}', 'operations', 'Иван Петров', '2026-02-20'),
(4, 'Vector search with k-NN and embeddings',
    'Semantic search represents text as dense vectors and finds neighbours by distance rather than keywords. The k-NN plugin in OpenSearch indexes embeddings and answers approximate nearest neighbour queries. Hybrid search combines lexical relevance with vector similarity for better recall.',
    '{opensearch,knn,ai}', 'search', 'John Smith', '2026-04-05'),
(5, 'Index mappings and analyzers',
    'A mapping defines how each field is stored and analysed before it goes into the inverted index. Text fields run through an analyzer that lowercases, tokenizes and stems the input. Keyword fields are stored verbatim for exact matching, sorting and aggregations.',
    '{opensearch,mappings,analyzers}', 'databases', 'John Smith', '2026-05-12'),
(6, 'Мониторинг и наблюдаемость кластера',
    'Наблюдаемость кластера строится на метриках, логах и трассировках, собираемых через OpenTelemetry. Дашборды показывают загрузку узлов, задержку запросов и состояние шардов в реальном времени. Своевременные алерты помогают заметить деградацию поиска до жалоб пользователей.',
    '{opensearch,monitoring,observability}', 'operations', 'Мария Соколова', '2026-06-01');
