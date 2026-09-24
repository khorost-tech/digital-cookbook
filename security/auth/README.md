# auth — живой стенд к серии «Авторизация: токены, сессии, refresh»

Живой стенд к серии **«Авторизация: токены, сессии, refresh»** (khorost.tech, раздел security):

1. [Модели сессий: opaque vs JWT, один сервис против многих](https://khorost.tech/security/auth-session-models-opaque-vs-jwt/) — обзорная, кода не требует
2. [Простой путь: один сервис и opaque-сессии в Redis](https://khorost.tech/security/auth-single-service-opaque-sessions-redis/) → `single-service/`
3. [Сложный путь: выделенный auth, JWT и refresh в Redis](https://khorost.tech/security/auth-multi-service-jwt-refresh-redis/) → `multi-service/`
4. [Вход и регистрация: email-код, OAuth, OTP, Telegram](https://khorost.tech/security/auth-login-registration-oauth-otp-telegram/) → `login-methods/`
5. [Refresh на клиенте: несколько устройств и вкладок](https://khorost.tech/security/auth-client-refresh-multi-device-tabs/) → `client-refresh/`
6. OAuth2/OIDC вглубь → `oidc/` (+ `login-methods/mockidp`)
7. MFA и TOTP → `totp/`
8. Passkeys и WebAuthn → `webauthn/`

Один Go-модуль (`khorost.tech/cookbook/auth`, go 1.26) с независимо запускаемыми демо-сервисами
(ядровые пять + доборы `oidc`/`totp`/`webauthn`) + `docker-compose.yml`, поднимающим весь стенд
разом. Паттерны обезличены из двух приватных проектов автора (см. spec-документ дизайна) —
санитайзированы, без реальных доменов и секретов.

> Код учебный. Демо-секреты (`devsecret`, фиктивный `TELEGRAM_BOT_TOKEN`) — очевидно тестовые,
> для прогона стенда локально. Честные упрощения относительно продакшена перечислены в разделе
> [Компромиссы](#честные-компромиссы-demo) — не переносите их в боевой код без сознательного решения.

## Demo ↔ статья

| Папка | Порт | Статья | Что показывает |
|---|---|---|---|
| `single-service/` | 8081 | ст.2 | opaque-сессии в Redis (`s:`/`su:`), email-код без пароля, rate-limit, список сессий, logout |
| `multi-service/` | 8082 (auth) + 8083 (consumer) | ст.3 | выделенный auth-сервис: JWT access (HS256) + opaque refresh в Redis (`rs:`/`rsu:`), consumer валидирует JWT локально, без похода в auth |
| `login-methods/` | 8084 (+ встроенный `/mockidp`) | ст.4 | OTP, OAuth (mock-OIDC + PKCE), Telegram (widget-HMAC и OAuth-вариант) — разные методы входа сходятся в одну сессию |
| `client-refresh/` | 8085 | ст.5 | ротация refresh с grace-period + lock + replay на сервере; BroadcastChannel + Web Locks на клиенте против гонки вкладок |
| `oidc/` | — (клиент `login-methods/mockidp`) | ст.6 «OAuth2/OIDC вглубь» | локальная JWKS-валидация id_token (RS256, alg-confusion отклоняется) + device flow client (RFC 8628) |
| `totp/` | 8086 | ст.7 «MFA и TOTP» | enrollment/verify TOTP (RFC 6238, skew ±1), анти-replay, recovery-коды, step-up |
| `webauthn/` | 8087 (Go) + 8088 (Java) | ст.8 «Passkeys и WebAuthn» | passwordless: два независимых верификатора-бэкенда (go-webauthn / webauthn4j) на одном контракте, frontend + Playwright virtual authenticator |

`internal/token`, `internal/rstore`, `internal/session`, `internal/jwtclaims` — общие пакеты
(opaque-токены, обёртка над go-redis, refresh-сессии `rs:`/`rsu:`, выпуск/парсинг JWT).
`multi-service/authmw` — общее middleware локальной JWT-валидации, используется и в
`multi-service/consumer`, и в `client-refresh`.

## Как запустить

```bash
cd security/auth
GOPROXY=https://go.khorost.tech,direct docker compose up -d --build
docker compose ps      # redis + 8 сервисов (5 ядровых + 3 добора), все Up/healthy
```

Порты: `single-service` 8081, `authservice` 8082, `consumer` 8083, `login-methods` 8084,
`client-refresh` 8085, `totp` 8086, `webauthn-go` 8087, `webauthn-java` 8088, `redis` 6379.

Smoke-проверка каждого сервиса (значения — фактический вывод живого прогона):

```bash
curl -s -XPOST localhost:8081/auth/send-code -H 'content-type: application/json' -d '{"email":"a@b.c"}' \
  -o /dev/null -w "single-service send-code %{http_code}\n"
# single-service send-code 204

curl -s -c cj.txt -XPOST localhost:8082/auth/login -H 'content-type: application/json' -d '{"email":"bob@ex.co"}' \
  -o /dev/null -w "multi login %{http_code}\n"
# multi login 200

curl -s -b cj.txt localhost:8083/me -o /dev/null -w "consumer me %{http_code}\n"
# consumer me 200

curl -s localhost:8083/debug/auth-calls -w " auth-calls\n"
# {"auth_calls":0} auth-calls   ← consumer ни разу не сходил в authservice

curl -s -XPOST localhost:8084/login/otp/request -H 'content-type: application/json' -d '{"email":"a@b.c"}' \
  -o /dev/null -w "login-methods otp %{http_code}\n"
# login-methods otp 204

curl -s localhost:8085/?mode=coordinated -o /dev/null -w "client-refresh web %{http_code}\n"
# client-refresh web 200

curl -s -XPOST localhost:8086/totp/enroll -H 'content-type: application/json' -d '{"user_id":"u1"}' \
  -o /dev/null -w "totp enroll %{http_code}\n"
# totp enroll 200

curl -s -XPOST localhost:8087/register/begin -H 'content-type: application/json' -d '{"username":"u1"}' \
  -o /dev/null -w "webauthn-go register/begin %{http_code}\n"
# webauthn-go register/begin 200

curl -s -XPOST localhost:8088/register/begin -H 'content-type: application/json' -d '{"username":"u1"}' \
  -o /dev/null -w "webauthn-java register/begin %{http_code}\n"
# webauthn-java register/begin 200

curl -s localhost:8084/mockidp/.well-known/openid-configuration -o /dev/null -w "mockidp discovery %{http_code}\n"
# mockidp discovery 200

docker compose down
rm -f cj.txt
```

Остальные маршруты сервисов:

| Сервис | Маршруты |
|---|---|
| `single-service` (8081) | `POST /auth/send-code`, `POST /auth/verify-code`, `GET /auth/sessions`, `POST /auth/logout`, `DELETE /auth/sessions/{sid}` |
| `authservice` (8082) | `POST /auth/login`, `POST /auth/refresh`, `GET /auth/sessions` |
| `consumer` (8083) | `GET /me` (JWT из cookie, локальная валидация), `GET /debug/auth-calls` (счётчик обращений к authservice — demo-метрика) |
| `login-methods` (8084) | `POST /login/otp/request`, `POST /login/otp/verify`, `GET /oauth/{provider}/start`, `GET /oauth/{provider}/callback`, `POST /login/telegram/widget`, `POST /login/telegram/oauth`, встроенный `/mockidp` (см. ниже) |
| `mockidp` (встроен в 8084, `/mockidp`) | `GET /.well-known/openid-configuration`, `GET /.well-known/jwks.json`, `GET /authorize`, `POST /token` (code+PKCE, `client_credentials`, `device_code`), `GET /userinfo`, `POST /introspect`, `POST /device_authorization`, `POST /device/approve` |
| `totp` (8086) | `POST /totp/enroll`, `POST /totp/verify`, `POST /totp/recovery`, `POST /sensitive` (step-up-защищённое действие) |
| `webauthn-go` (8087) | `POST /register/begin`, `POST /register/finish`, `POST /login/begin`, `POST /login/finish` |
| `webauthn-java` (8088) | тот же контракт, что `webauthn-go`: `POST /register/begin`, `POST /register/finish`, `POST /login/begin`, `POST /login/finish` |
| `client-refresh` (8085) | `POST /auth/login`, `POST /auth/refresh`, `GET /api/ping` (под access-токеном), статика `web/` на `/` (`?mode=coordinated`\|`naive`) |

Локально без Docker любой сервис запускается напрямую (нужен Redis на `localhost:6379`):

```bash
GOPROXY=https://go.khorost.tech,direct go run ./single-service
```

Go-тесты: `GOPROXY=https://go.khorost.tech,direct go test ./...`. Java-верификатор WebAuthn
тестируется через контейнер Maven — **с флагом `--user`, чтобы `target/` не создавался как
root** (иначе следующий прогон упрётся в права на `target/classes` и `mvn` не воспроизведётся):

```bash
cd webauthn/backend-java
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:/app" -w /app \
  maven:3.9-eclipse-temurin-21 mvn -q -B -Dmaven.repo.local=/tmp/.m2 test
```

`target/` — в `.gitignore` (артефакт сборки, не коммитится). Если он уже принадлежит root от
прежнего прогона без `--user` — почистить тем же контейнером: `docker run --rm -v "$PWD:/app"
-w /app maven:3.9-eclipse-temurin-21 rm -rf target`.

## Что доказывает стенд

Числа ниже — из реального прогона на момент финальной сборки стенда (Windows,
AMD Ryzen 7 5800X3D), не из документации и не предположения.

**ст.2 — opaque-сессии.** Отзыв мгновенный: `DELETE /auth/sessions/{sid}` удаляет `HASH s:{sid}`
и запись в `su:{userID}` синхронно, следующий запрос с этим sid получает `ErrNoSession`. Лимит
попыток входа держится двумя счётчиками (`auth:rl:{email}:min|hour`) — без Postgres в горячем пути
вообще: вся сессионная логика — Redis-структуры с TTL.

**ст.3 — локальная валидация JWT vs поход в auth.** `consumer` проверяет access-токен на
каждый `/me` через общий секрет (`multi-service/authmw`), не обращаясь к `authservice` —
`/debug/auth-calls` после серии запросов показывает **0**. Свежий bench
(`go test ./multi-service/bench/ -bench . -benchmem`, эмуляция round-trip — искусственная
задержка 1мс на стороне сервера, как аналог сетевого похода в выделенный auth):

| Бенчмарк | ns/op | B/op | allocs/op |
|---|---|---|---|
| `BenchmarkLocalValidate` | 4 681 | 2 864 | 50 |
| `BenchmarkRoundTrip` | 1 509 925 | 4 803 | 57 |

Локальная валидация (~4.7 мкс) быстрее эмулированного похода в auth (~1.51 мс) примерно в
**322 раза** — числовое обоснование архитектуры «валидировать JWT локально, не дёргать auth на
каждый запрос».

**ст.4 — методы входа.** OTP, OAuth (mock-OIDC + PKCE S256) и Telegram (widget-HMAC/OAuth) —
три независимых способа аутентификации. Демо намеренно даёт им разные account-ID: это fail-safe
против неявного авто-merge профилей разных методов на один аккаунт по email/id без явного шага
привязки (описано как компромисс, не решённая проблема — реальное объединение методов требует
отдельного flow привязки, вне рамок стенда).

**ст.5 — ротация refresh на сервере.** Concurrency-тесты (`go test ./client-refresh/ -run
'TestConcurrent|TestNaive' -count=3 -v`, 3 независимых прогона):

| Тест | Результат (3 прогона) |
|---|---|
| `TestConcurrentRefreshNoFalseLogout` (grace + lock + replay) | PASS, **0** ложных разлогинов во всех трёх прогонах |
| `TestNaiveConcurrentProducesFalseLogouts` (наивная ротация, контраст) | **17/20**, **18/20**, **18/20** горутин получают `ErrTokenInvalid` (ложный разлогин) |

**ст.5 — координация на клиенте.** Playwright-замер гонки вкладок (`client-refresh/e2e/`, живой
прогон против поднятого `client-refresh`, 3 независимых запуска):

| Режим | Сетевых `/auth/refresh` | Ложных разлогинов |
|---|---|---|
| `coordinated` (BroadcastChannel + Web Locks) | **1** (все 3 прогона) | **0** (все 3 прогона) |
| `naive` (каждая вкладка обновляет независимо) | **5** (все 3 прогона) | **1/5, 1/5, 0/5** |

Координация стабильно сводит 5 независимых запросов вкладок к одному сетевому refresh и не даёт
ложных разлогинов. Наивный режим стабильно шлёт все 5 запросов; итог по разлогинам в браузере
менее детерминирован, чем в контролируемой Go-гонке (там — счётчик через искусственную задержку
внутри процесса; в браузере — реальный race между вкладками), но в большинстве прогонов даёт
минимум один ложный разлогин, воспроизводя проблему, которую решает ст.5.

**ст.6 — OAuth2/OIDC вглубь.** `login-methods/mockidp` — учебный OIDC-провайдер: RS256-подпись
`id_token` с ключом, публикуемым через JWKS (`/.well-known/jwks.json`), OIDC Discovery
(`/.well-known/openid-configuration`), authorization code + PKCE (уже используется ст.4),
`client_credentials` (чистый OAuth2-grant, без `id_token`), device authorization flow (RFC 8628:
`/device_authorization` → `/device/approve` → поллинг `/token`) и token introspection (RFC 7662,
`/introspect` под client-аутентификацией — намеренно НЕ раскрывает причину неактивности токена,
отдаёт лишь `active: true/false`, чтобы не давать атакующему различать «не существует» и
«истёк/отозван»). Клиент `oidc/`
демонстрирует обе стороны последнего шага доверия: `ValidateIDToken` тянет JWKS и проверяет
подпись id_token локально по `kid` — **только `RS256`** принимается как метод подписи, любой
другой (включая `none` и `HS256` с публичным RSA-модулем в роли HMAC-секрета) отклоняется до
проверки подписи (`ErrUnexpectedAlg`), что закрывает classic alg-confusion; `RunDeviceFlow`
реализует клиентскую часть device flow — poll `/token` с уважением к `authorization_pending`/
`slow_down`, таймаут по `expires_in` (`ErrDeviceFlowTimedOut`). За контрастом introspection vs
локальная JWT-валидация на реальном бенче (не эмуляции) — `security/keycloak` (отдельный стенд,
готовый IdP).

**ст.7 — MFA и TOTP.** `totp/` — TOTP по RFC 6238 (SHA1, 6 цифр, период 30с) с окном допуска
±1 период (`totpSkew`), компенсирующим рассинхрон часов клиента. Анти-replay — не проверка «код
похож на использованный», а атомарный Lua-скрипт (`acceptStepScript`), сравнивающий предъявленный
time-step с `last_step`, сохранённым для пользователя, и продвигающий его только вперёд: код с
step ≤ `last_step` отклоняется, поэтому один и тот же код не может быть предъявлен дважды в
пределах ~60–90с окна допуска (`TestVerifyRejectsReplay` в живом прогоне подтверждает: тот же код
второй раз — отказ). Recovery-коды — 10 одноразовых кодов на enrollment, в Redis хранится только
`sha256`, а не сам код (`TestRecoverySingleUse` — код валиден один раз). Step-up
(`MarkStepUp`/`RequireStepUp`, `stepup:{sessionID}`, TTL 5 минут) — паттерн «свежее MFA-
подтверждение для чувствительного действия»: `POST /sensitive` без свежего подтверждения отдаёт
`403 step_up_required` даже с валидной первичной сессией (`TestStepUpExpires` — подтверждение
протухает по истечении TTL).

**ст.8 — Passkeys и WebAuthn.** `webauthn/` — passwordless-вход: два независимых
верификатора-бэкенда на одном HTTP-контракте (`/register/begin|finish`, `/login/begin|finish`) —
Go (`go-webauthn` v0.17.4, `backend-go/`, :8087) и Java (Spring Boot + `webauthn4j` 0.31.8,
`backend-java/`, :8088) — контраст реализаций для статьи. Оба проверяют origin/rpId binding
(запрос с чужого origin отклоняется до какой-либо криптографии) и signature counter — механизм
обнаружения клонированных аутентификаторов: если counter не увеличился между церемониями,
ceremony отклоняется (`CloneWarning` в go-webauthn, явный explicit-reject отката counter в
Java-сервисе, т.к. webauthn4j по умолчанию сам бросает исключение при откате — поведение
воспроизведено намеренно, а не обойдено). Frontend (`web/`, `navigator.credentials.create/get`) и
Playwright-набор (`e2e/webauthn.spec.ts`) гоняют один и тот же сценарий регистрации+входа через
virtual authenticator (CTAP2, платформенный, resident key) против **обоих** бэкендов
(`BASE_GO`/`BASE_JAVA`) — не только unit-тесты верификации подписи, но и полный HTTP-цикл
браузер↔сервер.

## Схема ключей Redis

Сводно по всем сервисам (namespace — общий, сервисы не пересекаются по префиксам):

| Префикс | Тип | Кто пишет | Назначение |
|---|---|---|---|
| `s:{sid}` | HASH | `single-service` | opaque-сессия (`uid`, `cat`, `lat`) |
| `su:{userID}` | SET | `single-service` | индекс живых `sid` пользователя (для листинга/logout) |
| `auth:pending:{email}` | HASH | `single-service` | ожидающий подтверждения email-код (magic-code) |
| `auth:{email}` | HASH | `login-methods` | ожидающий подтверждения OTP-код (`token`, `code`, `attempts`) |
| `auth:rl:{email}:min` / `:hour` | STRING (счётчик) | `single-service`, `login-methods` | rate-limit запросов кода/OTP |
| `rs:{refresh}` | HASH | `multi-service` (authservice), `client-refresh` | refresh-сессия (`aid`, `cat`, `lat`, `lm`, `nick`, `roles` — `roles` как JSON-массив строк) |
| `rsu:{accountID}` | HASH (индекс) | `multi-service`, `client-refresh` | живые refresh-токены аккаунта, per-field TTL |
| `rl:{accountID}` | STRING (`SET NX`) | `client-refresh` | refresh-lock — single-flight на ротацию одного аккаунта |
| `rotated:{oldRefresh}` | STRING | `client-refresh` | grace-мост: старый refresh → новый, TTL на replay-окно |
| `oauth:state:{state}` | STRING | `login-methods` | CSRF-state + PKCE-verifier на время OAuth-флоу, одноразовый (`GETDEL`) |
| `totp:{userID}` | HASH | `totp` | `secret`, `last_step` (анти-replay), `recovery:{sha256(code)}` (одноразовые recovery-коды) |
| `totp:rl:{userID}` | STRING (счётчик) | `totp` | анти-брутфорс `/totp/verify`: неудачные попытки, лимит 5 за 60с (`ErrTOTPRateLimited`), сбрасывается успешным verify |
| `stepup:{sessionID}` | STRING (`SET`) | `totp` | свежее MFA-подтверждение сессии, TTL 5 минут (step-up) |
| `webauthn:user:{username}` | STRING (JSON) | `webauthn-go` | учётная запись + credentials пользователя (`Get`/`Set` JSON-документа, не HASH) |
| `webauthn:cred:{credID}` | STRING | `webauthn-go` | обратный указатель credID → username |
| `webauthn:session:{kind}:{username}` | STRING | `webauthn-go` | временное состояние WebAuthn-церемонии (`register`/`login`) между begin/finish |

`webauthn-java` хранит состояние **в памяти процесса** (`WebauthnStore`, без Redis — см.
[Честные компромиссы](#честные-компромиссы-demo)), поэтому в схеме ключей Redis не участвует.
Device authorization flow (`mockidp`, `/device_authorization`) тоже хранится в памяти процесса
(мапы `devices`/`deviceUserCodes` внутри `login-methods`), не в Redis — demo не переживает рестарт
контейнера между `device_authorization` и подтверждением, чего для учебного flow достаточно.

`sel:` (account switching / multi-account picker) в стенде **не реализован** — упомянут в дизайне
исходных проектов, но за рамки демо не выносился.

## Честные компромиссы demo

Список того, что упрощено ради читаемости стенда — не переносить в прод без переосмысления:

- **Rate-limit — check-then-act, не атомарен.** `checkAndBumpOTPRateLimit` и аналог в
  `single-service` сначала читают счётчики, потом инкрементируют отдельной командой — под
  настоящей конкурентной нагрузкой возможен небольшой overshoot лимита. В проде — атомарный
  `INCR` + `EXPIRE NX` или Lua-скрипт.
- **`rl:{accountID}` — `SET NX` без ownership-токена.** Побеждает тот, кто первым поставил лок, но
  снять его может кто угодно, вызвавший `Del` по тому же ключу (в demo это не эксплуатируется, но
  структурно это не redlock). В проде — уникальный токен владения + compare-and-delete (Lua) или
  полноценный Redlock при нескольких Redis-инстансах.
- **Telegram OAuth-вариант не проверяет подпись `id_token`.** В отличие от canonical
  widget-flow (HMAC по `bot_token`, проверяется), OAuth-вариант декодирует payload без
  верификации подписи — оставлен как контраст «на что это похоже, если сделать не тем путём»,
  не как рекомендуемая практика.
- **`mockidp` — учебный OIDC-провайдер.** Достаточно для authorization code + PKCE flow демо, но
  не полноценный OIDC (нет ротации ключей, discovery упрощён, не проверяются все обязательные
  claims).
- **Bench round-trip — эмуляция, не сеть.** `BenchmarkRoundTrip` не ходит в реальный `authservice`
  по сети, а эмулирует его артифициальной задержкой 1мс в `httptest.Server` — число показывает
  порядок величины (поход в отдельный сервис ощутимо дороже локальной проверки подписи), не
  измерение конкретной инфраструктуры.
- **OAuth `state` защищает от подделки, но не от login-CSRF/fixation целиком.** `state` —
  одноразовый (`GETDEL`), поэтому подделанный/повторно использованный `state` отклоняется. Но он
  не привязан к браузеру, инициировавшему запрос (нет double-submit state-cookie) — классическая
  атака login-CSRF/session-fixation, где злоумышленник подсовывает жертве СВОЙ (валидный, не
  подделанный) authorization-код через её браузер, закрыта не полностью. В проде — дополнительная
  state-cookie с тем же значением, сверяемая на callback (double-submit).
- **`mockidp` редиректит на `redirect_uri` без whitelisting.** Это open redirect: `mockidp`
  принимает `redirect_uri` из запроса как есть и не сверяет его со списком зарегистрированных для
  клиента адресов. Приемлемо для учебного провайдера, но реальный IdP обязан отклонять
  `redirect_uri`, не входящий в заранее зарегистрированный список.
- **`ValidateAndTouch` продлевает TTL `rs:`, но не per-field TTL записи в `rsu:`.** `rsu:{accountID}`
  получает per-field TTL (`HExpire`) только в момент `CreateTokenPair`. При непрерывной активности
  дольше `RefreshTTL` (168ч) без нового логина запись о живой сессии может истечь и пропасть из
  индекса `rsu:` раньше, чем сама `rs:{refresh}` — сессия при этом останется рабочей (аутентификация
  не сломана), но временно не будет видна в `GET /auth/sessions`, пока `rs:` тоже не истечёт.
- **Частичный сбой `CreateTokenPair`.** Если пайплайн (`HSet`/`Expire`/`HSet`) выполнился успешно, а
  последующий отдельный `HExpire` на `rsu:` — нет (сетевой сбой между командами), возможна
  осиротевшая запись в `rsu:` без per-field TTL. Она не блокирует ничего функционально: `rs:{refresh}`
  всё равно самоистечёт по `RefreshTTL`, и `ListSessions` самоочищает такие ссылки при следующем чтении.
- **Отказ Redis везде отдаёт 500 (`internal_error`), а не 503.** Стенд не различает «Redis временно
  недоступен» (ретраябельно, 503) и «внутренняя ошибка сервиса» (500) — оба случая сворачиваются в
  один `internal_error`/500. В проде это разные сигналы для клиента и алертинга.
- **FIX I-1 (закрыт): nick/roles больше не теряются при refresh.** Раньше выпуск нового access при
  `/auth/refresh` и в `client-refresh`-ротации использовал только `aid` из `rs:{refresh}` — новый
  access приходил с пустыми `nick`/`roles`, и `RequireRole` на потребителях молча отклонял запросы
  после первого refresh (403 без явного объяснения). Теперь `CreateTokenPair` пишет `nick`/`roles`
  в `rs:{refresh}` при логине, а `AccountFromSession` восстанавливает их при refresh/rotation — см.
  таблицу ключей выше.
- **`mockidp` — RSA-ключ подписи эфемерный, per-process.** `rsa.GenerateKey` вызывается один раз
  при старте процесса — при рестарте `login-methods` ключ меняется (и старые id_token/JWKS `kid`
  становятся невалидны), ротации ключей нет. Для учебного провайдера это ожидаемо (демо не должно
  переживать рестарт с валидными старыми токенами), в проде — управляемая ротация ключей + перекрытие
  старого/нового `kid` на переходный период.
- **`client_credentials` — сравнение `client_secret` не constant-time.** `clientSecret !=
  demoClientCredentialsSecret` — обычное сравнение строк Go, потенциально timing-атакуемое (хотя
  практическая эксплуатируемость по сети мала). В проде — `subtle.ConstantTimeCompare` или сравнение
  хэшей.
- **TOTP-секрет хранится в открытом виде.** `totp:{userID}` хранит `secret` как есть, не шифрованным
  at-rest — компрометация Redis раскрывает секреты всех пользователей разом. В проде — шифрование
  секрета симметричным ключом вне Redis (KMS/HSM) перед записью. Анти-replay (`last_step`)
  инвалидирует старые предъявленные step'ы необратимо: если легитимный клиент и атакующий предъявили
  код почти одновременно, побеждает тот, кто раньше — это by design (иначе replay не закрыт), но
  означает, что повторный ввод того же кода пользователем (например, после случайного двойного клика)
  тоже будет отклонён.
- **WebAuthn Java — `createNonStrictWebAuthnManager` + in-memory store.** Non-strict-менеджер
  ослабляет часть built-in проверок webauthn4j (сравнимо по строгости с demo-уровнем `backend-go`),
  и `WebauthnStore` живёт в памяти процесса — без Redis, рестарт контейнера теряет все
  зарегистрированные credentials. В проде — strict-режим + персистентное хранилище.
- **WebAuthn — счётчик не защищает zero-counter аутентификаторы.** Некоторые platform-аутентификаторы
  (в т.ч. виртуальные, использованные в `e2e/`) всегда репортуют `signCount=0` и не реализуют
  signature counter вообще — оба бэкенда (go-webauthn и явный explicit-reject в Java) пропускают
  clone-detection для таких кредов (условие `signCount>0 || storedCount>0` перед проверкой отката),
  иначе легитимные zero-counter аутентификаторы никогда бы не прошли login. Это осознанный компромисс
  протокола WebAuthn, не баг стенда: против клонирования конкретно таких аутентификаторов counter
  ничего не доказывает.
- **WebAuthn — `excludeCredentials`/`pubKeyCredParams` demo-упрощены.** ОБА бэкенда не заполняют
  `excludeCredentials` при регистрации (go-webauthn v0.17.4 тоже не проставляет его по умолчанию —
  `BeginRegistration` вызывается без `WithExclusions`, как и Java-бэкенд без явного заполнения) —
  повторная регистрация того же аутентификатора для одного пользователя не блокируется на уровне
  опций creation и даёт второй credential в ОБОИХ бэкендах. Java-бэкенд дополнительно не ограничивает
  `pubKeyCredParams` набором алгоритмов сервера при проверке (`null` — все алгоритмы допустимы),
  полагаясь на то, что реальный аутентификатор сам предложит поддерживаемый алгоритм. В проде —
  явный whitelist алгоритмов и `excludeCredentials` для UX (не давать пользователю зарегистрировать
  один и тот же ключ дважды).
- **Device flow — интервал поллинга укорочен.** `deviceInterval` = 1с (вместо типичных 5с у реальных
  провайдеров) — чтобы demo и e2e-прогоны не ждали лишнего; RFC 8628 не запрещает короткий интервал,
  но в проде значение подбирается с учётом нагрузки на `/token` от множества устройств одновременно.
- **Cookie-аутентификация полагается только на `SameSite`, полноценного CSRF-механизма нет.** Сервисы с
  cookie-сессиями (`single-service`, `multi-service`, `client-refresh`) ставят cookie `SameSite=Strict`/`Lax`,
  но НЕ показывают явную anti-CSRF-защиту для изменяющих состояние cookie-запросов (`POST /auth/logout`,
  `DELETE /auth/sessions/{sid}` и т.п.) — ни синхронизированного CSRF-токена (double-submit), ни проверки
  заголовка `Origin`/`Referer`. `SameSite=Strict` закрывает классический cross-site CSRF, но не является
  полной заменой CSRF-middleware: `SameSite=Lax` (частый дефолт), старые браузеры и часть навигационных
  сценариев оставляют щели. В проде для cookie-auth POST/DELETE добавляют явный CSRF-механизм поверх `SameSite`.

## Версии

| Компонент | Версия |
|---|---|
| Go (module) | `go 1.26` |
| go-redis | `github.com/redis/go-redis/v9 v9.21.0` |
| golang-jwt | `github.com/golang-jwt/jwt/v5 v5.3.1` |
| chi | `github.com/go-chi/chi/v5 v5.3.1` |
| pquerna/otp | `github.com/pquerna/otp v1.5.0` (`totp/`) |
| go-webauthn | `github.com/go-webauthn/webauthn v0.17.4` (`webauthn/backend-go/`) |
| Java | `21` (`webauthn/backend-java/`, контейнер `maven:3.9-eclipse-temurin-21`) |
| Spring Boot | `3.5.16` |
| webauthn4j | `com.webauthn4j:webauthn4j-core 0.31.8.RELEASE` |
| Redis | `redis:7.4-alpine` |
| Playwright | `@playwright/test ^1.55.0` (`client-refresh/e2e/`, `webauthn/e2e/`) |

## Структура

```
security/auth/
  go.mod                       # один модуль khorost.tech/cookbook/auth
  docker-compose.yml           # redis + все 8 сервисов (5 ядровых + 3 добора)
  internal/
    token/                     # opaque hex-токены
    rstore/                    # обёртка над go-redis
    session/                   # refresh-сессии rs:/rsu: (multi-service, client-refresh)
    jwtclaims/                 # выпуск/парсинг JWT (HS256)
  single-service/              # ст.2 — opaque-сессии в Redis (:8081)
  multi-service/
    authservice/                # ст.3 — выпуск JWT + refresh (:8082)
    consumer/                   # ст.3 — локальная валидация JWT (:8083)
    authmw/                     # общее middleware JWT-валидации
    bench/                      # local-validate vs round-trip
  login-methods/                # ст.4 — OTP + OAuth(mock-OIDC) + Telegram (:8084) + mockidp/
    mockidp/                     # ст.6 — учебный OIDC-провайдер: RS256/JWKS/discovery/client_credentials/device flow/introspect
  client-refresh/                # ст.5 — ротация grace/lock/replay (:8085)
    web/                         # BroadcastChannel + Web Locks (coordinated/naive)
    e2e/                          # Playwright-замер гонки вкладок
  oidc/                          # ст.6 — клиент: локальная JWKS-валидация id_token + device flow client
  totp/                          # ст.7 — TOTP enrollment/verify/recovery/step-up (:8086)
  webauthn/                      # ст.8 — Passkeys и WebAuthn
    backend-go/                   # верификатор go-webauthn (:8087)
    backend-java/                 # верификатор webauthn4j, Spring Boot (:8088)
    web/                          # frontend, navigator.credentials
    e2e/                          # Playwright virtual authenticator (оба бэкенда)
```

## Teardown

```bash
docker compose down          # контейнеры + сеть; volume у стенда нет (Redis без persistence)
```

---

Часть [digital-cookbook](https://github.com/khorost/digital-cookbook) — живых примеров к статьям
[khorost.tech](https://khorost.tech). Лицензия MIT.
