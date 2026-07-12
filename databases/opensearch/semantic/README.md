# OpenSearch: семантический поиск (k-NN, эмбеддинги, гибрид)

Companion-демо к статье [«Семантический поиск в OpenSearch: k-NN, эмбеддинги и гибридный поиск»](https://khorost.tech/databases/opensearch-semantic-search/)
(финал серии «OpenSearch: глубокое погружение»).

Один стенд — **OpenSearch 3.5.0** с плагинами k-NN / ml-commons / neural-search (уже в образе) — показывает
оба пути генерации эмбеддингов и гибридный поиск на **6 синтетических статьях** (ru+en, те же темы, что в
демо #4). Всё проверено вживую: числа и выдачи в статье сняты именно с этого стенда.

Модель эмбеддингов — **`paraphrase-multilingual-MiniLM-L12-v2`** (384-dim, мультиязычная, включая русский).
Одноязычная англо-модель `all-MiniLM-L6-v2` на русском тексте не различала документы — под русский контент
нужна мультиязычная модель.

## Запуск

```bash
docker compose up -d          # OpenSearch :9223 (plain HTTP, security выкл.) + Python-контейнер sem-py
```

### Путь 1 — внешняя генерация (Python sentence-transformers → сырой knn)

```bash
./setup.sh load                                    # knn_vector-индекс articles-knn + демо-данные (без векторов)
docker exec -it sem-py pip install -q sentence-transformers
docker exec -it sem-py python embed.py             # считает эмбеддинги, пишет в поле embedding, knn-запрос
```

`embed.py` эмбеддит документы и поисковый запрос одной моделью, затем делает `knn`-запрос. Запрос
«как быстрее добавлять документы пачками» лексический `match` уводит НЕ в ту статью (цепляется за слово
«документы»), а семантика по смыслу ставит первой «Массовая загрузка данных через bulk API».

### Путь 2 — ml-commons (register/deploy + ingest-pipeline + neural-запрос)

```bash
./setup.sh mlcommons                               # register+deploy модели, ingest-pipeline, индекс articles-neural
                                                   # печатает MODEL_ID=<id> в конце
./setup.sh hybrid_pipeline                          # search-pipeline hybrid-pipeline (нормализация min_max + веса 0.3/0.7)
MODEL_ID=<id> ./queries.sh                          # match-промах / neural / pure-semantic / hybrid
```

Модель качает сам движок OpenSearch. Если прямого интернета нет — пробросьте прокси через JVM-опции
в `docker-compose.yml` (закомментированный вариант `OPENSEARCH_JAVA_OPTS` с `-Dhttp.proxyHost=...`).

`queries.sh` показывает контраст на точном термине **BM25**: чистая семантика «размазывает» выдачу,
а гибрид (лексика + семантика через `hybrid`-запрос и search-pipeline) ставит точное совпадение первым.

## Что где

| Файл | Назначение |
|------|-----------|
| `docker-compose.yml` | OpenSearch 3.5.0 (knn/ml/neural, heap 2g, security выкл.) + Python 3.12 |
| `data/articles.ndjson` | 6 синтетических статей (ru+en) — общие темы OpenSearch |
| `setup.sh` | `knn_vector`-индекс + данные; ml-commons register/deploy + ingest-pipeline; hybrid search-pipeline |
| `embed.py` | внешняя генерация эмбеддингов (sentence-transformers) + knn-запрос |
| `queries.sh` | match-промах / neural / pure-semantic / hybrid (контраст на точном термине) |

## Demo-only

Security **отключён** (`DISABLE_SECURITY_PLUGIN=true`, plain HTTP) — стенд про семантику, а не про TLS
(TLS/security — тема [#1 серии](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)). За
пределами localhost так не запускают: включите security, TLS и аутентификацию.

Прокси-хост в `docker-compose.yml` — плейсхолдер `<proxy-host>`, подставьте свой. Демо-контент
(`data/articles.ndjson`) — синтетические статьи на обобщённые темы, не из реальной инфраструктуры.

## Teardown

```bash
docker compose down
```
