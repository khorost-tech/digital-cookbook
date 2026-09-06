# keycloak — готовый IdP на живом стенде

Живой стенд к серии **«Готовый IdP: Keycloak на практике»** (khorost.tech, раздел security):

1. [Свой auth или готовый IdP: когда брать Keycloak](https://khorost.tech/security/keycloak-when-to-use-idp/)
2. [Keycloak на практике: realms, clients, потоки, деплой](https://khorost.tech/security/keycloak-realms-clients-deploy/)
3. [Валидация токенов и снижение нагрузки на IdP](https://khorost.tech/security/keycloak-token-validation-load/)
4. [Keycloak под свой продукт: экраны логина и внешние провайдеры](https://khorost.tech/security/keycloak-login-themes-federation/)
5. [Keycloak в production: HA, БД, бэкапы, обновления](https://khorost.tech/security/keycloak-production-ha-backups/)

Одной командой поднимается Keycloak с внешним PostgreSQL, два resource server'а
(**Go** и **Java** — один и тот же контракт эндпоинтов), внешний OIDC-провайдер для
identity brokering и кастомная login-тема. Demo-скрипты показывают выдачу токенов,
разграничение доступа по ролям (200/401/403), федерацию через внешний IdP и разницу
между локальной JWKS-валидацией и introspection под нагрузкой. Отдельный
`docker-compose.prod.yml` — production-топология (2 реплики + LB + TLS).

> Все пароли, секреты и сертификаты в стенде — **demo-only**, пригодны только для
> локального прогона. Для production см. раздел [Production](#production) и статью 5.

## Что демонстрирует

| Компонент | Где | Что видно |
|---|---|---|
| **Keycloak + PostgreSQL** | `docker-compose.yml`, `realm/` | realm `demo` импортируется на старте: клиенты, роли, пользователи, mappers |
| **Go resource server** | `backend/go/` (:8081) | валидация JWT по JWKS (`go-oidc`) или introspection; роли из `realm_access.roles` |
| **Java resource server** | `backend/java/` (:8082) | зеркало на Spring Boot 3 + `oauth2-resource-server`; тот же контракт |
| **Identity brokering** | `mock-oidc/`, `scripts/broker-demo.sh` | Keycloak как OIDC-брокер к внешнему IdP; brokered-пользователь заводится и связывается |
| **Login-тема** | `themes/khorost/` | кастомный брендинг экрана логина (FreeMarker + CSS) |
| **Bench** | `bench/`, `scripts/bench.sh` | JWKS vs introspection: p50/p95/p99 + число обращений к IdP |
| **Production** | `docker-compose.prod.yml`, `nginx/`, `certs/` | `start --optimized`, 2 реплики (JDBC_PING), nginx LB + TLS, `/metrics`; HA compute-слоя (PG/nginx — SPOF, осознанно) |
| **Backup/DR** | `scripts/backup-restore.sh` | `pg_dump` → авария (`down -v`) → restore → realm + канарейка + токен на месте (end-to-end) |

## Prerequisites

- **Docker** + `docker compose` (стенд собирает образы backend'ов multi-stage — сеть нужна).
- Для локальной сборки/тестов **Java**-сервиса вне Docker — **JDK 21** (внутри стенда
  сборка идёт в контейнере, JDK на хосте не требуется).
- Свободные порты хоста: **8080** (Keycloak auth/UI), **8081** (Go), **8082** (Java),
  **8083** (mock-OIDC), **9000** (Keycloak management: health/metrics).

## Быстрый старт

```bash
# 1. Поднять стенд (первый запуск собирает образы Go/Java — это занимает время)
bash scripts/up.sh

# 2. Получить access-token (password grant, клиент cli)
bash scripts/get-token.sh bob        # роли user+admin
bash scripts/get-token.sh alice -d   # роль user, -d = показать декодированный payload

# 3. Прогнать матрицу авторизации против Go (:8081) И Java (:8082)
bash scripts/call-apis.sh

# 4. Остановить и очистить
bash scripts/down.sh
```

`call-apis.sh` печатает реальные HTTP-коды. Ожидаемый результат (подтверждён живым
прогоном стенда) — идентичен на Go и Java:

| Сценарий | Код |
|---|---|
| `GET /public` без токена | **200** |
| `GET /me` без токена | **401** |
| `GET /me` (alice) | **200** |
| `GET /admin` (alice) | **403** |
| `GET /admin` (bob) | **200** |
| `GET /me` (битый токен) | **401** |

Дополнительные сценарии (стенд должен быть поднят):

```bash
bash scripts/broker-demo.sh      # identity brokering через mock-OIDC (end-to-end, с ассертами)
bash scripts/bench.sh            # JWKS vs introspection под нагрузкой (стенд оставляет поднятым)
bash scripts/backup-restore.sh   # backup/DR: pg_dump → down -v → restore → проверки (end-to-end)
```

`broker-demo.sh` не только проходит authorization code flow, но и **ассертит**:
совпадение `state` в callback (защита от CSRF), заведение brokered-пользователя,
связку с federated identity `mock` и назначение realm-роли `user` — при несоответствии
выходит с ненулевым кодом.

## Dev-топология

`docker-compose.yml` поднимает (Keycloak в режиме `start-dev` — упрощённый, **не для
production**):

- **postgres** (`postgres:16-alpine`) — единственный источник долговечного состояния Keycloak (краткоживущий runtime-стейт — только в кэшах); healthcheck `pg_isready`.
- **keycloak** (`quay.io/keycloak/keycloak:26.7.0`) — `start-dev --import-realm`, порты
  8080 (auth/UI) и 9000 (management: `/health/ready`, `/metrics`). Realm `demo`
  импортируется из `realm/demo-realm.json`. `KC_HOSTNAME=http://keycloak:8080` фиксирует
  `issuer`, чтобы токен, взятый с хоста через проброшенный порт, и токен, валидируемый
  внутри docker-сети, имели один и тот же `iss`.
- **backend-go** (:8081) и **backend-java** (:8082) — resource server'ы (см. ниже).
- **mock-oidc** (`ghcr.io/navikt/mock-oauth2-server:2.1.10`, :8083) — внешний OIDC-провайдер
  для brokering (роль Яндекс/VK/корпоративного IdP).
- **bench** — Go-нагрузчик под профилем `bench`; при обычном `up` не стартует.

В образе Keycloak нет `curl`/`wget`, поэтому healthcheck сделан через `bash /dev/tcp` к
management-порту 9000 (проверяет `/health/ready` → `"status": "UP"`).

## Realm-модель (`realm/demo-realm.json`)

**Клиенты:**

| Client | Тип | Назначение |
|---|---|---|
| `backend` | confidential | audience resource server'ов (service accounts on); секрет — demo-only |
| `frontend` | public + PKCE (S256) | браузерный клиент (Authorization Code Flow), redirect `http://localhost:3000/*` |
| `cli` | public + Direct Access Grants | password grant для demo-скриптов (`get-token.sh`) |

**Realm-роли:** `user`, `admin`.

**Пользователи (demo-only, пароли non-temporary):**

| Юзер | Пароль | Роли |
|---|---|---|
| `alice` | `alice-demo-2026` | `user` |
| `bob` | `bob-demo-2026` | `user`, `admin` |

`realm_access.roles` присутствуют в access-токене по умолчанию (client-scope `roles`).
Клиенты `cli` и `frontend` несут audience-mapper, кладущий `backend` в `aud` — поэтому
`"aud": ["backend", "account"]`, и resource server'ы валидируют audience по значению
`backend`.

## Resource servers: Go и Java (JWKS vs introspection)

Оба сервиса реализуют один контракт: `/public` (без токена), `/me` (любой валидный
токен → `sub` + роли), `/admin` (только роль `admin`).

- **Go** (`backend/go/`, `github.com/coreos/go-oidc/v3`): на старте — OIDC discovery по
  issuer, дальше локальная проверка подписи по **JWKS** (ключи кэшируются, ротация по `kid`).
  Переключается в **introspection** через `AUTH_MODE=introspect` (+ секрет клиента
  `backend`) — тогда каждый запрос проверяется RFC 7662-запросом к Keycloak.

  **Семантика кодов в introspect-режиме различает вердикт и сбой.** Операционная
  недоступность IdP (сетевая ошибка, таймаут, HTTP 5xx / любой не-200, битый ответ) — это
  **не** «токен невалиден»: middleware отдаёт **503** (`Retry-After: 1`), а не 401, потому
  что токен может быть валиден, просто проверить его сейчас нельзя. К **401** ведёт только
  валидный ответ introspection с `active:false`; к **403** — валидный токен без нужной роли.
  Так балансировщик/клиент повторит запрос при сбое IdP вместо того, чтобы счесть токен
  отозванным (см. `ErrUpstreamUnavailable` в `backend/go/internal/auth/verifier.go`).
- **Java** (`backend/java/`, Spring Boot 3 + `spring-boot-starter-oauth2-resource-server`):
  `JwtDecoder` из discovery, `JwtAuthenticationConverter` маппит `realm_access.roles` →
  `ROLE_*`, `AudienceValidator` требует `backend` в `aud`. Юнит-тесты (`mvn verify`)
  **гоняются при сборке образа** (`backend/java/Dockerfile`, без `-DskipTests`): на JDK 21
  self-attach java-агента Mockito разрешён флагом surefire `-XX:+EnableDynamicAgentLoading`
  (см. `pom.xml`). `Tests run: 2, Failures: 0`.

Разница между режимами количественно — в разделе [Bench](#bench).

## Identity brokering: внешний IdP (mock-OIDC), Яндекс, VK, ЕСИА

`scripts/broker-demo.sh` проходит authorization code flow через IdP `mock`: Keycloak
серверно обменивает код на токен внешнего провайдера, прогоняет first-broker-login,
**заводит и связывает** локального пользователя (claim'ы → username/email/имя, роль `user`)
и выдаёт свой access-token. Внешний `mock` играет роль стороннего IdP.

**mock-OIDC** настроен явными URL с разными хостами (mock выводит issuer из `Host`, а
Keycloak ходит к нему серверно, браузер — с хоста):

- `authorizationUrl` → `http://localhost:8083/default/authorize` (браузер, хост)
- `tokenUrl` / `jwksUrl` / `userInfoUrl` → `http://mock-oidc:8083/default/*` (Keycloak, docker-сеть)
- `issuer` → `http://mock-oidc:8083/default`

### Реальные провайдеры (Яндекс, VK) — конфигом, секреты не в репо

**Яндекс** — тип **OAuth v2** (`providerId: oauth2`), НЕ OpenID Connect v1.0: id_token
Яндекс не выдаёт, профиль берётся из userinfo. **VK ID** сложнее обычного generic:
требует PKCE и возвращает `device_id`, который нужно протаскивать в token-обмен
(актуальные endpoints — на `id.vk.ru`); стоковый generic-провайдер Keycloak может не
подойти — вероятен custom SPI/адаптер, **в стенде не проверялось**. `clientSecret` —
**не в репо**: в стенде плейсхолдер/env, в проде — из секрет-менеджера (Vault). В
кабинете провайдера прописывается redirect:
`https://<keycloak-домен>/realms/<realm>/broker/<alias>/endpoint`.

- **Яндекс (Yandex ID):** регистрация на `oauth.yandex.ru`. Endpoints задать явно
  (discovery неполный): authorize `https://oauth.yandex.ru/authorize`, token
  `https://oauth.yandex.ru/token`, userinfo `https://login.yandex.ru/info?format=json`.
  Это скорее OAuth2 с userinfo, чем строгий OIDC — профиль берётся из userinfo, не из
  id_token. Attribute-мапперы: `default_email`→email, `login`/`id`→username, `first_name`/`last_name`→имена.
- **VK (VK ID):** регистрация на `id.vk.com`/`dev.vk.com`. У VK несколько поколений API
  (VK ID vs legacy OAuth) — URL и формат ответа зависят от выбранного, **пиновать под
  конкретное поколение**. VK ID требует PKCE и `device_id` в token-обмене (endpoints на
  `id.vk.ru`); email отдаётся в ответе `/oauth2/user_info` при запрошенном scope.

Рекомендация РФ-аудитории: российские провайдеры (Яндекс, VK) — как основной вариант
входа; зарубежные (Google, GitHub) — альтернативой. `trustEmail` включать осознанно.

### ЕСИА (Госуслуги) — концептуально, в стенде не воспроизводится

ЕСИА — государственный IdP; в учебном стенде подключить нельзя:

- **Аккредитация Минцифры:** доступ к ЕСИА требует регистрации информационной системы и
  согласования — не «зарегистрировать OAuth-приложение» как у Яндекса/VK.
- **ГОСТ-TLS:** взаимодействие идёт по каналам с ГОСТ-шифрованием (СКЗИ типа КриптоПро) и
  подписанием запросов по ГОСТ. Стандартный Keycloak с RS256/обычным TLS это не
  закрывает — нужен ГОСТ-терминирующий прокси перед Keycloak.
- **Специфика профиля/флоу:** регламентированный набор scope/claim (СНИЛС, уровни УЗ),
  требования к журналированию и защите ПДн.

Вывод: архитектурно возможно через generic OIDC + ГОСТ-TLS-прослойку, но требует
аккредитации и СКЗИ — вне рамок учебного стенда.

## Login-тема

`themes/khorost/login/` — кастомный брендинг экрана логина в стиле журнала «Полдень.
XXI век» (кремово-бежевый фон, терракотовые акценты, орбитальный логотип). Реализована
штатным путём Keycloak: каталог темы + `theme.properties` (`parent=keycloak`) + CSS +
логотип + локализованные `messages_*.properties`; ни один FreeMarker-шаблон не
копировался. Realm ссылается на неё полем `loginTheme=khorost`. Скриншот —
`themes/screenshot-login.png`.

В `start-dev` темы не кешируются — правки CSS/логотипа видны после F5 без пересборки
образа (в `start`/prod темы кешируются).

**Шрифты — честно системные.** В `khorost.css` нет `@font-face` и бинарных `.woff2`
(их не тащим в репо стенда), поэтому CSS указывает только реально доступный системный
стек (`system-ui`, `-apple-system`, `Segoe UI`, …), а не брендовые Work Sans / Instrument
Sans, которых на странице фактически нет. Стиль «Полдень» держится на палитре
(кремово-бежевый + терракота), тонких линиях и острых углах, а не на гарнитуре.

**keycloakify как альтернатива:** для продуктового login-UX с логикой и дизайн-системой
на React/TypeScript тулинг keycloakify собирает экраны в стандартную Keycloak-тему.
Плюсы — компонентная модель, hot-reload, типобезопасность; минусы — Node-тулчейн, шаг
сборки в CI, зависимость от совместимости с версией Keycloak. Для чистого брендинга
(наш случай) FreeMarker + CSS проще и достаточно.

## Bench

`scripts/bench.sh` гоняет одинаковую нагрузку (N воркеров × M запросов на `/me`) в двух
режимах backend-go: `AUTH_MODE=jwks` (локальная проверка подписи по JWKS) и
`AUTH_MODE=introspect` (RFC 7662 introspection на каждый запрос). Нагрузчик работает
**внутри docker-сети** стенда — NAT Docker Desktop на Windows искажает latency при замере
с хоста. Число обращений к Keycloak считается по метрике
`http_server_requests_seconds_count{uri=".../token/introspect"}` на порту 9000.

Параметры по умолчанию — **20 воркеров × 2000 + 500 warmup** (env `WORKERS`/`REQ`/`WARMUP`).
Скрипт воспроизводим: интерпретатор Python определяется автоматически (`python3`, откат на
`python`), а `accessTokenLifespan` realm на время прогона расширяется до 900 с и **всегда
восстанавливается в исходные 300** через `trap … EXIT` (даже при сбое/`Ctrl-C`).

Реальные числа (**20 воркеров × 2000 = 40 000 запросов**, Keycloak 26.7.0 + PostgreSQL 16,
Docker Desktop):

| mode | p50 ms | p95 ms | p99 ms | throughput (req/s) | обращений к KC / запросов | ошибки |
|---|---|---|---|---|---|---|
| jwks | 1.18 | 3.89 | 5.57 | 12 599 | **0 / 40 000** | 0 |
| introspect | 4.19 | 11.02 | 16.16 | 3 928 | **40 500 / 40 000** | 0 |

- introspect p50 = **×3.5** к jwks; throughput jwks = **×3.2** к introspect.
- **jwks: 0** обращений к Keycloak на горячем пути (ключи закэшированы).
- **introspect: ровно 1** обращение к Keycloak на каждый запрос (40 000 замеряемых + 500 warmup).

При росте конкуренции (50 воркеров × 2000 = 100 000) introspection деградирует нелинейно
(p50 18.4 мс, throughput 1 450 req/s, часть запросов срывается в **503** — недоступность
IdP под нагрузкой, не 401: токен валиден, проверить его нечем), тогда как jwks
масштабируется без деградации. Вывод: дефолт для высоконагруженных resource server'ов —
локальная JWKS-валидация; introspection оправдана там, где нужна мгновенная реакция на
отзыв токена (или как короткоживущий кэш).

## Production

`docker-compose.prod.yml` — production-топология (для статьи 5). Отличия от dev:

- Keycloak в режиме **`start --optimized`** (`prod/Dockerfile` заранее `kc.sh build` с
  `--db=postgres --cache=ispn`), не `start-dev`.
- **2 реплики** Keycloak; кластеризация — **JDBC_PING** (дефолт с Keycloak 26.1): ноды
  находят друг друга через таблицу в общей БД, JGroups-канал с mTLS, distributed
  Infinispan-кэш сессий. Дополнительных портов/multicast не нужно.
- **nginx LB** (`nginx/keycloak-lb.conf`) — TLS-offload: клиент → HTTPS `:8443` (nginx
  терминирует self-signed) → HTTP к репликам в доверенной docker-сети;
  `KC_PROXY_HEADERS=xforwarded` + `KC_HOSTNAME=https://localhost:8443`.
- **TLS**: `certs/gen.sh` генерирует self-signed сертификат (RSA-2048, SAN
  localhost/keycloak/keycloak-1/keycloak-2). Сами `.pem` — в `.gitignore`, не в репо.
- **`/metrics`** на management-порту (в метриках видны distributed-кэши сессий).

Проверено живьём: кластер из 2 нод (ISPN cluster view `(2)`), **распределённая сессия**
(login на реплике-1 → refresh + userinfo на реплике-2 = HTTP 200), round-robin по LB
4/4, `/metrics` = 200 на обеих нодах. Prod-realm — без brokering (`start` отклоняет
http-URL внешнего IdP при `sslRequired=external`; `realm-prod/gen.sh` детерминированно
снимает `identityProviders` из канонического realm).

> **Честная рамка отказоустойчивости.** Стенд демонстрирует **HA вычислительного слоя
> Keycloak** (2 реплики в Infinispan-кластере, распределённые сессии — падение одной
> реплики не роняет вход), но **не полную HA всей системы**. `postgres` и `nginx` — по
> одному инстансу, и каждый из них SPOF (единая точка отказа). Для настоящей HA нужны
> реплицированная/HA-БД (Patroni + PgBouncer, Stolon или управляемый облачный PostgreSQL
> с автоfailover) и отказоустойчивый балансировщик (пара nginx/HAProxy + keepalived/VIP
> или внешний облачный LB). Это осознанное упрощение — цель стенда показать именно
> кластер Keycloak, а обвязка HA БД и сети вынесена за рамки (см. комментарии в
> `docker-compose.prod.yml`).

Поднять / погасить:

```bash
bash certs/gen.sh
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml down -v
```

## Backup и восстановление (DR)

Единственный источник ДОЛГОВЕЧНОГО состояния Keycloak — внешний PostgreSQL (краткоживущий
runtime-стейт — незавершённые флоу, brute-force-счётчики — живёт только в кэшах), поэтому
бэкап сводится к дампу этой БД, а восстановление — к заливке дампа в чистый PostgreSQL. `scripts/backup-restore.sh`
показывает это **end-to-end, а не на словах**:

```bash
bash scripts/backup-restore.sh   # стенд должен быть поднят (или скрипт поднимет сам)
```

Сценарий: снять базовый токен `bob` (200) → завести «канарейку» `dr-canary-<ts>` (её нет
в `realm/demo-realm.json`, только в БД) → `pg_dump` (показывает размер дампа) → **авария**
`docker compose down -v` (том `pgdata` уничтожается) → поднять чистый PostgreSQL →
восстановить дамп (`psql`) → поднять Keycloak → **проверки**: realm `demo` на месте,
канарейка восстановлена (доказывает, что данные пришли **из бэкапа**, а не из повторного
`--import-realm`), токен `bob` снова выдаётся (HTTP 200). При любом расхождении скрипт
выходит с ненулевым кодом.

Два уровня резервирования дополняют друг друга: **дамп БД** (полное состояние: пользователи,
сессии, связи) и **realm как код** (`realm/demo-realm.json` — декларативная конфигурация,
импортируется на пустой БД). В production дамп снимается по расписанию (`pg_dump`/PITR
средствами БД), realm-экспорт хранится в git.

## Версии (пиновка)

Поведение Keycloak меняется между мажорами — версии зафиксированы точными тегами:

| Компонент | Версия |
|---|---|
| Keycloak | `quay.io/keycloak/keycloak:26.7.0` |
| PostgreSQL | `postgres:16-alpine` |
| Go (module) | `go 1.25` (`go.mod`); сборка образа — `golang:1.26-alpine` |
| go-oidc | `github.com/coreos/go-oidc/v3 v3.16.0` |
| Java | 21 (Temurin) |
| Spring Boot | 3.4.2 |
| mock-oauth2-server | `ghcr.io/navikt/mock-oauth2-server:2.1.10` |

## Структура

```
security/keycloak/
  docker-compose.yml            # dev-стенд: KC + PG + Go + Java + mock-OIDC (+ bench под профилем)
  docker-compose.prod.yml       # production: 2 реплики + nginx LB + TLS
  realm/demo-realm.json         # экспорт realm demo (клиенты, роли, юзеры, mappers, IdP)
  realm-prod/                   # prod-вариант realm (без brokering) + gen.sh
  backend/go/                   # Go resource server (:8081)
  backend/java/                 # Spring Boot resource server (:8082)
  bench/                        # Go-нагрузчик (JWKS vs introspection)
  mock-oidc/config.json         # конфиг внешнего OIDC-провайдера
  themes/khorost/login/         # кастомная login-тема + screenshot-login.png
  nginx/keycloak-lb.conf        # LB-конфиг для prod
  certs/gen.sh                  # генерация self-signed TLS (сами .pem — в .gitignore)
  scripts/                      # up / get-token / call-apis / down / broker-demo / bench / backup-restore
```

## Teardown

```bash
bash scripts/down.sh                                 # dev: контейнеры + сеть + volume pgdata
docker compose -f docker-compose.prod.yml down -v    # prod
```

`down -v` удаляет БД — следующий `up.sh` поднимает стенд с нуля и заново импортирует
realm `demo` из `realm/demo-realm.json` (демо-пользователи/клиенты воспроизводятся из
файла).

---

Часть [digital-cookbook](https://github.com/khorost/digital-cookbook) — живых примеров к
статьям [khorost.tech](https://khorost.tech). Лицензия MIT.
