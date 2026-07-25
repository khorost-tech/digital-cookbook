# testing

Стенд к статье №10 (java-deep-dive): **JUnit 5 unit-тесты vs Testcontainers
integration-тесты** на одном и том же `OrderService`. Смысл — не «как писать
тесты», а контраст двух уровней по цене и по тому, что каждый реально проверяет.

## Что демонстрирует

Один сервис `OrderService.createOrder(name, amountCents)` с простым бизнес-правилом
(`REVIEW_THRESHOLD_CENTS = 10_000` — заказы от 100.00 и выше уходят в `REVIEW`,
остальные — в `PENDING`) тестируется двумя способами:

| Уровень | Тесты | Репозиторий | Что проверяет |
|---|---|---|---|
| Unit | 4 (`OrderServiceUnitTest`) | `InMemoryOrderRepository` (ручной stub, 3 теста) + `mock(OrderRepository)` Mockito (1 тест) | бизнес-логику `OrderService` в изоляции, без БД |
| Integration | 2 (`OrderRepositoryIntegrationTest`) | `JdbcOrderRepository` против `PostgreSQLContainer` (postgres:18.4) | реальный SQL round-trip: `INSERT` + `SELECT`, Postgres-sequence id |

Итого **6 тестов** (проверено 3 прогонами подряд, все зелёные).

## Реальный вывод (характерный прогон)

```
[main] INFO tc.postgres:18.4 - Container postgres:18.4 started in PT2.687712734S
[main] INFO ... Postgres container started: jdbc:postgresql://172.20.0.3:5432/orders_test
[main] INFO ... savesAndReadsOrder_againstRealPostgres took 64 ms
[INFO] Tests run: 2, Failures: 0, Errors: 0, Skipped: 0 -- OrderRepositoryIntegrationTest
[main] INFO ... pendingForSmallAmount_withStub took 26 µs
[INFO] Tests run: 4, Failures: 0, Errors: 0, Skipped: 0 -- OrderServiceUnitTest
[INFO] Tests run: 6, Failures: 0, Errors: 0, Skipped: 0
[INFO] BUILD SUCCESS
```

**Контраст по времени** (важен порядок величин, числа host-зависимы):

- Unit (бизнес-логика над stub/mock) — реальное время ассертов **17–48 микросекунд** на
  тест. «3.0–3.5 с» на класс в surefire — это оверхед старта JVM и self-attach agent'а
  Mockito, а не сама логика.
- Integration — старт контейнера postgres:18.4 **~2.5–2.7 секунды**, сам SQL round-trip
  (`INSERT`+`SELECT`) **50–74 миллисекунды**. «7.2–8.0 с» на класс — старт контейнера
  плюс ретраи соединения.
- Разница по порядку величины: unit — десятки микросекунд, integration — секунды (в
  основном на старт контейнера), но integration реально проверяет поведение против
  настоящей БД (Postgres-sequence id, реальный `SELECT`), чего stub не покрывает.

## ⚠️ Тонкость порядка BOM (Testcontainers 1.21.2 vs 1.21.3)

Без явной версии на testcontainers-зависимостях молча резолвится **1.21.2**, а не
1.21.3: `spring-boot-dependencies` BOM импортируется в `dependencyManagement`
**раньше** `testcontainers-bom`, а при конфликте побеждает первое объявление в
`dependencyManagement`. Фикс — явная `<version>${testcontainers.version}</version>`
на всех трёх testcontainers-зависимостях в `pom.xml` (parent BOM-порядок не трогаем).

## ⚠️ Топология Docker-in-Docker (Docker Desktop / Windows / WSL2)

Testcontainers запускался ВНУТРИ контейнера (сборка без хостового Maven), что на
Docker Desktop/Windows требует двух фиксов сверх дефолта — на Linux CI с
host-networking всё это проще:

- **`api.version=1.43`** (system property через `surefire` `argLine`): socket-прокси
  Docker Desktop отвечает HTTP 400 на Docker Engine API < 1.40, а Testcontainers 1.21.x
  без явного `apiVersion` фолбэчится на `VERSION_1_32`. Переменная окружения
  `DOCKER_API_VERSION` при этом **не читается** (проверено) — работает только
  системное свойство.
- **Общая user-defined docker-сеть** вместо опубликованного host-порта: JDBC-соединение
  на published host-порт sibling-контейнера стабильно даёт `Connection refused` (порт
  недостижим из другого контейнера). Имя предсозданной сети тест читает из env
  `TC_TESTCONTAINERS_NETWORK` и подключает к ней Postgres-контейнер
  (`withNetworkMode(...)`), соединяясь по внутреннему IP:5432 — официально
  задокументированный Testcontainers-паттерн для вложенных контейнеров. Если env не
  задан, тест работает как обычно (для Linux-хоста с host-networking).
- **`TESTCONTAINERS_RYUK_DISABLED=true`** — компромисс для DinD-топологии, **не**
  рекомендация для прод-CI (в обычном CI ryuk нужен для гарантированной уборки
  контейнеров).

## Запуск

Через Docker-образ Maven с temurin:25 (без хостового JDK 25). Предсоздаём общую
docker-сеть и передаём её имя тесту через env `TC_TESTCONTAINERS_NETWORK`
(`api.version=1.43` уже зашит в `surefire` `argLine` в `pom.xml` — на команду выносить
не нужно):

```bash
NET=jdd-testing-net
docker network create "$NET" 2>/dev/null || true
docker run --rm -v "$PWD:/ws" -w /ws \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --network "$NET" \
  -e TC_TESTCONTAINERS_NETWORK="$NET" \
  -e TESTCONTAINERS_RYUK_DISABLED=true \
  maven:3.9-eclipse-temurin-25 \
  mvn -q -pl testing test
```

На Linux-хосте с host-networking достаточно обычного `mvn -pl testing test`
(без env `TC_TESTCONTAINERS_NETWORK` — тест поднимет контейнер на дефолтной сети).

## Версии

JUnit 5.14.4 (junit-jupiter), Testcontainers 1.21.3 (PostgreSQL-модуль,
`postgres:18.4`), Mockito 5.23.0, maven-surefire-plugin 3.5.6, postgresql-driver
42.7.4. Точные версии — в `pom.xml` и корневом `../README.md`.
