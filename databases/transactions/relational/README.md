# Стенд №1: аномалии транзакций на PostgreSQL

Companion к статье [«Транзакции в реляционных БД на практике: PostgreSQL»](https://khorost.tech/databases/transactions-relational-postgres-practice/)
(серия «Транзакции и изоляция»).

Go-нагрузчик воспроизводит **lost update** и **write skew** под конкуренцией и показывает их
лечение, снимая метрики (доля аномалий, latency p50/p99, retry, throughput). Проверено на
**PostgreSQL 18.4**.

## Запуск

```bash
docker compose up -d db                                    # PostgreSQL :5440 (demo-креды)
docker exec -i tx-pg psql -U demo -d demo < schema.sql     # accounts + on_call
export DSN="postgres://demo:demo@localhost:5440/demo"
```

### Lost update — потерянное обновление на конкурентном инкременте

```bash
go run . -scenario lost-update -strategy naive        -workers 16 -iters 100  # теряет ~87%
go run . -scenario lost-update -strategy for-update   -workers 16 -iters 100  # 0 потерь (блокировка строки)
go run . -scenario lost-update -strategy atomic       -workers 16 -iters 100  # 0 потерь (UPDATE ... = x+1)
go run . -scenario lost-update -strategy serializable -workers 16 -iters 100  # 0 потерь, но лавина retry
```

### Write skew — на снимке (RR) ломается, на SERIALIZABLE нет

```bash
go run . -scenario write-skew -iso rr           -rounds 200   # 100% нарушений инварианта
go run . -scenario write-skew -iso serializable -rounds 200   # 0 нарушений (SSI откатывает одну tx)
```

## Java (JDBC)

Тот же паттерн в терминах JDBC — уровень через `Connection.setTransactionIsolation`, retry на
`SQLState 40001`:

> **Требуется JDK 21+** (`pom.xml` компилируется с `maven.compiler.release=21`). Проверьте
> `java -version` перед запуском — на JDK ниже 21 Maven упадёт на компиляции.

```bash
cd java
DSN=jdbc:postgresql://localhost:5440/demo mvn -q compile exec:java
# expected=400  naive(RC)=50 (lost 350)  serializable+retry=400
```

## Что где

| Файл | Назначение |
|------|-----------|
| `docker-compose.yml` | PostgreSQL 18 (demo-креды, порт 5440) |
| `schema.sql` | таблицы `accounts` (lost update) и `on_call` (write skew) |
| `main.go` | Go-нагрузчик: сценарии lost-update / write-skew, стратегии, метрики |
| `java/` | JDBC-пример (изоляция + retry на 40001), сборка Maven |

## Demo-only

`demo`/`demo` — только для локального стенда. Данные синтетические.

## Teardown

```bash
docker compose down
```
