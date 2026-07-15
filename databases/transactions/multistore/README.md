# Стенд №2: один сценарий по всем хранилищам

Companion к финальной статье [«Мульти-хранилищный стенд и карта выбора»](https://khorost.tech/databases/transactions-multistore-benchmark-decision-map/)
(серия «Транзакции и изоляция»).

Один сценарий — **«списать 1 при инварианте баланс ≥ 0»** — реализован поверх PostgreSQL, Redis,
MongoDB и ScyllaDB, каждым идиоматичным атомарным механизмом, и прогнан одним Go-нагрузчиком.
Число попыток (1600) превышает начальный баланс (1000), поэтому часть списаний должна быть
отклонена. Все хранилища держат инвариант корректно (`final=0`); различается **цена корректности**.

Проверено на **PostgreSQL 18, Redis 8.6, MongoDB 8.2, ScyllaDB 6.2** — типичный прогон:

| Хранилище | Механизм | throughput | conflicts |
|-----------|----------|-----------:|----------:|
| Redis | Lua: атомарная проверка + `DECR` | ~15000/s | 0 |
| MongoDB | `findOneAndUpdate({balance:{$gt:0}}, {$inc:-1})` | ~2500/s | 0 |
| PostgreSQL | `UPDATE ... SET balance=balance-1 WHERE balance>0` | ~800/s | 0 |
| ScyllaDB | LWT compare-and-set на Paxos | ~17/s | ~10500 |

(Абсолютные числа сильно зависят от машины и сетевого пути до контейнеров — на Docker Desktop под
Windows in-memory Redis искусственно занижается port-forwarding'ом; на Linux/WSL он в разы быстрее
остальных. Важен порядок и разрыв — throughput между Redis и ScyllaDB отличается примерно в 880 раз.)

## Запуск

```bash
docker compose up -d                                      # pg :5442, redis :6391, mongo :27028, scylla :9053
docker exec ms-mongo mongosh --quiet --eval 'rs.initiate()'   # mongo — single-node RS
docker exec ms-scylla cqlsh -e "describe keyspaces"       # дождаться готовности Scylla (~минута)

cd go
go run . -store all -workers 16 -iters 100 -budget 1000
go run . -store scylla                                    # по одному хранилищу
```

## Что где

| Путь | Назначение |
|------|-----------|
| `docker-compose.yml` | PostgreSQL 18 / Redis 8 / MongoDB 8 (RS) / ScyllaDB 6.2 |
| `go/main.go` | общий нагрузчик: интерфейс `Store`, сценарий, метрики |
| `go/{pg,redis,mongo,scylla}.go` | адаптеры: атомарное «списать если баланс>0» на каждом |

## Замечание про ScyllaDB

LWT (`IF balance = ?`) на **одном горячем ключе** — анти-паттерн: Paxos-консенсус на каждую
попытку плюс лавина retry под конкуренцией (~10500 на 1600 успешных). `succeeded` считается
авторитетно из БД (`budget − final`), а не по клиентским `applied`: под жёсткой LWT-контензией
клиентский счётчик ненадёжен, истина — баланс в БД.

## Demo-only

Локальный стенд без аутентификации, данные синтетические.

## Teardown

```bash
docker compose down
```
