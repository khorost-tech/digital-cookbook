# data-access

Стенд к статье №6 (java-deep-dive): **JDBC vs JPA/Hibernate vs jOOQ** + HikariCP на
живом Postgres. Схема — классика `authors` 1:N `books`, на которой демонстрируется
N+1 и его устранение.

## Схема + сид

`SchemaInit` пересоздаёт схему при каждом запуске (`DROP`+`CREATE`, идемпотентно):

```sql
CREATE TABLE authors (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE books (
    id             BIGSERIAL PRIMARY KEY,
    author_id      BIGINT NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    title          TEXT NOT NULL,
    published_year INT
);
CREATE INDEX idx_books_author_id ON books(author_id);
```

Сид: 20 авторов, 2–6 книг на каждого (детерминированный `Random(seed=42)`) — фактически
получилось **authors=20, books=74**.

## Один и тот же доступ тремя способами

Задача: `authorName -> [список названий книг]`, один и тот же результат.

| Подход | Класс | Как получает результат |
|---|---|---|
| JDBC | `JdbcDemo` | Ручной SQL (`JOIN authors+books`) + `ResultSet`, маппинг руками |
| JPA/Hibernate | `JpaDemo#authorsWithBooks` | JPQL `JOIN FETCH` через `EntityManager`, объектная модель |
| jOOQ | `JooqDemo` | Типобезопасный DSL (`select/from/join/on`) **без codegen** |

jOOQ здесь **без кодогенерации**: codegen требовал бы живую БД на этапе
`mvn generate-sources`, что усложняет Docker-сборку без внешних зависимостей на
момент сборки. Вместо этого `Table`/`Field` объявлены вручную через `DSL.name(...)` —
это по-прежнему типобезопасный DSL (структура запроса — Java-типы, а не строковая
конкатенация), просто без проверки имён колонок компилятором, которую даёт codegen.
В реальном проекте — codegen jOOQ maven plugin поверх живой схемы.

Прогон подтверждает эквивалентность результата (`Main`, реальный вывод):

```
JDBC: 1 SQL-запрос (JOIN authors+books), authors=20
jOOQ: 1 SQL-запрос (JOIN authors+books через DSL), authors=20
Эквивалентность результатов: JDBC==jOOQ: true, jOOQ==JPA: true
```

## N+1 на JPA: воспроизведение и устранение

Счётчик запросов — `Statistics#getPrepareStatementCount()` (реальный счётчик
подготовленных JDBC statement'ов внутри Hibernate), а не разбор текстового лога.
Лог (`hibernate.show_sql`) — качественное подтверждение рядом.

**Наивный обход** (`JpaDemo#n1Demo`): список из 20 авторов, затем в цикле
обращение к лениво загруженной `author.getBooks()` — по одному `SELECT` на автора:

```
Hibernate: select a1_0.id,a1_0.name from authors a1_0 order by a1_0.id
Hibernate: select b1_0.author_id,... from books b1_0 where b1_0.author_id=?   (x20)
JPA N+1: authors=20, prepareStatementCount=21 (ожидание: 1 + 20 = 21), totalBooks=74
```

**Фикс #1 — `JOIN FETCH`** (`JpaDemo#fetchJoinDemo`, JPQL
`SELECT DISTINCT a FROM Author a JOIN FETCH a.books ORDER BY a.id`):

```
Hibernate: select distinct a1_0.id,b1_0.author_id,... from authors a1_0
           join books b1_0 on a1_0.id=b1_0.author_id order by a1_0.id
JPA JOIN FETCH: authors=20, prepareStatementCount=1 (ожидание: 1), totalBooks=74
```

**Фикс #2 — `EntityGraph`** (`JpaDemo#entityGraphDemo`, hint
`jakarta.persistence.fetchgraph`) — тот же эффект, другой API (`LEFT JOIN`
вместо `JOIN`, т.к. graph-fetch не гарантирует наличие связанных строк):

```
Hibernate: select distinct a1_0.id,b1_0.author_id,... from authors a1_0
           left join books b1_0 on a1_0.id=b1_0.author_id order by a1_0.id
JPA @EntityGraph: authors=20, prepareStatementCount=1 (ожидание: 1), totalBooks=74
```

**Итог одного прогона:**

| Вариант | SQL-запросов |
|---|---|
| Наивный обход (N+1) | **21** (1 + 20) |
| `JOIN FETCH` | **1** |
| `@EntityGraph` | **1** |

`Main` ассертит оба факта (`n1Queries == AUTHORS+1`, оба фикса `<= 2`) и падает с
`IllegalStateException`, если числа разошлись — не выдуманные, реальные счётчики
с каждого прогона.

## HikariCP: пул с метриками

`HikariManager` — общий пул для JDBC/jOOQ-демо (`maximumPoolSize=4`,
`minimumIdle=1`, специально маленький) + отдельная демонстрация под нагрузкой:
8 конкурентных воркеров, каждый держит соединение ~600мс (`pg_sleep(0.6)`).
Метрики — снимок через `HikariPoolMXBean` до/во время пика/после:

```
HikariCP: maximumPoolSize=4, minimumIdle=1, 8 конкурентных воркеров
  [до нагрузки  ] total=2 active=0 idle=2 threadsAwaitingConnection=0
  [под нагрузкой] total=4 active=4 idle=0 threadsAwaitingConnection=4
  [после        ] total=4 active=0 idle=4 threadsAwaitingConnection=0
```

Пул реально насыщается (`active=total=4`) и 4 воркера ждут в очереди
(`threadsAwaitingConnection=4`) — 8 воркеров на 4 соединения.

## Подводный камень: Hibernate 7 требует Jakarta Persistence 3.2

Parent-POM `java-deep-dive` импортирует `spring-boot-dependencies:3.5.3` (для
модуля `spring-vs-quarkus`) — эта BOM управляет `jakarta.persistence-api` версией
**3.1.0** (под Hibernate 6.x). Hibernate **7.0.2.Final** требует Jakarta
Persistence **3.2** (класс `PersistenceUnitTransactionType` и другие новые API
появились только в 3.2). Без явного оверрайда версии в `data-access/pom.xml`
падает на старте `EntityManagerFactory`:

```
NoClassDefFoundError: jakarta/persistence/PersistenceUnitTransactionType
```

Фикс — явная версия на `<dependency>` в `data-access/pom.xml` (прямая версия
всегда сильнее унаследованной `dependencyManagement`):

```xml
<dependency>
  <groupId>jakarta.persistence</groupId>
  <artifactId>jakarta.persistence-api</artifactId>
  <version>3.2.0</version>
</dependency>
```

## Версии

- Hibernate: `7.0.2.Final` (пин parent POM)
- jOOQ: `3.20.3` (пин parent POM)
- HikariCP: `7.1.0` (latest stable на Maven Central, проверено 2026-07-07)
- postgresql-driver: `42.7.4` (пин parent POM)
- jakarta.persistence-api: `3.2.0` (явный оверрайд, см. подводный камень выше)
- Postgres: `18.4` (образ из `docker/compose.yml`)

## Сборка и прогон

Хостового Maven нет — сборка только через Docker; хостовый доступ к
`localhost:5455` может быть заблокирован файрволом, поэтому прогон — тоже в
контейнере, на сети compose (`postgres:5432`, имя сервиса, а не `localhost`):

```bash
cd java-deep-dive
./data-access/run.sh
```

Делает по шагам: поднимает `postgres` из `docker/compose.yml`, собирает
`data-access.jar` (Docker Maven, JDK 25), прогоняет демо в контейнере на сети
compose, останавливает Postgres.

Вручную (то же самое):

```bash
cd java-deep-dive
docker compose -f docker/compose.yml up -d postgres && sleep 8

MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl data-access -am package -DskipTests

MSYS_NO_PATHCONV=1 docker run --rm --network docker_default \
  -e JDBC_URL="jdbc:postgresql://postgres:5432/jdd" -e JDBC_USER=jdd -e JDBC_PASSWORD=jdd \
  -v "$(pwd)/data-access/target:/app" eclipse-temurin:25-jdk \
  java -jar /app/data-access.jar

docker compose -f docker/compose.yml down
```

(`docker_default` — имя сети compose-проекта `docker` при запуске из
`java-deep-dive/`; проверить фактическое имя: `docker network ls | grep docker`.)
