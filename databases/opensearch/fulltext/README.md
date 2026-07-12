# OpenSearch: полнотекстовый поиск (запросы, анализаторы, релевантность)

Companion-демо к статье [«Полнотекстовый поиск в OpenSearch: запросы, анализаторы и подсветка результатов»](https://khorost.tech/databases/opensearch-fulltext-search/)
(серия «OpenSearch: глубокое погружение»).

Три движка на **одних демо-данных** (6 синтетических статей, ru+en) для честного сравнения
полнотекстового поиска: **OpenSearch 3.5.0**, **PostgreSQL 17.9**, **MongoDB 8.2.11**. Всё
проверено вживую — числа в статье сняты именно с этого стенда.

## Запуск

```bash
docker compose up -d                          # OpenSearch :9215, PostgreSQL :5442, MongoDB :27027
```

### OpenSearch — анализаторы, запросы, релевантность, highlight, пагинация, агрегации

```bash
cd opensearch
./queries.sh                                  # setup + все блоки подряд
./queries.sh analyze                          # только _analyze (standard vs ru_custom/en_custom)
./queries.sh relevance                        # function_score (gauss) + _explain (BM25)
```

Скрипт идемпотентен (`setup` пересоздаёт индекс `articles` и заливает `../data/articles.ndjson`).
Маппинг `opensearch/mapping.json` — кастомные анализаторы `ru_custom`/`en_custom` (стеммер + стоп-слова),
мульти-поля `.en`/`.keyword`, `tags`/`category` как `keyword`, `published_at` как `date`.

### PostgreSQL — tsvector / GIN / ts_rank

```bash
docker exec -i ft-pg psql -U postgres -d fulltext < postgres/schema.sql    # таблица + tsvector('russian') + GIN
docker exec -i ft-pg psql -U postgres -d fulltext < postgres/queries.sql   # @@ plainto_tsquery + ts_rank
```

### MongoDB — text index / $text / textScore

```bash
docker exec -i ft-mongo mongosh --quiet fulltext < mongo/load.js     # вставка + createIndex text (russian)
docker exec -i ft-mongo mongosh --quiet fulltext < mongo/queries.js  # $text + $meta:"textScore"
```

На запрос «поиск» все три движка находят один и тот же набор (`id=2`, `id=6`) в одном порядке;
абсолютные score несопоставимы (разные алгоритмы: BM25 / ts_rank / textScore).

## Demo credentials

Пароль `FtDemo#2026` (OpenSearch и PostgreSQL) и self-signed TLS (demo security дистрибутива
OpenSearch) — **только для локального стенда**. За пределами localhost сгенерируйте свой пароль и
выпустите настоящие сертификаты (см. статью серии про
[установку кластера](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)).

Демо-контент (`data/articles.ndjson`) — синтетические статьи на обобщённые темы, не из реальной
инфраструктуры.

## Teardown

```bash
docker compose down
```
