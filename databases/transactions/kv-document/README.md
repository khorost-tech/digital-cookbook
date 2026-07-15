# KV и документные: Redis, MongoDB, ScyllaDB

Companion к статье [«KV и документные: транзакций почти нет — Redis, MongoDB, ScyllaDB»](https://khorost.tech/databases/transactions-kv-document-redis-mongo-scylla/)
(серия «Транзакции и изоляция»).

По одному выразительному примеру на систему — что там вообще значит «транзакция». Go и Java,
проверено на **Redis 8.6.1**, **MongoDB 8.2.11**, **ScyllaDB 6.2.3**.

| Система | Механизм | Что показывает |
|---------|----------|----------------|
| Redis | `WATCH` + `MULTI/EXEC` (оптимистичный CAS) | не изоляция и не rollback, а проверка версии ключа; naive GET/SET теряет обновления |
| MongoDB | multi-document transaction на replica set | атомарность одного документа — всегда; инвариант между двумя документами — только явной транзакцией |
| ScyllaDB | LWT (`IF NOT EXISTS`) на Paxos | `BATCH` — не транзакция; линеаризуемость — только через LWT в пределах раздела |

## Запуск

```bash
docker compose up -d                                        # redis :6390, mongo :27027, scylla :9052
# MongoDB — single-node replica set (нужен для multi-doc tx):
docker exec tx-mongo mongosh --quiet --eval 'rs.initiate()'
# ScyllaDB стартует ~минуту; готовность:
docker exec tx-scylla cqlsh -e "describe keyspaces"
```

### Go

```bash
cd go
go run . redis    # naive GET/SET теряет ~1500/1600; WATCH+CAS — 1600 (с retry)
go run . mongo    # 800 переводов A→B, инвариант sum=1000 сохранён
go run . scylla   # LWT: применился 1 из 16; plain INSERT: «успешны» 16 из 16
```

### Java

```bash
cd java
mvn -q compile
mvn -q exec:java -Dexec.args=redis    # Jedis WATCH-CAS: 1600 (с retry)
mvn -q exec:java -Dexec.args=mongo    # multi-doc tx: sum=1000 сохранён
mvn -q exec:java -Dexec.args=scylla   # LWT: применился 1 из 16
```

## Что где

| Путь | Назначение |
|------|-----------|
| `docker-compose.yml` | Redis 8 / MongoDB 8 (RS) / ScyllaDB 6.2 |
| `go/` | Go: go-redis / mongo-driver / gocql |
| `java/` | Java: Jedis / mongodb-driver-sync / DataStax java-driver |

## Demo-only

Локальный стенд без аутентификации, данные синтетические. Mongo — `directConnection=true` (single-node
RS в docker отдаёт клиенту внутреннее имя контейнера).

## Teardown

```bash
docker compose down
```
