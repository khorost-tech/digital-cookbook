# vector-databases/pgvector — векторный поиск внутри PostgreSQL

Стенд к статье [pgvector вглубь](https://khorost.tech/databases/vector-pgvector-deep/).
PostgreSQL 17 + pgvector 0.8.7 (образ по дайджесту), корпус и истина — стенд
`vector-databases/dataset` (BEIR Quora, 522 931 документ, e5-small, 384
измерения). Таблица:

```sql
CREATE TABLE docs (id int PRIMARY KEY, bucket int NOT NULL,
                   embedding vector(384) NOT NULL, embedding_h halfvec(384) NOT NULL);
```

`id` — номер строки корпуса (как в истине), `bucket` — синтетическая метка
0…999 для фильтров: `(id · 2654435761) mod 1000`, без связи со смыслом.

| ось | что меряет |
|---|---|
| `exact` | точный перебор в SQL против истины датасета (контроль) |
| `hnsw` | HNSW m = 16, ef_construction = 200: построение, размер, recall от `hnsw.ef_search`; `maintenance_work_mem` 2 ГБ и 64 МБ |
| `ivfflat` | IVFFlat lists = 723, recall от `ivfflat.probes` |
| `ops` | индекс `vector_ip_ops` и операторы `<->`, `<=>`, `<#>`: план и время |
| `filter` | `WHERE bucket < b` при 50%, 10%, 1%, 0,1%: `hnsw.iterative_scan`, B-tree рядом с HNSW |
| `quant` | HNSW по `halfvec` и по `binary_quantize` с пересчётом |
| `java` | те же запросы через JDBC + pgvector-java: выдача id в id |

## Запуск

```bash
(cd ../dataset && python3 scripts/make-dataset.py)   # один раз
bash scripts/run-all.sh
```

Одна ось — `python3 scripts/run-filter.py`. Замеры делает `vbench` (Go, pgx +
pgvector-go) в контейнере в сети compose; запросы идут по одному на одном
соединении. Истина для фильтров — точный перебор по срезу корпуса.

Гипотезы — в `expected.json`, записаны до прогона. Проверки:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/sabotage.py
```
