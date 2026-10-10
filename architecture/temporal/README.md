# Temporal: durable execution вглубь — стенд

Живой стенд к серии из восьми статей «Temporal: durable execution вглубь»
на [khorost.tech](https://khorost.tech/architecture/).

Обзорная статья про Temporal — [здесь](https://khorost.tech/architecture/temporal-durable-workflows/);
она отвечает на вопрос «брать или нет». Этот стенд про другое: **как оно
устроено внутри и что нужно знать, чтобы эксплуатировать это в проде**.

Все числа, которые попадают в статьи, живут в [FIXTURES.md](FIXTURES.md) и
больше нигде. Сырые прогоны — в `scripts/.runs/`.

## Топология: прод-подобная, роли разнесены

Стенд **не** использует `server start-dev`. Dev-server держит историю в
памяти, поэтому на нём нельзя показать ни перезапуск сервера с сохранением
исполнения, ни persistence-слой, ни отдельный visibility-store — а это
предмет статей 2 и 7 серии.

| Сервис | Роль |
|---|---|
| `postgres` | persistence: event history, mutable state, таймеры |
| `elasticsearch` | visibility отдельным хранилищем |
| `temporal-schema` | одноразовый: ставит схемы и индекс visibility, завершается |
| `temporal-frontend` | gRPC API, точка входа |
| `temporal-history` | продвижение состояния, запись истории, таймеры |
| `temporal-matching` | сведение задач с воркерами |
| `temporal-worker` | системные фоновые задачи Temporal |
| `temporal-namespace` | одноразовый: создаёт namespace `default` |
| `temporal-ui` | Web UI |
| `prometheus`, `grafana` | метрики сервиса и SDK |

Четыре роли — четыре контейнера (`SERVICES=<роль>`). Это не украшение:
разнесение даёт демонстрации, недоступные ни на dev-server, ни на едином
auto-setup — убить **только** `history` и посмотреть, что видит воркер;
погасить сервер целиком и увидеть, что исполнение продолжается после
подъёма, потому что история лежит в Postgres.

## Версии

Сверены живьём `scripts/probe.sh` (2026-08-15): Temporal Server `1.29.7`,
UI `2.53.3`, PostgreSQL `18.4`, Elasticsearch `8.19.5`, Go `1.26.3`,
Go SDK `v1.46.0`, Java SDK `1.30.1`, TypeScript SDK `1.22.0`,
Python SDK `1.31.0`, .NET SDK `1.18.0`.

Образ `temporalio/auto-setup` отстаёт от `temporalio/server`: максимальный
тег auto-setup на дату сборки — `1.29.7`. Стенд стоит на auto-setup, потому
что только этот образ несёт шаблон конфигурации и умеет ставить схему.

## Порты

| Что | С хоста |
|---|---|
| gRPC frontend | **7253** |
| Web UI | **8253** — http://localhost:8253 |
| Prometheus | **9253** |
| Grafana | **3253** |

`7233` занят стендом `event-coordination`, `7243` — стендом `saga`.

## Как поднять

```bash
bash scripts/up.sh       # поднять и дождаться готовности
bash scripts/probe.sh    # сверить версии живьём
bash scripts/down.sh     # погасить вместе с томом Postgres
```

## Правило прогонов: всё внутри сети compose

Локально собранные бинари на рабочей Windows-машине не достукиваются до
`localhost:7253` — dial висит на `[::1]`/IPv4 и отваливается по таймауту.
Поэтому воркеры и клиенты запускаются **контейнерами на сети стенда** и
ходят на `temporal-frontend:7233`. То же касается пяти SDK-воркеров
профиля 07: каждый собирается в официальном образе своего языка, на хосте
не нужен ни один языковой тулчейн, кроме Docker.

Это же условие влияет на числа: каждый gRPC-раунд здесь стоит сотни
миллисекунд. Величины, снятые метриками SDK внутри процесса воркера, эту
накладную не несут — источник у каждого числа указан в `FIXTURES.md`.

## Профили

Общее доменное ядро (`go/internal/provisioning`) плюс восемь изолированных
профилей. Домен один — процесс «провизионинг ресурса», — но каждый профиль
ломается и перемеряется отдельно.

| Профиль | Статья | Что показывает |
|---|---|---|
| `00-paradigm` | 1. Durable execution: парадигма | один процесс тремя способами; убийство воркера и убийство всего сервера |
| `01-internals` | 2. Архитектура вглубь | sticky-кэш по метрикам SDK, гашение роли `history` под нагрузкой, срез persistence |
| `02-determinism` | 3. Детерминизм и replay | воспроизводимая non-determinism error, цена replay от длины истории |
| `03-activities` | 4. Activities вглубь | идемпотентность, heartbeat, local activity против обычной |
| `04-messaging` | 5. Signals, Queries, Updates | Query не растит историю, Update валидируется синхронно, цена Continue-As-New |
| `05-versioning` | 6. Версионирование | наивная правка, починка `GetVersion`, преждевременно снятый патч |
| `06-operations` | 7. Эксплуатация | ёмкость воркера против schedule-to-start, тест с промоткой времени |
| `07-languages` | 8. По языкам | один сценарий на Go, Java, TypeScript, Python, .NET |

Каждый профиль запускается своим скриптом:

```bash
bash scripts/00-paradigm.sh
bash scripts/01-internals.sh
bash scripts/07-languages.sh
```

Скрипты печатают отчёт и строки `ЗАМЕР`/`ЯЗЫК`/`СТАТУС`, из которых
собираются фикстуры.

## Структура

```
temporal/
  compose/
    compose.yml               # одиннадцать сервисов, роли Temporal раздельно
    prometheus.yml            # скрейп метрик сервиса и SDK
    grafana-datasource.yml
  go/
    internal/provisioning/    # общее доменное ядро: домен и activity
    internal/obs/             # экспорт метрик SDK в Prometheus
    00-paradigm/ … 07-languages/
  clients/
    java/ ts/ python/ dotnet/ # воркеры профиля 07, сборка в образах
  scripts/
    lib.sh up.sh down.sh
    probe.sh                  # фактчек-гейт: версии живьём
    00-paradigm.sh … 07-languages.sh
    verify-static.sh          # статический гейт
    .runs/                    # сырые прогоны профилей
  FIXTURES.md                 # единственный источник чисел для статей
  README.md
```

## Гейты

```bash
bash scripts/probe.sh
bash scripts/verify-static.sh
```

## Оговорка про этот стенд

Топология прод-подобная, но это не прод: по одному экземпляру каждой роли,
один узел Elasticsearch, без TLS и без аутентификации. Она сделана такой
ровно настолько, чтобы механика была видна честно.
