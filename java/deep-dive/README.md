# java-deep-dive

Мультимодульный cookbook к серии статей про JVM/Java. Даёт реальные числа
(throughput, время старта, RSS) для hands-on разделов статей — не готовое
production-решение, а учебные стенды для сравнения подходов.

## Фактические версии тулчейна

Все версии ниже — не «на момент создания каркаса», а подтверждены живыми
прогонами каждого стенда (см. отчёты задач сборки). Числа host-зависимы
(Docker Desktop / Windows), сами версии — нет.

| Компонент | Версия | Где используется |
|---|---|---|
| JDK | **25.0.3** (Temurin, `eclipse-temurin:25-jdk`/`-jre`) | все Maven-модули + рантайм Kotlin-модуля |
| Maven | 3.9.x (`maven:3.9-eclipse-temurin-25`) | сборка всех Maven-модулей (без хостового Maven) |
| Gradle | **9.6.1** (`gradle:9-jdk25`; `gradle:8-jdk25` не существует) | `kotlin/` |
| Kotlin | **2.2.0** (компилятор/рантайм на JDK 25, байткод-target **JVM 24** — Kotlin 2.2.0 ещё не поддерживает JDK 25 как bytecode target, фолбэк подтверждён логом `compileKotlin`) | `kotlin/` |
| kotlinx-coroutines-core | 1.11.0 | `kotlin/` (корутин-бенчмарк, идиомы) |
| Shadow (Gradle) | com.gradleup.shadow 9.5.1 | `kotlin/` (fat-jar) |
| GraalVM CE | **25.0.2+10.1** (`native-image 25.0.2`, `ghcr.io/graalvm/native-image-community:25`, JDK 25 из коробки, без fallback) | `build-packaging/` (режим native) |
| Spring Boot | 3.5.3 | `spring-vs-quarkus/spring`, унаследовано в `dependencyManagement` |
| Quarkus | 3.22.3 | `spring-vs-quarkus/quarkus` |
| Hibernate ORM | 7.0.2.Final | `data-access/` (требует jakarta.persistence-api 3.2.0 — явный оверрайд, см. README модуля) |
| jOOQ | 3.20.3 | `data-access/` (DSL без codegen) |
| HikariCP | 7.1.0 | `data-access/` |
| postgresql-driver (JDBC) | 42.7.4 | `data-access/`, `testing/` |
| kafka-clients | 4.3.0 | `messaging/` (пин = версия образа брокера) |
| Kafka broker | `apache/kafka:4.3.0` (KRaft single-node) | `docker/compose.yml` |
| Testcontainers | 1.21.3 (⚠️ без явной версии молча резолвится 1.21.2 из-за порядка BOM — см. README `testing/`) | `testing/` |
| JUnit | 5.14.4 (junit-jupiter) | `testing/` |
| Mockito | 5.23.0 | `testing/` |
| maven-surefire-plugin | 3.5.6 | `testing/` |
| resilience4j | 2.3.0 | `production-patterns/` |
| async-profiler | 4.4 (`linux-x64`) | `profiling/` |
| reactor-core | 3.8.6 | `concurrency/` (Reactor-режим бенчмарка) |
| PostgreSQL | 18.4 | `docker/compose.yml` (`data-access/`, `testing/`) |

## Модули

| Модуль | Что демонстрирует |
|---|---|
| `concurrency/` | Virtual threads vs platform threads (пул 200 и пул 10 000) vs Reactor под I/O-bound нагрузкой — throughput/p50/p99/peak RSS, данные для SVG #5 (вместе с `kotlin/`) |
| `build-packaging/` | JVM fat-jar vs layered vs AppCDS vs GraalVM native-image vs jlink — startup/RSS/размер образа, данные для SVG #7 |
| `spring-vs-quarkus/` | Spring Boot vs Quarkus в JVM-режиме на одном базовом образе — startup/RSS/размер, данные для SVG #4 |
| `data-access/` | JDBC vs jOOQ (без codegen) vs Hibernate/JPA (N+1 → `JOIN FETCH`/`@EntityGraph`) + HikariCP под нагрузкой, живой Postgres 18.4 |
| `messaging/` | Kafka: базовая доставка, ребаланс consumer groups, exactly-once (транзакционный producer + `read_committed`), retry+DLT |
| `profiling/` | JFR + async-profiler на одном и том же прогоне — кросс-верификация hot-метода, топ-аллокации, GC-пауз |
| `production-patterns/` | resilience4j (Retry → CircuitBreaker → RateLimiter → Bulkhead) + graceful shutdown с дренажом in-flight запросов + liveness/readiness |
| `testing/` | JUnit 5 unit-тесты (stub/Mockito) vs Testcontainers integration-тесты против настоящего Postgres — контраст по времени и подходу |
| `modern-features/` | Статус фич JDK 25 (records/sealed/pattern matching/unnamed variables/virtual threads — все finalized; string templates — removed) |
| `kotlin/` | Идиомы Kotlin 2.2.0 (null-safety, data classes, sealed, coroutines, Flow) + корутин-бенчмарк для SVG #5 |

## Требования

- **JDK 25 (LTS)** — основной тулчейн. Проверено: `eclipse-temurin:25-jdk` и
  `maven:3.9-eclipse-temurin-25` тянутся с Docker Hub без прокси.
  Если на хосте нет JDK 25 (в этом репозитории по состоянию на 2026-07-07 на
  хосте установлен JDK 21) — модули можно собирать через Docker-образ Maven,
  см. ниже.
- **Docker** + Docker Compose — для общего стенда (Postgres + Kafka) и/или
  сборки без хостового JDK 25.
- Gradle — используется Kotlin-модулем (`kotlin/`, Gradle 9.6.1 через
  `gradle:9-jdk25`, вне Maven-реактора); Maven — основной инструмент сборки для
  остального мультимодуля.

## Структура

```
java-deep-dive/
  pom.xml              # parent POM: packaging=pom, JDK 25, общие версии зависимостей
  docker/compose.yml    # общий стенд Postgres + Kafka (KRaft) для под-модулей
  <модуль>/              # добавляется отдельной задачей на каждый стенд/сравнение
```

Полный список модулей и что каждый демонстрирует — см. раздел «Модули» выше;
фактические зарегистрированные модули — `<modules>` в `pom.xml`.

## Сборка

Хостовым JDK 25 (если установлен):

```bash
cd java-deep-dive
mvn -q validate
mvn -q -pl <module> package
```

Без хостового JDK 25 — через Docker-образ Maven с temurin:25:

```bash
docker run --rm -v "$PWD:/ws" -w /ws maven:3.9-eclipse-temurin-25 mvn -q validate
```

## Общий стенд (Postgres + Kafka)

```bash
docker compose -f docker/compose.yml up -d
docker compose -f docker/compose.yml down -v
```

Порты подобраны так, чтобы не конфликтовать с другими cookbook-стендами
в этом репозитории:

| Сервис   | Host-порт | Контейнерный порт |
|----------|-----------|--------------------|
| Postgres | 5455      | 5432               |
| Kafka    | 9096      | 9094 (HOST-listener) |

Стенд — KRaft single-node Kafka без Zookeeper и одиночный Postgres.
Переиспользуется под-модулями напрямую; поднимать не обязательно, пока не
появится модуль, которому это реально нужно.

## Оговорка про числа

Числа из стендов (throughput/latency/startup/RSS) — **host-зависимы**:
конкретные значения будут отличаться на разном железе/ОС/загрузке машины.
Смысл не в абсолютных цифрах, а в порядке величин и относительном сравнении
подходов внутри одного стенда, на одной машине, при прочих равных.
