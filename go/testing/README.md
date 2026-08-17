# testing — стенд к статье про тестирование в Go

Demo-only стенд к статье khorost.tech о слоях тестирования в Go: юнит-тесты
с ручными фейками, `net/http/httptest`, интеграционные тесты через
`testcontainers-go` (реальные Postgres + Redis), детектор гонок `-race` и
честная граница `-cover`. Один модуль `khorost.tech/go-testing`, три
подкаталога — по одному сюжету каждый.

Сквозной сценарий — read-through кэш профилей пользователей
(`cache.Service.GetProfile`): читает из `Cache`, при промахе идёт в
`ProfileRepository` и кладёт результат обратно в кэш. Абстракции —
интерфейсы (`ProfileRepository`, `Cache`), поэтому один и тот же
`Service` тестируется и на ручных фейках (юниты), и на настоящих
адаптерах поверх pgx/go-redis (интеграция).

## Что где

| Подкаталог | Сюжет | Что демонстрирует |
|---|---|---|
| [`cache/`](cache) | Read-through кэш: юниты + httptest + интеграция | `service.go` — логика; `handler.go` — HTTP-обёртка (`GET /user/{id}`); `pg.go`/`redis.go` — реальные адаптеры; `service_test.go` — table-driven на фейках; `handler_test.go` — `httptest.NewRecorder`/`httptest.NewServer`; `integration_test.go` — `testcontainers-go` (Postgres+Redis) |
| [`race/`](race) | `-race` ловит реальную гонку | `UnsyncCache` — небезопасный доступ к map, `SyncCache` — та же логика под `sync.RWMutex`; `race_test.go` — идентичная конкурентная нагрузка на оба; демо гонки на `UnsyncCache` по умолчанию `SKIP`, включается `DEMO_RACE=1` (см. ниже) |
| [`coverage/`](coverage) | 100% покрытия операторов (statements) ≠ отсутствие багов | `DiscountedPrice` без валидации `pct`; `TestDiscountedPrice` даёт 100% покрытия операторов (statements) на «нормальных» входах, но не ловит уход цены в минус при `pct>100` |

Реальные числа, полные логи и точные версии — в [`FIXTURES.md`](FIXTURES.md)
(источник фактов для статьи, не пересказ).

## Как запускать

### Юниты + httptest (быстро, без Docker)

```bash
GOPROXY=https://go.khorost.tech,direct go vet ./...
GOPROXY=https://go.khorost.tech,direct go test ./... -short -v
```

`-short` пропускает `TestIntegration_ReadThrough` (требует Docker, см.
ниже). Всё остальное — `cache.Service` на ручных фейках (`fakeRepo`,
`fakeCache`), `cache.Handler` через `httptest.NewRecorder` и
`httptest.NewServer`, `coverage.DiscountedPrice` на «нормальных» входах,
`race.SyncCache` без детектора — идут нативно на Windows/Linux/macOS,
десятки-сотни миллисекунд на весь модуль (`race.UnsyncCache` — только под
`DEMO_RACE=1`, см. «Race detector» ниже).

### Coverage

```bash
GOPROXY=https://go.khorost.tech,direct go test ./coverage/ -cover
```

Покажет `coverage: 100.0% of statements` — и это **не** означает
отсутствие багов, см. следующий пункт.

Живая демонстрация пропущенного бага (по умолчанию `SKIP`, чтобы основной
прогон оставался зелёным):

```bash
GOPROXY=https://go.khorost.tech,direct DEMO_BUG_CAUGHT=1 go test ./coverage/ -run BugCaught -v
```

Упадёт с `DiscountedPrice(1000, 150) = -500` — это ожидаемо, тест
специально ловит то, что пропускает 100%-покрытие операторов (statements).

### Race detector (нужен CGO/gcc)

Команды по умолчанию — зелёные: `TestUnsyncCache_Race` (демонстрация
реальной гонки) по умолчанию **пропускается** (`t.Skip`), поэтому под
`-race` гоняется только чистый `SyncCache`. Но `go test -race ./...` (без
`-short`) заодно гоняет и `TestIntegration_ReadThrough` (`cache/`) — это
два разных по требованиям прогона, не путать:

```bash
# race БЕЗ Docker: интеграция SKIP (-short), нужен только CGO/C-компилятор для -race
GOPROXY=https://go.khorost.tech,direct go test -race ./... -short   # зелёно; TestUnsyncCache_Race и TestIntegration_ReadThrough — SKIP

# race + integration: полный прогон модуля, нужны и Docker, и CGO/C-компилятор
GOPROXY=https://go.khorost.tech,direct go test -race ./...          # зелёно; TestUnsyncCache_Race — SKIP, TestIntegration_ReadThrough реально поднимает Postgres+Redis
```

Демо реальной гонки — включается явно через `DEMO_RACE=1`:

```bash
GOPROXY=https://go.khorost.tech,direct DEMO_RACE=1 go test ./race/ -run TestUnsyncCache_Race -race -v   # WARNING: DATA RACE (ожидаемо, FAIL)
GOPROXY=https://go.khorost.tech,direct go test ./race/ -run TestSyncCache_NoRace -race -v                # чисто, PASS
```

Без `DEMO_RACE=1` и без `-race` `TestUnsyncCache_Race` **нельзя** гонять
напрямую (`-run TestUnsyncCache_Race`) вслепую: без детектора
несинхронизированная конкурентная запись в map под такой нагрузкой с очень
высокой вероятностью (но не гарантированно — зависит от реально
исполненного конкурентного пути) роняет процесс рантайм-фаталом
`fatal error: concurrent map writes` (это не `panic`, `recover` не
спасает) — именно поэтому тест гейтится через
`DEMO_RACE`, а не просто помечен обычным `t.Skip` без причины.

`-race` требует CGO и рабочий C-компилятор. **На Windows без
gcc/MSYS2/MinGW команда не соберётся** — гоняйте через WSL2 (Ubuntu +
`build-essential`) или Linux/macOS-CI напрямую.

**`~1с` у `TestSyncCache_NoRace` под `-race` — это не компиляция**, а
дефолтная послетестовая пауза race-рантайма
(`GORACE=atexit_sleep_ms=1000`). Проверить:
`GORACE=atexit_sleep_ms=0 go test ./race/ -run TestSyncCache_NoRace -race`
укладывается в `~0.01с` — это доказывает лишь, что та секунда уходит на
послетестовую паузу `atexit_sleep_ms`, а не на детектор; собственно
overhead инструментирования `-race` этим микротестом НЕ измеряется (в
общем случае — 2–20× по памяти и времени). Подробности и живые числа —
в разделе «§race» [`FIXTURES.md`](FIXTURES.md).

### Интеграция (нужен Docker)

```bash
GOPROXY=https://go.khorost.tech,direct go test ./cache/ -run TestIntegration -v
```

Поднимает настоящие `postgres:17.2-alpine` и `redis:7.4-alpine` (**пиновые
теги**, не плавающие `17-alpine`/`7-alpine`, — уменьшают дрейф версий
между прогонами на разных машинах и в разное время, но сами по себе
остаются изменяемыми тегами: Docker явно отличает изменяемый тег от
неизменяемого digest,
https://docs.docker.com/engine/reference/commandline/pull/#pull-an-image-by-digest-immutable-identifier
— для строгой воспроизводимости нужен именно `@sha256:...`; digest'ы обоих
образов зафиксированы в [`FIXTURES.md`](FIXTURES.md#версии)) через
`testcontainers-go`,
засеивает Postgres, прогоняет `Service.GetProfile` дважды (промах → БД,
попадание → Redis) и напрямую проверяет значение в Redis. Требует
запущенный Docker с рабочим сокетом.

**Оговорка про конкретное окружение, не общее ограничение библиотеки**:
Testcontainers for Go официально поддерживает Docker Desktop на Windows
(https://golang.testcontainers.org/system_requirements/docker/). В этом
авторском окружении (нативный Windows + Docker Desktop через named pipe
`npipe://`) наблюдался таймаут проброса портов контейнера — это
окруженческое наблюдение, не документированное ограничение
testcontainers-go. Рабочий обход, который сработал здесь, — WSL2 (Ubuntu)
с Docker, подключённым через `unix:///var/run/docker.sock` (Docker Desktop
→ Settings → Resources → WSL Integration, включить для дистрибутива).
Внутри WSL2 команды те же самые (`GOPROXY=... go test ./cache/ -run
TestIntegration -v`).

Если WSL2-дистрибутив держит модуль на смонтированном Windows-диске
(`/mnt/c`, `/mnt/g`, ...) — при нестабильной сборке (в т.ч. race-бинаря)
скопируйте пакет на нативную WSL-файловую систему (например,
`~/go-testing-stand`) и гоняйте оттуда; исходники в Git остаются на
Windows-пути, копия — только рабочая для конкретного прогона.

## Требования

- Go 1.26+ (модуль объявляет `go 1.26.3`).
- Docker (любой, с рабочим сокетом) — только для `TestIntegration_ReadThrough`;
  весь остальной модуль (юниты/httptest/coverage/race без `-race`) от
  Docker не зависит.
- CGO + C-компилятор (gcc/clang) — только для `-race`.
- На Windows обе технологии поддерживаются при подходящей конфигурации;
  в конкретном авторском окружении (Docker Desktop через npipe + отсутствие
  gcc в нативном Go) для интеграции и `-race` понадобился WSL2 — см.
  оговорку выше, это не общее ограничение платформы.
- `GOPROXY=https://go.khorost.tech,direct` — приватный кеш-прокси Go-модулей
  (используется во всех командах выше; без него — публичный
  `proxy.golang.org`, тоже сработает, но не то, чем прогонялся стенд).

## Demo-only оговорка

Стенд — минимальный, для статьи, не production-код:

- `Service` кэша — best-effort (ошибка записи в кэш игнорируется, в проде
  нужны лог/метрика — см. комментарий в `cache/service.go`);
- `race.UnsyncCache` **намеренно сломан** — это демонстрационный «плохой»
  пример гонки, не использовать как есть;
- `coverage.DiscountedPrice` **намеренно не валидирует вход** — это
  демонстрация ограничения метрики покрытия операторов (statements), не образец кода для
  копирования;
- интеграционный тест поднимает контейнеры на каждый прогон (без переиспользования
  между прогонами/тестами) — для CI-паттерна с общим тестовым контейнером
  на пакет нужна отдельная обвязка (`TestMain`), здесь её нет специально,
  чтобы тест оставался самодостаточным и читаемым за один проход.

Числа (wall-time, версии образов, точный `coverage: X%`, полные логи
`-race`/testcontainers) — реальные, сняты живыми прогонами, см.
[`FIXTURES.md`](FIXTURES.md). Абсолютные тайминги (особенно wall-time
интеграции) зависят от машины/загрузки Docker-хоста — снимайте у себя при
воспроизведении, не переносите числа этого README как гарантированные.
