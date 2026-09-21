# databases/graph

Живой стенд к статье [Графовые базы данных и Go: где они действительно полезны](https://khorost.tech/databases/graph-databases-and-go/).

Один и тот же граф платформы грузится в **три угла** и опрашивается одними и теми же
графовыми вопросами. Смысл стенда — показать не «граф всегда быстрее», а **карту решений**:
где нативная графовая БД окупается, а где хватает PostgreSQL.

- **Neo4j** — нативная графовая БД (Cypher, bolt). «Как задумано».
- **Apache AGE** — openCypher-расширение поверх PostgreSQL. «Граф поверх PG».
- **PostgreSQL baseline** — тот же граф в реляционных таблицах, обход через `WITH RECURSIVE`.
  «Честный SQL-only».

AGE и baseline живут в одной инстанции PostgreSQL, поэтому клиентам нужен не отдельный
драйвер под каждый движок, а два семейства: нативный Neo4j + обычный PG-драйвер (он же для
AGE, он же для baseline).

## Что внутри

```
graph/
  docker-compose.yml        # neo4j 5.26 + postgres (apache/age 1.6, PG16)
  dataset/                  # детерминированный генератор графа (Go)
    schema.sql              # реляционные таблицы для baseline
    generate.go, ...        # генератор → relational.sql / neo4j.cypher / age.sql
  clients/
    go/     neo4j-go-driver + pgx     (Neo4j + AGE + baseline)
    java/   neo4j-java-driver + JDBC  (Neo4j + AGE + baseline)
    rust/   tokio-postgres            (AGE + baseline; без нативного Neo4j)
  bench/
    go/       канонический замерочный harness → out/results.csv
    scenarios.md            # что и почему мерим
```

Пять графовых вопросов (одинаковый контракт во всех клиентах): доступные ресурсы (access
graph), impact-анализ по зависимостям, сервисы на цикле, кратчайшее расстояние по
сотрудничеству, кольца заданной длины (fraud). Каждый клиент подтверждает, что три угла
дают **один и тот же ответ**; bench показывает, за сколько.

## Требования

- Docker + Docker Compose v2. Свободная подсеть `172.30.0.0/16`.
- Для локальной сборки/прогона клиентов вне контейнеров: Go ≥ 1.24, JDK 21 + Maven, Rust
  (cargo). Клиенты можно и не собирать локально — они запускаются как профили compose.

Версии зафиксированы и сверены на актуальность (июль 2026): Neo4j `5.26-community`,
Apache AGE `1.6.0` на PostgreSQL 16, neo4j-go-driver `5.28`, pgx `5.7`, neo4j-java-driver
`5.28`, PostgreSQL JDBC `42.7`, tokio-postgres `0.7`.

## Запуск (repeatable runbook)

### 1. Поднять базы

```bash
docker compose up -d neo4j postgres
```

### 2. Сгенерировать датасет

```bash
cd dataset
go run . --seed 42 --out ./out          # дефолт: 2000 пользователей, ~5.5k рёбер collaborates
cd ..
```

### 3. Загрузить граф во все три угла

```bash
# baseline (реляционные таблицы) + AGE — в один PostgreSQL
cat dataset/schema.sql        | docker compose exec -T postgres psql -U graph -d graphlab -q
cat dataset/out/relational.sql | docker compose exec -T postgres psql -U graph -d graphlab -q
cat dataset/out/age.sql        | docker compose exec -T postgres psql -U graph -d graphlab -q
# Neo4j
cat dataset/out/neo4j.cypher   | docker compose exec -T neo4j cypher-shell -u neo4j -p graphlab-pass
```

### 4. Прогнать клиентов (каждый проверяет согласие трёх углов)

```bash
docker compose --profile go   up --build   # Go   (Neo4j + AGE + baseline)
docker compose --profile java up --build   # Java (Neo4j + AGE + baseline)
docker compose --profile rust up --build   # Rust (AGE + baseline)
```

`GOPROXY` для сборки Go/бенча можно передать через окружение
(`GOPROXY=... docker compose --profile go up --build`).

### 5. Замеры (числа-эталон)

Bench запускается с хоста, чтобы измерять сеть, а не накладные контейнера:

```bash
cd bench/go
go run .                      # печатает таблицу и пишет out/results.csv
cd ../..
```

Оси и трактовка — в [bench/scenarios.md](bench/scenarios.md). Ось объёма снимается повторным
прогоном на большем масштабе (см. там же).

### Остановить

```bash
docker compose down           # + флаг -v, чтобы снести данные
```

## Тесты клиентов

Интеграционные тесты грузят крошечную фикстуру с руками посчитанными ответами и проверяют,
что все углы согласны. **Тесты сбрасывают содержимое хранилищ** — запускать на стенде.

```bash
cd clients/go   && go test ./...        # neo4j + age + baseline
cd clients/java && mvn test             # то же на JVM
cd clients/rust && cargo test           # age + baseline
```
