# ORM-стенд: Go(pgx) + Java(Hibernate) против PostgreSQL

Companion к статье 5 серии «Индексы в базах данных» (демонстрация ORM-эффектов). Использует тот
же PG-стенд, что и `db-indexes/postgres/` — таблицу `events` (2M строк) с индексом
`idx_events_user` на `user_id`.

Показывает три эффекта, которые ORM создаёт (или скрывает) на ровном месте:

| Эффект | Демонстрация |
|--------|--------------|
| N+1 | список из 20 `user_id` → по каждому отдельный запрос агрегата вместо одного `IN`/`ANY` |
| Каст ломает индекс | `user_id::text = '555'` вместо `user_id = 555` — планировщик уходит в Seq Scan по 2M строк |
| План из кода | `EXPLAIN` через сам драйвер (Go: `pool.Query(ctx, "EXPLAIN ...")`), а не только через `psql` |

## Запуск

```bash
cd ../postgres && docker compose up -d
./run.sh sql/00-schema.sql >/dev/null   # схема + 2M строк
./run.sh sql/01-scan.sql >/dev/null     # создаёт idx_events_user (+ покрывающий idx_events_user_cov)
```

### Go (pgx)

```bash
cd ../orm/go
go get github.com/jackc/pgx/v5@latest && go mod tidy
go run .
```

По умолчанию подключается к `localhost:5433` (хостовый порт стенда). Если гоняете код в
контейнере на docker-сети стенда (`postgres_default`), переопределите строку подключения:

```bash
docker run --rm --network postgres_default \
  -e DATABASE_URL="postgres://postgres:idxdemo@postgres:5432/idxdemo" \
  -v "$PWD:/app" -w /app golang:1.26-alpine go run .
```

Ожидаемый вывод (реальный прогон, см. отчёт):

```
=== (1) N+1 vs батч ===
N+1: выполнено 20 отдельных запросов (по одному на user_id)
Батч: 1 запрос, total=357 (сумма по тем же 20 user_id)

=== (2) план с кастом user_id::text = '555' (ломает индекс) ===
Seq Scan on events  (cost=0.00..60847.00 rows=10000 width=70)
  Filter: ((user_id)::text = '555'::text)

=== (3) для сравнения: план без каста user_id = 555 (использует индекс) ===
Index Scan using idx_events_user_cov on events  (cost=0.43..24.99 rows=21 width=70)
  Index Cond: (user_id = 555)
```

### Java (Hibernate/JPA)

```bash
export JAVA_HOME="C:\Program Files\Java\jdk-21.0.11"
export PATH="/c/Users/ak/tools/apache-maven-3.9.9/bin:$PATH"
cd ../orm/java
mvn -q compile exec:java
```

`persistence.xml` по умолчанию указывает на `localhost:5433`; `JDBC_URL` переопределяет URL
(нужно при запуске в контейнере на сети стенда, аналогично Go). `hibernate.show_sql` +
`hibernate.format_sql` включены — в логе виден каждый сгенерированный SQL.

Ожидаемый вывод: сначала N+1 — 1 запрос за список `user_id` + 20 одинаковых
`select count(e1_0.id) from events e1_0 where e1_0.user_id=?`, затем один
`select e1_0.user_id, count(e1_0.id) from events e1_0 where e1_0.user_id in (...) group by e1_0.user_id`
с тем же итоговым `total`.

## Что где

| Путь | Назначение |
|------|-----------|
| `go/main.go` | pgx: N+1 vs `ANY($1)`, план каста через `EXPLAIN` из кода |
| `java/pom.xml`, `java/src/main/java/{Event,Main}.java` | Hibernate/JPA: `@Entity Event` (read-only, таблица уже существует), N+1 vs JPQL `IN`+`GROUP BY` |
| `java/src/main/resources/META-INF/persistence.xml` | `show_sql`/`format_sql`, `hbm2ddl.auto=none` |

## Demo-only

Стенд только читает существующую таблицу `events` из `db-indexes/postgres` (не создаёт и не
удаляет данные). Пароль/DSN — только для локального demo-стенда, не для прод.

## Teardown

```bash
cd ../postgres && docker compose down
```
