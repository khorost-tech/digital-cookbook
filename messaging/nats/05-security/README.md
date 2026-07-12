# 05-security — accounts, JWT (nsc), mTLS

Демо к статье [«Безопасность и multi-tenancy в NATS: accounts, JWT, nkeys и TLS»](https://khorost.tech/messaging/nats-security-accounts-jwt/).

**Всё в этом каталоге — демонстрационные секреты.** Пароли, приватные ключи, демо-CA — не для продакшна, сгенерированы только чтобы показать механику. Приватные ключи (`nsc/nsc-data`, `nsc/stores`, `*.creds`, все `tls/*.pem` кроме `ca.pem`) — в `.gitignore` и никогда не коммитятся.

Версия: `nats-server 2.12.x` (образ `nats:2.12`, проверено на `2.12.12`).

Три независимых сценария, каждый показывает свою грань безопасности NATS:

| Сценарий | Что показывает | Файлы |
|----------|-----------------|-------|
| (а) Статические accounts | Изоляция subject-namespace между двумя accounts + один export/import + permissions | `static-accounts.conf` |
| (б) mTLS | Обязательная взаимная TLS-аутентификация (`verify: true`) | `mtls.conf`, `gen-certs.sh`, `tls/` |
| (в) Decentralized JWT | Цепочка operator → account → user через `nsc`, resolver | `nsc/setup.sh`, `resolver.conf` |

## (а) Статические accounts: изоляция + export/import + permissions

`static-accounts.conf` поднимает один сервер с двумя accounts:

- **APP_A** — пользователь `alice` (`alice_demo_pw`), publish/subscribe ограничены `app.a.>`. Экспортирует публичный stream `app.a.public.>`.
- **APP_B** — пользователь `bob` (`bob_demo_pw`), publish/subscribe ограничены `app.b.>` (плюс `from-a.>` для приёма импорта). Импортирует `app.a.public.>` из APP_A, локально перекладывая под префикс `from-a.*`.

```bash
docker compose up -d nats-static
```

Сервер честно предупредит в логах `Plaintext passwords detected, use nkeys or bcrypt` — это ожидаемо для демо со статическими паролями; в проде — bcrypt-хеш пароля или переход на nkeys/JWT (см. ниже).

**Изоляция.** `bob` подписывается на «сырой» subject `app.a.public.demo` (без импорта) — получает permissions violation: у него нет права ни на этот subject, ни на чужой account namespace:

```bash
nats sub "app.a.public.demo" --server nats://localhost:4222 --user bob --password bob_demo_pw
```

```bash
nats pub app.a.public.demo "isolated" --server nats://localhost:4222 --user alice --password alice_demo_pw
# у bob: Permissions Violation for Subscription to "app.a.public.demo"
```

**Export/Import.** `bob` подписывается на `from-a.>` (разрешено его permissions и обслуживается импортом) — сообщение, опубликованное `alice` в `app.a.public.demo`, приходит к `bob` под локальным именем `from-a.app.a.public.demo`:

```bash
nats sub "from-a.>" --server nats://localhost:4222 --user bob --password bob_demo_pw
```

```bash
nats pub app.a.public.demo "via-import" --server nats://localhost:4222 --user alice --password alice_demo_pw
# у bob: [#1] Received on "from-a.app.a.public.demo" — via-import
```

**Permissions.** `alice` не может опубликовать за пределы `app.a.>`, даже в собственном account:

```bash
nats pub app.b.hack "nope" --server nats://localhost:4222 --user alice --password alice_demo_pw
# Permissions Violation for Publish to "app.b.hack"
```

Все три команды проверены на живом сервере — ровно эти сообщения об ошибках и приходят.

## (б) mTLS: обязательная взаимная аутентификация

Генерация демо-CA и пары серверный/клиентский сертификат:

```bash
bash gen-certs.sh
ls tls/*.pem
```

Скрипт создаёт (алгоритм ed25519, срок жизни 30 дней — намеренно короткий, демо):

- `ca.pem` / `ca-key.pem` — демо-CA (только `ca.pem` можно коммитить, с явной пометкой; `ca-key.pem` — никогда);
- `server-cert.pem` / `server-key.pem` — серверный сертификат, SAN: `localhost`, `nats`, `127.0.0.1`;
- `client-cert.pem` / `client-key.pem` — клиентский сертификат для mTLS.

`mtls.conf`:

```
tls {
  cert_file: "/etc/nats/tls/server-cert.pem"
  key_file: "/etc/nats/tls/server-key.pem"
  ca_file: "/etc/nats/tls/ca.pem"
  verify: true
}
```

`verify: true` — сервер требует и проверяет клиентский сертификат (mTLS), а не только шифрует канал.

```bash
docker compose up -d nats-mtls
```

Подключение без клиентского сертификата отклоняется на TLS-уровне:

```bash
nats pub test.mtls "no-cert" --server tls://localhost:4223 --tlsca tls/ca.pem
# tls: certificate required
```

Подключение с клиентским сертификатом проходит:

```bash
nats pub test.mtls "with-cert" --server tls://localhost:4223 \
  --tlsca tls/ca.pem --tlscert tls/client-cert.pem --tlskey tls/client-key.pem
```

Оба исхода проверены на живом сервере с сертификатами из `gen-certs.sh`.

## (в) Decentralized JWT: operator → account → user через nsc

Требует установленный [`nsc`](https://github.com/nats-io/nsc) (в этом окружении проверено на `nsc version 0.0.0-dev`; если бинарник не в `PATH`, вызывайте по полному пути, например `NSC=/path/to/nsc`).

`nsc/setup.sh` воспроизводимо строит всю цепочку в изолированном локальном сторе (не трогает глобальный `~/.config/nats` и `~/.local/share/nats` — использует `XDG_DATA_HOME`/`XDG_CONFIG_HOME` внутри `nsc/nsc-data/`, которая в `.gitignore`):

```bash
NSC="nsc" bash nsc/setup.sh
# на Windows, если nsc не в PATH:
# NSC="C:/Users/ak/go/bin/nsc" bash nsc/setup.sh
```

Скрипт:

1. `nsc add operator --generate-signing-key --sys --name DEMO` — создаёт operator + системный account `SYS` (обязателен для decentralized JWT).
2. `nsc add account --name APP_A` / `APP_B` — два изолированных account, каждый со своим subject-namespace.
3. `nsc add user --account APP_A --name alice` / `--account APP_B --name bob` — по одному пользователю на account; `nsc` кладёт `.creds`-файл (JWT пользователя + nkey seed) в keystore.
4. `nsc add export --account APP_A --name public-events --subject "events.public.>"` — публичный stream-export.
5. `nsc add import --account APP_B --src-account <APP_A pubkey> --remote-subject "events.public.>" --local-subject "from-a.events.>"` — импорт с локальным ремаппингом subject.
6. `nsc generate config --mem-resolver --config-file resolver.generated.conf` — генерирует рабочий серверный конфиг с типом resolver `MEMORY`: JWT operator'а и всех accounts встраиваются прямо в конфиг (`resolver_preload{}`), сервер не обращается ни к диску, ни по сети за ними. Удобно для демо/CI; в проде для часто меняющегося набора accounts используют `resolver: { type: full, dir: ... }` (NATS-based resolver с директорией на диске, синхронизируется через `$SYS`) или `resolver: URL(...)` (HTTP-эндпоинт, отдающий JWT по account ID) — оба типа не встраивают JWT в конфиг сервера и не требуют рестарта при добавлении нового account.

`resolver.conf` в этом каталоге — **шаблон**, не рабочий файл: JWT внутри вымышленные (подпись невалидна), он только показывает структуру. Настоящий `resolver.generated.conf` — продукт шага 6, в `.gitignore`, генерируется заново при каждом запуске `nsc/setup.sh`.

Запуск сервера с сгенерированным конфигом и проверка изоляции + импорта:

```bash
docker run --rm -p 4224:4222 \
  -v "$(pwd)/resolver.generated.conf:/etc/nats/resolver.conf:ro" \
  nats:2.12 -c /etc/nats/resolver.conf
```

```bash
CREDS=nsc/nsc-data/nats/nsc/keys/creds/DEMO

# bob (APP_B) подписывается на импортированный поток
nats sub "from-a.>" --server nats://localhost:4224 --creds "$CREDS/APP_B/bob.creds"

# alice (APP_A) публикует в свой экспортируемый subject
nats pub events.public.demo "hello-via-jwt" --server nats://localhost:4224 --creds "$CREDS/APP_A/alice.creds"
# у bob: [#1] Received on "from-a.events.demo" — hello-via-jwt
```

Проверено end-to-end: сервер стартует с `Trusted Operators: DEMO`, `alice` и `bob` аутентифицируются своими JWT+nkey (без пароля — подпись челленджа приватным ключом), сообщение доходит через export/import ровно как в сценарии (а), но без единой строки credentials в конфиге сервера — весь список доверенных identity зашит в подписанные JWT.

## Три модели аутентификации — когда какая

- **Статическая (user/password, token)** — сценарий (а). Простая, но пароли/токены хранятся в конфиге сервера в открытом виде (или bcrypt-хешем); добавление пользователя требует правки и релоада конфига на каждой ноде.
- **nkeys (ed25519)** — публичный ключ пользователя в конфиге сервера, приватный — только у клиента; аутентификация через подпись challenge, без передачи секрета по сети. Хорошо для сервис-to-сервис, где identity стабильны, а инфраструктура для operator/account JWT избыточна.
- **Decentralized JWT** — сценарий (в). Единственная модель с иерархией account (полная изоляция namespace + собственные лимиты/JetStream domain) и офлайн-выпуском credentials: `nsc` подписывает JWT локально, серверу нужен только resolver. Обязательна, если нужны accounts как unit изоляции (мульти-тенантность) или leaf node с собственной identity на edge (см. статью про leaf nodes, ст.4).

## Очистка

```bash
docker compose down -v
rm -rf nsc/nsc-data resolver.generated.conf tls/*.pem
```

(последняя команда удалит и `ca.pem` — если он закоммичен, `git checkout -- tls/ca.pem` восстановит его).

## Документация

- [Multi Tenancy using Accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts)
- [Decentralized JWT Authentication/Authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/auth_intro/jwt)
- [NKeys](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/auth_intro/nkey_auth)
- [Authorization (permissions)](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization)
- [TLS](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/tls)
- [nsc](https://github.com/nats-io/nsc)
