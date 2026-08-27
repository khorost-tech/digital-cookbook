# Тестирование распределённых систем — живой стенд

Стенд к статье «Тестирование распределённых систем». Каждая тестовая практика
здесь — не текст, а запускаемый пример на настоящих зависимостях
([Testcontainers](https://testcontainers.com/): реальные Postgres и Kafka).

## Что демонстрирует каждый раздел

| Раздел | Практика | Что проверяет тест |
|--------|----------|--------------------|
| `integration/` | Интеграционный тест на реальной БД | `OrderRepositoryTest` поднимает Postgres, накатывает схему Flyway'ем и проверяет то, что **мок пропустил бы**: настоящий `UNIQUE`-constraint на `external_id` (дубль → SQLState 23505) и идемпотентный upsert `ON CONFLICT DO NOTHING`. |
| `contract/` | Consumer-driven contract (Pact JVM) | `ConsumerInventoryPactTest` описывает ожидания consumer'а к сервису склада и генерирует пакт; `ProviderInventoryVerificationTest` поднимает реальный HTTP-сервер провайдера и **верифицирует** пакт. Несовместимость ловится (см. ниже). |
| `async/` | Тест eventual-эффекта **без sleep** | `AsyncEventualDeliveryTest` шлёт событие в Kafka, фоновый консюмер пишет эффект в хранилище, тест ждёт результата через Awaitility `await().untilAsserted(...)`. |
| `failures/` | Идемпотентность при повторной доставке | `IdempotentDeliveryTest` имитирует дубль доставки одного события; обработчик с ключом идемпотентности (таблица `processed_events`) применяет эффект **ровно один раз**. |

## Как запускать

Нужен **Docker** (Testcontainers поднимает контейнеры) и **JDK 21**. Maven —
через контейнер (на хосте `mvn` не требуется):

```bash
# компиляция (без Docker, только зависимости)
docker run --rm \
  -v "$PWD:/work" -w /work -v "$HOME/.m2:/root/.m2" \
  maven:3.9-eclipse-temurin-21 mvn -q -DskipTests test-compile

# полный прогон тестов (нужен доступ к docker.sock изнутри контейнера)
docker run --rm \
  -v "$PWD:/work" -w /work -v "$HOME/.m2:/root/.m2" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  maven:3.9-eclipse-temurin-21 mvn test
```

Либо, если на хосте есть Maven: `mvn test` из этого каталога.

## Поднимаемые контейнеры

При `mvn test` Testcontainers стартует:

- `postgres:18-alpine` — для `integration/` и `failures/` (каждый тест-класс
  поднимает свой чистый экземпляр, схема накатывается Flyway с нуля);
- `apache/kafka:4.3.1` — для `async/` (KRaft-режим, без ZooKeeper, через новый
  модуль `org.testcontainers.kafka.KafkaContainer`).

Раздел `contract/` контейнеров не поднимает: consumer работает с mock-сервером
Pact, provider — с локальным `com.sun.net.httpserver.HttpServer`.

## Как ловится несовместимость контракта

`ProviderInventoryVerificationTest` воспроизводит запросы из пакта против живого
HTTP-сервера провайдера и сверяет ответ. Если провайдер изменит контракт молча —
вернёт не 200, уберёт поле `sku`/`available` или сменит тип `available` со числа
на строку — `context.verifyInteraction()` провалит тест. Это и есть страховка от
рассинхрона между сервисами, который на юнит-моках не виден.

## Про антипаттерн `Thread.sleep` в async-тестах

`AsyncEventualDeliveryTest` намеренно **не** использует `Thread.sleep`. Пауза
фиксированной длины — источник двух проблем:

- слишком короткая → флак: медленный CI не успел, эффект ещё не наступил;
- слишком длинная → трата времени: эффект наступил за десятки миллисекунд, а
  тест всё равно ждёт секунды.

Awaitility опрашивает условие с коротким интервалом и завершается сразу по факту
его выполнения, с внятным сообщением по таймауту.

## Версии

| Компонент | Версия |
|-----------|--------|
| JDK | 21 (`maven:3.9-eclipse-temurin-21`) |
| JUnit 5 (BOM) | 5.11.4 |
| Testcontainers (BOM) | 1.21.4 (модули `junit-jupiter`, `postgresql`, `kafka`) — ≥1.21 нужен для Docker 29 |
| Pact JVM (consumer+provider junit5) | 4.6.14 |
| Awaitility | 4.2.2 |
| Flyway | 10.20.1 (`flyway-core` + `flyway-database-postgresql`) |
| PostgreSQL JDBC | 42.7.4 |
| kafka-clients | 3.8.1 (forward-совместим с брокером Kafka 4.x) |
| Postgres (образ) | `postgres:18-alpine` |
| Kafka (образ) | `apache/kafka:4.3.1` |

## Статус проверки

Полный набор **проверен живьём на нативном Docker Engine** — через Docker-in-Docker (`docker:dind`, движок Docker 29.6.1, Testcontainers 1.21.4, JDK 21, образы `postgres:18-alpine` + `apache/kafka:4.3.1`): `mvn test` → **BUILD SUCCESS, `Tests run: 7, Failures: 0, Errors: 0`** (Postgres-интеграция, Kafka-eventual через Awaitility, Pact consumer/provider, идемпотентность). Контейнеры поднялись внутри dind, тесты зелёные.

На **Docker Desktop с ограниченным `docker_cli`-proxy** (встречается в закрытых корпоративных сборках) есть два подводных момента, оба проявились при проверке:
1. **Версия Testcontainers.** docker-java из TC 1.20.4 не договаривается с Docker 29 (`/info` → HTTP 400, хотя `docker info` в CLI работает) — нужна TC ≥ 1.21 (здесь 1.21.4): с ней контейнеры стартуют.
2. **Публикация портов.** Через proxy `docker run -p` из CLI форвардит порт на хост, а container-create от docker-java — нет, поэтому тест не достукивается до БД/брокера (`Connection refused` на mapped-порту). Обходится запуском на прямом Docker daemon.

Полезно также `TESTCONTAINERS_RYUK_DISABLED=true`, если reaper-контейнеру недоступен его порт. Итог: на прямом Docker daemon `mvn test` поднимает Postgres + Kafka и проходит целиком (доказано на DinD, см. начало раздела); на ограниченном `docker_cli`-proxy той же машины он не идёт из-за проброса портов (два пункта выше) — это ограничение среды, а не тестов.

Воспроизвести прогон на нативном движке локально (без CI), через Docker-in-Docker:

    docker network create tcnet
    docker run -d --privileged --name tc-dind --network tcnet -e DOCKER_TLS_CERTDIR="" docker:dind --host=tcp://0.0.0.0:2375
    # (опционально) перенести локальные образы в dind, чтобы не тянуть из сети:
    docker save postgres:18-alpine apache/kafka:4.3.1 | docker exec -i tc-dind docker load
    docker run --rm --network tcnet -e DOCKER_HOST=tcp://tc-dind:2375 \
      -e TESTCONTAINERS_HOST_OVERRIDE=tc-dind -e TESTCONTAINERS_RYUK_DISABLED=true \
      -v "$PWD:/work" -w /work -v "$HOME/.m2:/root/.m2" maven:3.9-eclipse-temurin-21 mvn test
    docker rm -f tc-dind && docker network rm tcnet
