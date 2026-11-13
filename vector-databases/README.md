# Vector databases — примеры

Векторный поиск: алгоритмы ANN, pgvector, выделенные движки (Qdrant, Milvus, Weaviate), гибридный поиск, фильтрация и эксплуатация — на общем датасете и истине точного перебора.

| Стенд | Описание | Статья |
|---|---|---|
| [`dataset/`](dataset/) | Общий датасет серии о векторных БД: BEIR Quora (523 тыс. текстов) и FiQA, эмбеддинги self-hosted моделью multilingual-e5-small в контейнере, истина точного перебора top-100 на Go; сборка сверяется с эталонной выборкой по косинусу | [статья](https://khorost.tech/databases/vector-ann-algorithms-deep/) |
| [`ann/`](ann/) | Алгоритмы приближённого поиска на 523 тыс. эмбеддингов против точного перебора: своя HNSW, IVF-Flat, SQ8 и PQ на Go — recall@10, время, число расстояний и память по ручкам M, ef, nlist, nprobe, m; сверка с Faiss и jvector (Java); недостижимые узлы графа, нерепрезентативное обучение IVF, метрики на ненормированных векторах | [статья](https://khorost.tech/databases/vector-ann-algorithms-deep/) |
| [`pgvector/`](pgvector/) | pgvector 0.8.7 в PostgreSQL 17 на 523 тыс. эмбеддингов против истины точного перебора: HNSW и IVFFlat (recall, время, размер, maintenance_work_mem), операторы и класс индекса в EXPLAIN, фильтр WHERE при разной селективности с hnsw.iterative_scan и B-tree рядом, halfvec и битовая квантизация с пересчётом; та же выдача через JDBC (Java) | 🔜 скоро |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
