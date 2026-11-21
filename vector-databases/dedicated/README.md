# vector-databases/dedicated — Qdrant, Milvus, Weaviate на одном корпусе

Стенд к статье [Qdrant, Milvus, Weaviate](https://khorost.tech/databases/vector-dedicated-engines/).
Корпус и истина — стенд `vector-databases/dataset` (BEIR Quora, 522 931
документ, e5-small, 384 измерения). Метка для фильтров — как в стенде
`vector-databases/pgvector`: `bucket = (номер · 2654435761) mod 1000`.

| движок | образ | запуск |
|---|---|---|
| Qdrant | `qdrant/qdrant:v1.19.2` | один контейнер |
| Milvus | `milvusdb/milvus:v2.6.25` | standalone, встроенный etcd, локальное хранилище |
| Weaviate | `semitechnologies/weaviate:1.39.11` | один узел, синхронная индексация |

Образы — по дайджесту (`images.json`). Движки поднимаются по одному, с
одинаковым лимитом: 5 ГБ памяти и 8 ЦП. HNSW у всех — M = 16,
ef_construction = 200, скалярное произведение (как в стендах `ann` и
`pgvector`).

Ось — движок. В каждой (`scripts/engine_axis.py`):

| вариант | что меряет |
|---|---|
| `load` | вставка и индексация всего корпуса, память (anon и страничный кеш) через 30 с после |
| `hnsw` | recall@10 и время от ef |
| `exact` | точный поиск против истины — контроль стенда (Qdrant) |
| `filter` | `bucket < b` при 50%, 10%, 1%, 0,1% против точного перебора по срезу |
| `filter-sweeping` | то же со стратегией фильтра `sweeping` (Weaviate) |
| `java` | выдача 200 запросов официальным Java-клиентом против Go-клиента |
| `quant` | скалярная квантизация int8: только коды и пересчёт (Qdrant) |
| `fresh` | 90% корпуса, затем вставка 10% и сразу поиск новыми документами |
| `fresh-strong` | то же со Strong-согласованностью (Milvus) |

## Запуск

```bash
(cd ../dataset && python3 scripts/make-dataset.py)   # один раз
bash scripts/run-all.sh
```

Одна ось — `python3 scripts/run-qdrant.py`. Замеры делает `ebench` (Go: клиенты
qdrant/go-client 1.19.4, milvus client/v2 2.6.5, weaviate-go-client 5.7.3);
Java — `java/` (io.qdrant:client 1.19.0, milvus-sdk-java 2.6.25,
io.weaviate:client 5.5.0).

Гипотезы — в `expected.json`, записаны до прогона, ревизии после сессий — там
же. Проверки:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/sabotage.py
```
