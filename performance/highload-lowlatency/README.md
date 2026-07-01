# Highload под SLA < 300 мс: HAProxy L7 + пул бэкендов

Живой стенд к серии статей о highload с низкой латентностью. Кейс: сервис принимает
2000–5000 запросов в секунду, каждый запрос — JSON-документ размером ~8 КБ, синхронная
проверка полезной нагрузки занимает 100–200 мс, а SLA на весь запрос — не более 300 мс.
Стенд показывает, как балансировка на уровне L7 (HAProxy) и пул бэкендов (Go и Java) держат
такую нагрузку и что происходит с латентностью и очередями при разных настройках пула и
балансировщика.

## Контракт

Единый контракт `POST /check`, на который опираются все бэкенды (Go, Java) и клиенты стенда.

### `POST /check`

- Транспорт: HTTP/2 cleartext (h2c).
- Порт бэкенда: `9000`.
- Тело запроса: JSON, размер ~8 КБ. Схема:

```json
{
  "request_id": "<uuid>",
  "issued_at": "<rfc3339>",
  "items": [
    { "id": <int>, "code": "<str>", "value": <float>, "note": "<padding>" }
  ]
}
```

  Массив `items` заполняется до итогового размера тела ~8 КБ (см. пример в
  [`topology/payload/sample-request.json`](topology/payload/sample-request.json)).

- Обработка: имитация синхронной проверки — `sleep` на равномерно случайные 100–200 мс.
- Ответ: `200`, JSON:

```json
{
  "request_id": "<echo>",
  "backend": "<NAME>",
  "runtime": "go|java",
  "check_ms": <int>,
  "in_flight_peak": <int>
}
```

  - `request_id` — эхо из запроса.
  - `backend` — идентификатор инстанса, отвечающего на запрос (из `BACKEND_NAME`).
  - `runtime` — `go` или `java`, в зависимости от реализации бэкенда.
  - `check_ms` — фактическая длительность имитации проверки в мс.
  - `in_flight_peak` — пиковое число одновременно обрабатываемых запросов на этом инстансе
    с момента старта.

### `GET /healthz`

- Ответ: `200`, тело `"ok"`.

### gRPC-эквивалент (`highload.CheckService/Check`)

Тот же контракт, что и `POST /check`, но как unary gRPC RPC вместо JSON поверх HTTP/2.
Контракт: [`grpc/proto/check.proto`](grpc/proto/check.proto). Сгенерированный Go-код
закоммичен в [`grpc/gen/highload/`](grpc/gen/highload/) (свой модуль
`khorost.tech/highload-grpc-gen`) — читателю не нужен `protoc`, чтобы собрать стенд.

- Транспорт: HTTP/2 cleartext (h2c prior-knowledge) — стандартный транспорт grpc-go,
  никаких дополнительных настроек не требуется (в отличие от `net/http`, которому нужен
  `golang.org/x/net/http2/h2c`).
- Порт бэкенда: `9100` (сервис `services/grpc-backend`, отдельно от REST-пула на `9000`).
- Поля `CheckRequest`/`CheckResponse` — те же, что в JSON-контракте выше (`request_id`,
  `issued_at`, `items[]` / `request_id`, `backend`, `runtime`, `check_ms`, `in_flight_peak`),
  `runtime` = `go-grpc`.
- Health-check: стандартный `grpc.health.v1.Health` (`google.golang.org/grpc/health`).
- Балансировка HAProxy: пул из 2 инстансов (`grpc-backend-1`, `grpc-backend-2`) заведён во
  фронтенд `fe_grpc` (`:8090 proto h2` → backend `be_grpc`, round-robin), см. раздел
  "gRPC-профиль" ниже.

### Переменные окружения бэкенда

| Переменная | Назначение | По умолчанию |
|---|---|---|
| `BACKEND_NAME` | Идентификатор инстанса, попадает в поле `backend` ответа | — (обязателен) |
| `LISTEN_ADDR` | Адрес и порт, на котором слушает бэкенд | `:9000` |

## Запуск

```bash
cd topology
docker compose up -d --build
```

Поднимает HAProxy (три фронтенда, `:8080`, `:8081` и `:8090`), пул REST-бэкендов
(`go-backend-1/2/3`, `java-backend`) с health-check'ами и пул gRPC-бэкендов
(`grpc-backend-1/2`). Клиенты-нагрузчики не стартуют автоматически — у них свои
`profiles`, запускаются по требованию:

```bash
# Go-клиент — идёт в h2c-фронтенд :8080 (HTTP/2 end-to-end)
docker compose --profile load run --rm client-go

# Java-клиент — идёт в HTTP/1.1-фронтенд :8081 (см. "Что наблюдать" ниже)
docker compose --profile load-java run --rm client-java

# gRPC-клиенты (Go и Java) — оба идут в h2c-фронтенд :8090 (см. "gRPC-профиль" ниже)
docker compose --profile load-grpc run --rm client-grpc-go
docker compose --profile load-grpc-java run --rm client-grpc-java
```

> **⚠️ gRPC-Java и прокси.** Netty-клиент gRPC уважает прокси-окружение
> (`GRPC_PROXY_EXP`, `-Dhttps.proxyHost`). Достаточно даже *пустой* переменной
> `GRPC_PROXY_EXP=` — `ProxyDetectorImpl` решит, что прокси задан, и все вызовы
> `client-grpc-java` упадут при полностью живом бэкенде. «Отключать прокси» надо
> не пустой строкой, а не задавая переменную вовсе (или через `NO_PROXY`).
> Go-клиент (`grpc.NewClient`) к пустой переменной устойчив. По умолчанию compose
> её не ставит — оба клиента работают из коробки.

Клиенты управляются переменными окружения `TARGET` / `CONCURRENCY` / `REQUESTS` /
`CONNS` / `TIMEOUT_MS` / `PAYLOAD` (см. Dockerfile'ы клиентов), например:

```bash
docker compose --profile load run --rm -e REQUESTS=5000 -e CONCURRENCY=200 client-go
```

Остановить и снести стенд:

```bash
docker compose down
```

## Что наблюдать

- **Распределение по пулу.** Отчёт клиента в конце прогона печатает таблицу
  `backend distribution` — доли `go-1` / `go-2` / `go-3` / `java-1`. На L7-фронтенде
  (`fe_http2` для Go, `fe_h1` для Java) HAProxy распределяет каждый HTTP-запрос
  индивидуально (round-robin по streams/запросам), поэтому при достаточном числе
  запросов доли получаются близкими к 25% на каждый инстанс — независимо от того,
  что клиент держит всего `CONNS` TCP/H2-соединений в пуле.
- **Latency.** Каждый бэкенд имитирует синхронную проверку случайной длительностью
  100–200 мс (`sleep`), поэтому у p50 стоит ожидать значение в этом диапазоне,
  у p95/p99 — ближе к верхней границе и выше за счёт хвоста и накладных расходов
  HAProxy/сети. Полный бюджет запроса — SLA < 300 мс (см. таймауты в `haproxy.cfg`
  и `TIMEOUT_MS` клиента).
- **L7 vs L4.** В `topology/haproxy/` рядом лежит `haproxy-l4.cfg` — конфиг ДЛЯ
  СРАВНЕНИЯ, в `mode tcp`. Чтобы увидеть разницу, временно подмените
  `haproxy.cfg` на `haproxy-l4.cfg` в volume-маунте `haproxy`-сервиса (или
  скопируйте поверх) и перезапустите: в L4-режиме балансировка происходит один
  раз на TCP-соединение целиком, а не на отдельный HTTP/2-запрос — при малом
  числе долгоживущих h2-соединений клиента распределение по `backend
  distribution` станет заметно неравномерным (вплоть до того, что часть
  инстансов пула не получит ни одного запроса). Подробное объяснение — в
  комментариях `haproxy-l4.cfg` и в статье про L4/L7/HTTP/2.
- **Почему Java-клиент ходит в `:8081`, а не в `:8080`.** JDK
  `java.net.http.HttpClient` не умеет h2c prior-knowledge: вместо того чтобы сразу
  открыть HTTP/2-соединение без TLS, он отправляет HTTP/1.1-запрос с заголовком
  `Upgrade: h2c` и ждёт ответа `101 Switching Protocols`. Фронтенд `fe_http2`
  (`bind :8080 proto h2`) настроен на приём готового HTTP/2 prior-knowledge и
  на такой апгрейд отвечает `<BADREQ>`. Поэтому для Java-клиента в `haproxy.cfg`
  заведён отдельный фронтенд `fe_h1` (`bind :8081`, обычный HTTP/1.1) —
  client↔HAProxy идёт по HTTP/1.1, а HAProxy↔backend всё равно поднимается по
  HTTP/2 (`proto h2` на серверах в `be_check_pool`, тот же пул для обоих
  фронтендов). Отчёт `client-java` печатает поле `protocol: [HTTP_1_1]`,
  подтверждающее эту схему. Это осознанная демонстрация ограничения JDK —
  подробнее разобрано в статье про JVM-клиент серии.
- **Таймаут < SLA.** У клиентов `TIMEOUT_MS` по умолчанию — 280 мс, чуть меньше
  общего бюджета в 300 мс: часть запросов, у которых бэкенд выбрал длительность
  проверки ближе к верхней границе 200 мс плюс накладные расходы, не укладывается
  в этот таймаут и уходит в `timeouts` в отчёте клиента (а не в `errors` —
  клиент различает их). Это ожидаемое поведение стенда, а не баг: таймаут
  клиента — часть бюджета SLA, а не механическое ожидание "пока бэкенд ответит".

## gRPC-профиль

Отдельный фронтенд HAProxy `fe_grpc` (`bind :8090 proto h2` → `backend be_grpc`, см.
`topology/haproxy/haproxy.cfg`) перед пулом из 2 gRPC-бэкендов (`grpc-backend-1/2`,
контракт `highload.CheckService/Check`, см. выше). Запуск клиентов:

```bash
docker compose --profile load-grpc run --rm client-grpc-go
docker compose --profile load-grpc-java run --rm client-grpc-java
```

Что наблюдать:

- **Распределение по пулу.** Как и у REST-клиентов, отчёт печатает таблицу `backend
  distribution` — при достаточном числе запросов доли `grpc-1` / `grpc-2` близки к 50%
  каждая: `fe_grpc` балансирует по отдельным HTTP/2-запросам (unary RPC), а не по
  TCP-соединению целиком, то же самое L7-поведение, что и у `fe_http2`/`fe_h1`.
- **Латентность.** Тот же контракт имитации проверки (100–200 мс `sleep`), тот же
  бюджет SLA < 300 мс и тот же принцип таймаута клиента (`TIMEOUT_MS` по умолчанию
  280 мс) — см. "Таймаут < SLA" выше, справедливо и для gRPC-клиентов.
- **Главный тезис раздела — почему `:8090`, а не `:8080`/`:8081`.** У gRPC нет отдельного
  HTTP/1.1-Upgrade-пути на cleartext: и `grpc.NewClient` с insecure-credentials
  (`clients/grpc-go`), и `ManagedChannelBuilder.usePlaintext()` (`clients/grpc-java`) по
  умолчанию сразу отправляют HTTP/2-преамбулу (`PRI * HTTP/2.0`) — то есть h2c
  prior-knowledge с первого байта, тот же режим, что и ожидает `fe_grpc` (`proto h2`,
  как и `fe_http2` на `:8080`). Это прямо противоположно ситуации из раздела "Почему
  Java-клиент ходит в `:8081`, а не в `:8080`" выше: там `java.net.http.HttpClient`
  пытается HTTP/1.1 `Upgrade: h2c` и ловит `<BADREQ>` от h2c-only фронтенда — а
  `ManagedChannel` того же JDK/JVM-приложения проходит `:8090` без единой правки,
  потому что у gRPC этой развилки (Upgrade vs prior-knowledge) в принципе нет: cleartext
  всегда prior-knowledge. Практический вывод: если для JVM-highload-клиента обязателен
  HTTP/2 без TLS и упираетесь в ограничение JDK `HttpClient` — gRPC `ManagedChannel`
  (или любой другой prior-knowledge-клиент) закрывает эту проблему архитектурно, а не
  обходным путём вроде принудительного даунгрейда до HTTP/1.1.

## Статьи серии

1. [Бюджет латентности и выбор транспорта](https://khorost.tech/performance/latency-budget-and-transport/)
2. [HAProxy: L4 vs L7 и HTTP/2](https://khorost.tech/performance/haproxy-l4-l7-http2/)
3. [Go-клиент для highload: пул соединений](https://khorost.tech/performance/go-highload-client-pooling/)
4. [JVM-сервис для highload](https://khorost.tech/performance/jvm-highload-service/)
5. [JVM-клиент для highload](https://khorost.tech/performance/jvm-highload-client/)

## Лицензия

MIT — см. [LICENSE](../../LICENSE) в корне репозитория.
