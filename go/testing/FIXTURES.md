# FIXTURES — реальные выводы прогонов стенда go/testing

Все числа, выводы и логи ниже — из **живых** прогонов на этой машине
(Windows 11, WSL2 Ubuntu-24.04 + Docker Desktop). Раздел версий,
table-driven, race, integration и тайминги ниже обновлены в ходе ревизии
по замечаниям автор-ревью (гейт `DEMO_RACE`, расширенные негативные
юниты, пиновые образы, контроль race-тайминга) — каждая команда
перезапущена заново непосредственно перед записью в этот файл на даты
**2026-07-13–14** (базовые прогоны — 13-го; часть выводов — расширенная
HTTP-матрица с `TestHandler_MethodAndError`, точная сверка wire-полей и
пересъём таймингов `-short` — перезаписана 14-го при доработке по
автор-ревью). Источник фактов для статьи «Тестирование в Go: unit,
httptest, testcontainers, -race, coverage».

## Версии

Сверено живьём (`go version`, `go list -m all`, `docker version`) на дату
прогона 2026-07-13 (версии за 13–14 не менялись).

| Компонент | Версия | Источник |
|---|---|---|
| Go (нативный Windows) | `go1.26.3 windows/amd64` | `go version` |
| Go (WSL2 Ubuntu-24.04) | `go1.26.3 linux/amd64` | `go version` в WSL |
| Docker Engine (WSL2) | `29.6.1` | `docker version --format '{{.Server.Version}}'` |
| module `khorost.tech/go-testing` | `go 1.26.3` | `go.mod` |
| `github.com/jackc/pgx/v5` | `v5.10.0` | `go.mod` / `go list -m all` |
| `github.com/redis/go-redis/v9` | `v9.21.0` | `go.mod` / `go list -m all` |
| `github.com/testcontainers/testcontainers-go` (+`modules/postgres`, `modules/redis`) | `v0.43.0` | `go.mod` / `go list -m all`, подтверждено баннером testcontainers в живом логе интеграции |
| `github.com/stretchr/testify` | `v1.11.1` | `go list -m all` — транзитивная зависимость testcontainers-go (сами тесты стенда testify не используют, ручные ассерты `t.Fatalf`/`t.Errorf`) |
| Образ Postgres (интеграция) | `postgres:17.2-alpine` (**пиновый тег**, было `postgres:17-alpine`) | `cache/integration_test.go`, подтверждено логом `Creating container for image postgres:17.2-alpine`; digest `postgres@sha256:7e5df973a74872482e320dcbdeb055e178d6f42de0558b083892c50cda833c96` (`docker inspect`, WSL2) |
| Образ Redis (интеграция) | `redis:7.4-alpine` (**пиновый тег**, было `redis:7-alpine`) | `cache/integration_test.go`, подтверждено логом `Creating container for image redis:7.4-alpine`; digest `redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99` (`docker inspect`, WSL2) |
| Ryuk (testcontainers reaper) | `testcontainers/ryuk:0.14.0` | автоматически подтянут testcontainers-go, живой лог |
| GOPROXY | `https://go.khorost.tech,direct` | явно выставлен во всех командах ниже |

**Почему пиновые теги, а не `X-alpine`**: `postgres:17-alpine`/`redis:7-alpine` —
плавающие теги (пересобираются апстримом на новые минорные версии/патчи
без предупреждения), из-за чего повторный прогон интеграции через
полгода-год может неожиданно тянуть другой Postgres/Redis и ловить другое
поведение. `17.2-alpine`/`7.4-alpine` — тоже теги, а не digest, и формально
тоже могут быть переопределены апстримом (Docker явно отличает изменяемый
тег от неизменяемого digest,
https://docs.docker.com/engine/reference/commandline/pull/#pull-an-image-by-digest-immutable-identifier),
но на практике патч-версийные теги трогают куда реже минорных — пиновка до
патч-версии уменьшает дрейф между прогонами, не устраняет его полностью.
Для строгой, гарантированной воспроизводимости нужен именно `@sha256:...`
— digest'ы обоих образов сверены живьём (`docker inspect`) и приведены в
таблице выше как «строгий» вариант привязки.

Полный баннер testcontainers из живого прогона интеграции:

```
6.1
  API Version: 1.54
  Operating System: Docker Desktop
  Total Memory: 64260 MB
  Labels:
    com.docker.desktop.address=unix:///var/run/docker-cli.sock
  Testcontainers for Go Version: v0.43.0
  Resolved Docker Host: unix:///var/run/docker.sock
  Resolved Docker Socket Path: /var/run/docker.sock
```

---

## §table-driven — юнит-тесты `cache.Service.GetProfile` (фейки, `-v`)

Расширено по замечанию автор-ревью: было 3 кейса (miss/hit/not-found), стало
**7 табличных кейсов** + отдельный тест на TTL — покрыты advanced-контракты
read-through логики (`cache/service.go`): ошибка `Cache.Get` (best-effort →
трактуется как miss), битый JSON в кэше (→ miss), ошибка `Cache.Set`
(best-effort → запрос не фейлится), произвольная (не `ErrNotFound`) ошибка
репозитория (пробрасывается наружу как есть), и то, что TTL из `Service`
реально доходит до `Cache.Set`. Все ожидания сверены с фактической
семантикой `service.go` — расхождений не найдено, код сервиса не менялся.

Декоративный `t.Cleanup` (пустой no-op) внутри `t.Run` убран — фейкам
нечего чистить, реальный `t.Cleanup` (терминация testcontainers)
демонстрируется в `integration_test.go`.

Команда: `go test ./cache/ -run TestGetProfile -v -count=1` (нативный
Windows).

```
=== RUN   TestGetProfile
=== PAUSE TestGetProfile
=== RUN   TestGetProfile_PassesTTLToCache
=== PAUSE TestGetProfile_PassesTTLToCache
=== CONT  TestGetProfile
=== RUN   TestGetProfile/cache_miss_reads_through_repo_and_backfills_cache
=== PAUSE TestGetProfile/cache_miss_reads_through_repo_and_backfills_cache
=== RUN   TestGetProfile/cache_hit_does_not_call_repo
=== PAUSE TestGetProfile/cache_hit_does_not_call_repo
=== CONT  TestGetProfile_PassesTTLToCache
=== RUN   TestGetProfile/unknown_user_propagates_ErrNotFound
=== PAUSE TestGetProfile/unknown_user_propagates_ErrNotFound
=== RUN   TestGetProfile/cache_get_error_is_treated_as_miss,_request_still_succeeds
=== PAUSE TestGetProfile/cache_get_error_is_treated_as_miss,_request_still_succeeds
=== RUN   TestGetProfile/corrupted_json_in_cache_is_treated_as_miss,_repo_backfills_valid_value
=== PAUSE TestGetProfile/corrupted_json_in_cache_is_treated_as_miss,_repo_backfills_valid_value
=== RUN   TestGetProfile/cache_set_error_does_not_fail_the_request_(best-effort)
=== PAUSE TestGetProfile/cache_set_error_does_not_fail_the_request_(best-effort)
=== RUN   TestGetProfile/repo_arbitrary_error_propagates_as-is_(not_just_ErrNotFound)
=== PAUSE TestGetProfile/repo_arbitrary_error_propagates_as-is_(not_just_ErrNotFound)
=== CONT  TestGetProfile/cache_miss_reads_through_repo_and_backfills_cache
=== CONT  TestGetProfile/repo_arbitrary_error_propagates_as-is_(not_just_ErrNotFound)
=== CONT  TestGetProfile/cache_hit_does_not_call_repo
=== CONT  TestGetProfile/cache_set_error_does_not_fail_the_request_(best-effort)
=== CONT  TestGetProfile/corrupted_json_in_cache_is_treated_as_miss,_repo_backfills_valid_value
=== CONT  TestGetProfile/unknown_user_propagates_ErrNotFound
--- PASS: TestGetProfile_PassesTTLToCache (0.00s)
=== CONT  TestGetProfile/cache_get_error_is_treated_as_miss,_request_still_succeeds
--- PASS: TestGetProfile (0.00s)
    --- PASS: TestGetProfile/repo_arbitrary_error_propagates_as-is_(not_just_ErrNotFound) (0.00s)
    --- PASS: TestGetProfile/unknown_user_propagates_ErrNotFound (0.00s)
    --- PASS: TestGetProfile/cache_miss_reads_through_repo_and_backfills_cache (0.00s)
    --- PASS: TestGetProfile/cache_get_error_is_treated_as_miss,_request_still_succeeds (0.00s)
    --- PASS: TestGetProfile/cache_set_error_does_not_fail_the_request_(best-effort) (0.00s)
    --- PASS: TestGetProfile/cache_hit_does_not_call_repo (0.00s)
    --- PASS: TestGetProfile/corrupted_json_in_cache_is_treated_as_miss,_repo_backfills_valid_value (0.00s)
PASS
ok  	khorost.tech/go-testing/cache	0.086s
```

Семь табличных подтестов (`t.Run` по кейсам `cases`) + отдельный
`TestGetProfile_PassesTTLToCache` реально прошли параллельно
(`t.Parallel()` и в родительском, и в дочерних тестах — видно по
`PAUSE`/`CONT`). Каждый табличный кейс проверяет одновременно инварианты:
профиль/ошибку, флаг `fromCache`, число обращений к `fakeRepo.calls` и
содержимое кэша после вызова — не только НАЛИЧИЕ ключа (через прямой
доступ к `fakeCache.data`, а не через `Cache.Get` — часть кейсов намеренно
ломает `Get` через `getErr`), но и для успешных кейсов сам факт, что
лежащее значение декодируется в ожидаемый профиль. Последнее принципиально
для кейса с битым JSON: проверка одного наличия ключа осталась бы зелёной,
даже если бы сервис перестал перезаписывать повреждённое значение (ключ
существует и ДО вызова) — декодирование значения ловит такой регресс
(проверено мутацией: с отключённым бэкфиллом кейс `corrupted_json` падает).
Новые поля фейков:
`fakeCache.getErr`/`setErr`/`gotTTL`/`setCalls`, `fakeRepo.err` (см.
`cache/service_test.go`).

---

## §httptest — `cache.Handler` (recorder + реальный сервер, `-v`)

Та же команда (`go test ./cache/ -run TestHandler -short -v`), фрагмент
вывода — все три httptest-теста:

```
=== RUN   TestHandlerRecorder
=== PAUSE TestHandlerRecorder
=== RUN   TestHandler_MethodAndError
=== PAUSE TestHandler_MethodAndError
=== RUN   TestHandlerServer
=== PAUSE TestHandlerServer
=== CONT  TestHandlerRecorder
=== CONT  TestHandlerServer
=== CONT  TestHandler_MethodAndError
--- PASS: TestHandler_MethodAndError (0.00s)
--- PASS: TestHandlerRecorder (0.00s)
--- PASS: TestHandlerServer (0.00s)
PASS
ok  	khorost.tech/go-testing/cache	0.079s
```

Что именно проверено в теле тестов (`cache/handler_test.go`), по кодам и
заголовкам:

- `TestHandlerRecorder` (`httptest.NewRecorder` + `h.ServeHTTP` напрямую,
  без сетевого сокета):
  - `GET /user/1` (первый раз, кэш пуст) → `200`, заголовок
    `X-Cache: MISS`, тело — валидный JSON `Profile{ID:1, Name:"Alice", ...}`;
  - `GET /user/1` (второй раз, тот же процесс) → `200`,
    `X-Cache: HIT` — подтверждает, что `Handler` реально видит
    read-through кэш `Service` между запросами;
  - `GET /user/999` (несуществующий id) → `404` (`ErrNotFound` из
    `fakeRepo` → `errors.Is` в хендлере);
  - `GET /user/abc` (нечисловой id) → `400` (`strconv.ParseInt`
    проваливается раньше похода в сервис);
  - на `200`-ответе дополнительно проверены заголовок
    `Content-Type: application/json` и **точный набор** JSON-полей тела
    (разбор в `map[string]json.RawMessage`, сверка ключей `ID/Name/Email`
    без лишних — а не только декодирование обратно в `cache.Profile`, что
    замаскировало бы и добавленное, и переименованное поле).
- `TestHandler_MethodAndError` (оставшиеся ветки матрицы, ранее не
  покрытые): `POST /user/1` → `405 Method Not Allowed` (ServeMux Go 1.22
  сам отвечает на зарегистрированный путь с чужим методом); произвольная
  (не `ErrNotFound`) ошибка репозитория → `500` (ветка `handler.go`,
  иначе не исполнявшаяся в тестах).
- `TestHandlerServer` (`httptest.NewServer` + настоящий `http.Client` по
  TCP-сокету на `127.0.0.1`, не прямой вызов `ServeHTTP`):
  - `GET /user/2` → `200`, `X-Cache: MISS`, тело — `Profile{ID:2,
    Name:"Bob", Email:"bob@example.com"}`, декодировано через
    `encoding/json` из `resp.Body`.

Все три httptest-теста прошли за один прогон. В составе целого пакета
(`go test ./cache/ -short`) вместе с ними идут `TestGetProfile`,
`TestGetProfile_PassesTTLToCache` и `SKIP` интеграционного теста при
`-short`; полное wall-time пакета — см. таблицу таймингов ниже (нативный
Windows заметно шумит от прогона к прогону).

---

## §integration — testcontainers-go, реальные Postgres + Redis (WSL2, Docker)

**Почему WSL2, а не нативный Windows**: Testcontainers for Go официально
поддерживает Docker Desktop на Windows
(https://golang.testcontainers.org/system_requirements/docker/) — это не
общее ограничение библиотеки. В конкретном авторском окружении (нативный
Windows + Docker Desktop через named pipe `npipe://`) наблюдался таймаут
проброса портов контейнера (задокументировано в Task 4); WSL2 оказался
рабочим обходом именно для этой машины — контейнеры стартуют через WSL2
Ubuntu-24.04 c Docker-сокетом `unix:///var/run/docker.sock`. Пакет
скопирован из `/mnt/g/...` (drvfs) в нативную WSL-файловую систему
(`~/khorost-scratch/go-testing-stand`) перед прогоном — источник истины
остаётся в Git на Windows-пути, копия — только рабочая для сборки/прогона.

Команда: `GOPROXY=https://go.khorost.tech,direct go test ./cache/ -run TestIntegration -v -count=1`.

Полный живой вывод (после пиновки образов на `postgres:17.2-alpine` /
`redis:7.4-alpine` — Фикс 5 ревизии):

```
=== RUN   TestIntegration_ReadThrough
2026/07/13 22:20:22 github.com/testcontainers/testcontainers-go - Connected to docker:
  Server Version: 29.6.1
  API Version: 1.54
  Operating System: Docker Desktop
  Total Memory: 64260 MB
  Labels:
    com.docker.desktop.address=unix:///var/run/docker-cli.sock
  Testcontainers for Go Version: v0.43.0
  Resolved Docker Host: unix:///var/run/docker.sock
  Resolved Docker Socket Path: /var/run/docker.sock
  Test SessionID: 073b7ea4e8e17db11f9e6970f4e7cd6d7e65ade9b8a546f22402298bb2b15a01
  Test ProcessID: 6231e116-a132-4abc-8d9d-0d2d826b7d25
2026/07/13 22:20:22 🐳 Creating container for image postgres:17.2-alpine
2026/07/13 22:20:23 🐳 Creating container for image testcontainers/ryuk:0.14.0
2026/07/13 22:20:23 ✅ Container created: f244f3b22801
2026/07/13 22:20:23 🐳 Starting container: f244f3b22801
2026/07/13 22:20:23 ✅ Container started: f244f3b22801
2026/07/13 22:20:23 ⏳ Waiting for container id f244f3b22801 image: testcontainers/ryuk:0.14.0. Waiting for: all of: [log message "Started", port 8080/tcp to be listening]
2026/07/13 22:20:23 Shell not found in container
2026/07/13 22:20:23 🔔 Container is ready: f244f3b22801
2026/07/13 22:20:23 ✅ Container created: 861d2a9dfaa9
2026/07/13 22:20:23 🐳 Starting container: 861d2a9dfaa9
2026/07/13 22:20:24 ✅ Container started: 861d2a9dfaa9
2026/07/13 22:20:24 ⏳ Waiting for container id 861d2a9dfaa9 image: postgres:17.2-alpine. Waiting for: all of: [log message "database system is ready to accept connections" (occurrence: 2), port 5432/tcp to be listening]
2026/07/13 22:20:26 🔔 Container is ready: 861d2a9dfaa9
2026/07/13 22:20:26 🐳 Creating container for image redis:7.4-alpine
2026/07/13 22:20:26 ✅ Container created: 091fa01a522a
2026/07/13 22:20:26 🐳 Starting container: 091fa01a522a
2026/07/13 22:20:26 ✅ Container started: 091fa01a522a
2026/07/13 22:20:26 ⏳ Waiting for container id 091fa01a522a image: redis:7.4-alpine. Waiting for: all of: [log message "Ready to accept connections"]
2026/07/13 22:20:26 🔔 Container is ready: 091fa01a522a
2026/07/13 22:20:26 🐳 Stopping container: 091fa01a522a
2026/07/13 22:20:27 ✅ Container stopped: 091fa01a522a
2026/07/13 22:20:27 🐳 Terminating container: 091fa01a522a
2026/07/13 22:20:27 🚫 Container terminated: 091fa01a522a
2026/07/13 22:20:27 🐳 Stopping container: 861d2a9dfaa9
2026/07/13 22:20:28 ✅ Container stopped: 861d2a9dfaa9
2026/07/13 22:20:28 🐳 Terminating container: 861d2a9dfaa9
2026/07/13 22:20:28 🚫 Container terminated: 861d2a9dfaa9
--- PASS: TestIntegration_ReadThrough (5.63s)
PASS
ok  	khorost.tech/go-testing/cache	5.642s
```

**Wall-time: 5.642s** (`ok` строка `go test`, пиновые образы
`postgres:17.2-alpine`/`redis:7.4-alpine`, оба уже локально кешированы —
без сетевой загрузки слоёв), из них ~4с — старт Postgres+Redis+Ryuk-
контейнеров, остаток — фактический код теста (схема+сид Postgres, два
вызова `Service.GetProfile`, прямая проверка значения в Redis через
`rdb.Get`, `t.Cleanup` — `Terminate` обоих контейнеров). Для сравнения:
предыдущий прогон на плавающих тегах (`postgres:17-alpine`/
`redis:7-alpine`, до Фикса 5) давал 6.020s на этой же машине — разброс в
пределах шума одной машины/прогона, пиновка тегов сама по себе не меняет
порядок величины wall-time интеграции.

Переходы, которые реально проверяет тест (assert-уровень, не просто «нет
ошибки»):
1. **1-й вызов** `GetProfile(ctx, 42)` — кэш Redis пуст → идёт в
   Postgres (`fromCache1 == false`), профиль `{42, "Ada Lovelace",
   "ada@example.com"}` из заранее засеянной таблицы `profiles`.
2. **Побочный эффект в Redis** — после 1-го вызова ключ `user:42`
   реально появляется в Redis (`rdb.Get` напрямую, в обход `Service`) и
   значение после `json.Unmarshal` по значению структуры совпадает с
   профилем из Postgres (сравниваются поля `Profile`, не сырые байты);
   дополнительно `PTTL` подтверждает, что у ключа выставлен положительный
   срок жизни (TTL реально доехал до Redis, а не запись без expire).
3. **2-й вызов** `GetProfile(ctx, 42)` — тот же id → `fromCache2 == true`,
   профиль совпадает с 1-м вызовом (`p2 == p1`), Postgres при этом
   повторно не читается (проверяется тем, что вызов идёт через Redis-путь
   сервиса, а не отдельным счётчиком запросов к БД — граница теста).

Образы Postgres/Redis запрашивались testcontainers по пиновым тегам
`postgres:17.2-alpine` / `redis:7.4-alpine` — уже присутствовали локально
(предварительно вытянуты `docker pull` перед прогоном интеграции в ходе
этой ревизии), поэтому в этом прогоне отсутствует фаза `Pulling image` —
честная оговорка: на «холодной» машине wall-time первого прогона будет
заметно больше (сетевая загрузка слоёв образов), 5.64s — время именно
старта уже скачанных контейнеров + самого теста.

---

## §race — `-race`, несинхронизированный кэш (DATA RACE) vs синхронизированный (чисто)

Обе команды — WSL2 (нативный Windows без CGO/gcc не даёт `-race`), пакет
скопирован в нативную WSL-FS (см. §integration) — на `/mnt/g` (drvfs)
компиляция race-инструментированного бинаря нестабильна (задокументировано
в Task 5).

### Гейт `DEMO_RACE` (Фикс 1 ревизии)

`TestUnsyncCache_Race` по умолчанию **пропускается** (`t.Skip`): без
`-race` конкурентная запись в несинхронизированную map под такой нагрузкой
с очень высокой вероятностью (но не гарантированно — зависит от реально
исполненного конкурентного пути) роняет процесс рантайм-фаталом
`fatal error: concurrent map writes` (не
`panic` — `recover` его не ловит, упал бы весь `go test ./...`, а не
только этот тест), а под `-race` тест намеренно и ожидаемо `FAIL` (сам
детектор гонки — это и есть демонстрация, но `FAIL` в основном прогоне
недопустим). Включается явно: `DEMO_RACE=1 go test ./race/ -run
TestUnsyncCache_Race -race -v`. `TestSyncCache_NoRace` гейта не имеет и
гоняется всегда — он чист и под `-race`, и без него.

Проверено вживую (WSL2, `~/khorost-scratch/go-testing-stand`):

- `go test ./... -short` → **зелёно**, `TestUnsyncCache_Race` — `SKIP`.
- `go test -race ./...` → **зелёно** (весь модуль, включая интеграцию),
  `TestUnsyncCache_Race` — `SKIP`, гоняется только чистый `SyncCache`.
- `DEMO_RACE=1 go test ./race/ -run TestUnsyncCache_Race -race` → ловит
  `WARNING: DATA RACE` (см. ниже), `FAIL` — ожидаемо.

### `TestUnsyncCache_Race` — реальная гонка, детектор ловит (`DEMO_RACE=1`)

Команда: `DEMO_RACE=1 GOPROXY=https://go.khorost.tech,direct go test ./race/ -run TestUnsyncCache_Race -race -v -count=1`.

Полный вывод (обрезаны повторяющиеся стек-трейсы `testing.tRunner` —
оставлен первый инцидент полностью, остальные три показаны как
заголовки-подтверждения):

```
=== RUN   TestUnsyncCache_Race
==================
WARNING: DATA RACE
Write at 0x00c0000c86c0 by goroutine 17:
  runtime.mapassign_faststr()
      /usr/local/go/src/internal/runtime/maps/runtime_faststr.go:263 +0x0
  khorost.tech/go-testing/race.(*UnsyncCache).Set()
      /home/khap/khorost-scratch/go-testing-stand/race/cache_race.go:36 +0xa5
  khorost.tech/go-testing/race.TestUnsyncCache_Race.func1()
      /home/khap/khorost-scratch/go-testing-stand/race/race_test.go:36 +0x80

Previous write at 0x00c0000c86c0 by goroutine 9:
  runtime.mapassign_faststr()
      /usr/local/go/src/internal/runtime/maps/runtime_faststr.go:263 +0x0
  khorost.tech/go-testing/race.(*UnsyncCache).Set()
      /home/khap/khorost-scratch/go-testing-stand/race/cache_race.go:36 +0xa5
  khorost.tech/go-testing/race.TestUnsyncCache_Race.func1()
      /home/khap/khorost-scratch/go-testing-stand/race/race_test.go:36 +0x80

Goroutine 17 (running) created at:
  khorost.tech/go-testing/race.TestUnsyncCache_Race()
      /home/khap/khorost-scratch/go-testing-stand/race/race_test.go:34 +0x1dc
  testing.tRunner()
      /usr/local/go/src/testing/testing.go:2036 +0x21c
  testing.(*T).Run.gowrap1()
      /usr/local/go/src/testing/testing.go:2101 +0x38

Goroutine 9 (finished) created at:
  khorost.tech/go-testing/race.TestUnsyncCache_Race()
      /home/khap/khorost-scratch/go-testing-stand/race/race_test.go:34 +0x1dc
  testing.tRunner()
      /usr/local/go/src/testing/testing.go:2036 +0x21c
  testing.(*T).Run.gowrap1()
      /usr/local/go/src/testing/testing.go:2101 +0x38
==================
==================
WARNING: DATA RACE   [ещё 1 write/write race — конкурентная mapassign_faststr]
==================
==================
WARNING: DATA RACE   [1 read/write race — Get против Set на том же адресе]
==================
==================
WARNING: DATA RACE   [ещё 1 read/write race]
==================
    testing.go:1712: race detected during execution of test
--- FAIL: TestUnsyncCache_Race (0.01s)
FAIL
FAIL	khorost.tech/go-testing/race	0.013s
FAIL
```

**Всего 4 отдельных `WARNING: DATA RACE`** за один прогон: 2× write/write
между конкурентными вызовами `Set` и 2× read/write между `Get` и `Set` —
все по **одному** ключу `"k"` несинхронизированной map (тест других
ключей не использует, см. `race/race_test.go`). Разные адреса в отчёте
(`0x00c0000c86c0`, `0x00c000208018`) — это внутренняя память map
(заголовок/бакеты/слоты `mapassign_faststr`/`mapaccess1_faststr` без
happens-before), а НЕ разные ключи карты. Детектор завершает тест с
`FAIL` (`testing.go:1712: race detected during execution of test`) — это
ожидаемый, «сломанный» артефакт задачи: код в `race/cache_race.go`
(`UnsyncCache`) намеренно не синхронизирован.

**Недетерминизм гонки — честная оговорка**: точное число и порядок
`WARNING: DATA RACE` (2–4 инцидента за прогон), а иногда и исход прогона
варьируются от запуска к запуску — это неотъемлемое свойство гонки данных
(нет фиксированного happens-before, планировщик горутин недетерминирован).
В одном из прогонов этой ревизии детектор успел напечатать 2 предупреждения
`WARNING: DATA RACE`, после чего рантайм упал собственным фаталом `fatal
error: concurrent map writes` до завершения теста — тоже валидный и
ожидаемый исход (гонка реальна и ловится в обоих случаях), просто менее
удобный для цитирования, чем чистый `FAIL` с полным списком инцидентов
выше.

### `TestSyncCache_NoRace` — та же нагрузка, `sync.RWMutex`, чисто

Команда: `go test ./race/ -run TestSyncCache_NoRace -race -v -count=1`.

```
=== RUN   TestSyncCache_NoRace
--- PASS: TestSyncCache_NoRace (0.00s)
PASS
ok  	khorost.tech/go-testing/race	1.012s
```

Идентичная конкурентная нагрузка (50× параллельных `Set`/`Get` на один
ключ) через `SyncCache` (`Get` под `RLock`, `Set` под `Lock`) — под `-race`
абсолютно чисто, ни одного предупреждения.

### Контроль race-тайминга (Фикс 2 ревизии) — `1.012s` не компиляция

Автор-ревью справедливо усомнился: `1.012s` на тест с телом `0.00s` — не
объясняется компиляцией race-инструментированного бинаря (она происходит
один раз до запуска тестового процесса и не входит в `ok`-строку `go
test`, которая мерит только время выполнения). Проверено вживую (WSL2)
подавлением послетестовой паузы race-рантайма через `GORACE=atexit_sleep_ms=0`:

```
$ GOPROXY=https://go.khorost.tech,direct go test ./race/ -run TestSyncCache_NoRace -race -count=1
ok  	khorost.tech/go-testing/race	1.012s

$ GORACE=atexit_sleep_ms=0 GOPROXY=https://go.khorost.tech,direct go test ./race/ -run TestSyncCache_NoRace -race -count=1
ok  	khorost.tech/go-testing/race	0.010s
```

**`1.012s` (по умолчанию) → `0.010s` (`atexit_sleep_ms=0`)** — воспроизведено
дважды, оба раза с точностью до миллисекунд. Разница ровно объясняется
дефолтом race-рантайма Go: `GORACE=atexit_sleep_ms` по умолчанию **1000**
(документировано в `go doc cmd/vet` / race detector docs) — при выходе из
процесса, скомпилированного с `-race`, рантайм дополнительно спит 1000мс
перед завершением (это внутренний механизм race-детектора, задуманный
для того, чтобы дать фоновым горутинам шанс на срабатывание детектора
перед выходом процесса, а не задержка компиляции или самого теста).
Правильная формулировка для статьи: **«~1с — это `atexit_sleep_ms=1000`
race-рантайма, а не время компиляции»** — предыдущая версия текста этот
пункт объясняла неверно.

---

## §coverage — 100% покрытия операторов (statements) при живом баге

Команда: `GOPROXY=https://go.khorost.tech,direct go test ./coverage/ -cover -v -count=1`.

```
=== RUN   TestDiscountedPrice
=== RUN   TestDiscountedPrice/no_discount
=== RUN   TestDiscountedPrice/ten_percent
=== RUN   TestDiscountedPrice/half_price
=== RUN   TestDiscountedPrice/full_discount
--- PASS: TestDiscountedPrice (0.00s)
    --- PASS: TestDiscountedPrice/no_discount (0.00s)
    --- PASS: TestDiscountedPrice/ten_percent (0.00s)
    --- PASS: TestDiscountedPrice/half_price (0.00s)
    --- PASS: TestDiscountedPrice/full_discount (0.00s)
=== RUN   TestDiscountedPrice_BugCaught
    validate_test.go:55: demo-тест выключен по умолчанию; запустить: DEMO_BUG_CAUGHT=1 go test -run BugCaught -v ./coverage/
--- SKIP: TestDiscountedPrice_BugCaught (0.00s)
PASS
coverage: 100.0% of statements
ok  	khorost.tech/go-testing/coverage	0.044s	coverage: 100.0% of statements
```

**Точное покрытие: `coverage: 100.0% of statements`** — воспроизведено
дважды (первый прогон из кеша `go test`, второй с `-count=1` без кеша, оба
раза `100.0%`). Пакет `coverage/` состоит из одной функции
`DiscountedPrice(base, pct int) int` (`coverage/validate.go`) без ветвлений
(`discount := base*pct/100; return base - discount`) — четыре кейса
`TestDiscountedPrice` (`pct` = 0, 10, 50, 100) исполняют оба оператора
функции целиком, отсюда 100% покрытия операторов (statements) уже на
«нормальных» входах.

**Живой баг**: при `pct > 100` (за пределами разумного диапазона `0..100`)
`DiscountedPrice` не валидирует вход и возвращает **отрицательную** цену.
Живая демонстрация (`DEMO_BUG_CAUGHT=1`, тест по умолчанию `SKIP`, чтобы
основной прогон оставался зелёным):

Команда: `DEMO_BUG_CAUGHT=1 go test ./coverage/ -run BugCaught -v` (exit code 1).

```
=== RUN   TestDiscountedPrice_BugCaught
    validate_test.go:61: DiscountedPrice(1000, 150) = -500 — цена ушла в минус: pct>100 не валидируется (баг пойман)
--- FAIL: TestDiscountedPrice_BugCaught (0.00s)
FAIL
FAIL	khorost.tech/go-testing/coverage	0.041s
FAIL
```

**`DiscountedPrice(1000, 150) = -500`** — реальное отрицательное число,
не гипотетическое. **Что именно пропускает 100%-покрытая
`TestDiscountedPrice`**: у бага нет отдельного оператора/ветки, которую
тесты могли бы «не задеть» — оба оператора функции исполняются на любом
входе, включая `pct=150`. Значит метрика покрытия операторов (statements)
отвечает только на вопрос «был ли оператор исполнен хотя бы раз», а не
«проверены ли граничные/некорректные значения входа». Пропущенный кейс —
не пропущенный оператор кода, а пропущенное **значение** (`pct` вне
диапазона `0..100`), которое стандартный `go test -cover` в принципе не
способен обнаружить:
для этого нужен либо явный тест на границу (как
`TestDiscountedPrice_BugCaught`), либо property-based/fuzz-тестирование
инварианта «цена после скидки не может быть отрицательной».

---

## §тайминг-сравнение — юниты vs интеграция

Все числа — `ok` строки `go test` из живых прогонов выше (не усреднены,
конкретный прогон), нативный Windows для юнитов/httptest/coverage/race
(без `-race`), WSL2 для интеграции и `-race`.

| Прогон | Команда | Wall-time | Где |
|---|---|---:|---|
| `cache` — юниты (7 табличных кейсов + TTL-тест, фейки) + httptest (recorder/method+error/server), `-short` | `go test ./cache/ -short -count=1` | **~0.09–0.29s** (весь пакет, включая старт тестового бинаря; нативный тайминг заметно шумит от прогона к прогону) | нативный Windows |
| `cache` — тот же `-short`, но в WSL2 (для сравнения с интеграцией в ОДНОМ окружении) | `go test ./cache/ -short -count=1` | **0.035–0.041s** | WSL2 |
| `coverage` — юниты + `-cover` | `go test ./coverage/ -cover -count=1` | **0.044s** | нативный Windows |
| `race` — обе функции БЕЗ `-race`, `TestUnsyncCache_Race` — SKIP (гейт `DEMO_RACE`) | `go test ./race/ -short -count=1` | **0.069–0.084s** | нативный Windows |
| `race` — `TestSyncCache_NoRace` С `-race`, дефолт `atexit_sleep_ms=1000` | `go test ./race/ -run TestSyncCache_NoRace -race -count=1` | **1.012s** (сама логика теста `0.00s`, остальное — послетестовая пауза race-рантайма `atexit_sleep_ms=1000`, НЕ компиляция — см. §race) | WSL2 |
| `race` — то же, `GORACE=atexit_sleep_ms=0` | `GORACE=atexit_sleep_ms=0 go test ./race/ -run TestSyncCache_NoRace -race -count=1` | **0.010s** | WSL2 |
| `cache` — **интеграция** (testcontainers, Postgres+Redis, пиновые `17.2-alpine`/`7.4-alpine`) | `go test ./cache/ -run TestIntegration -v -count=1` | **5.642s** | WSL2, Docker уже прогрет (образы локальны) |

**Вывод**: юнит-тесты и httptest (fakes/recorder/сервер в памяти) —
десятки **миллисекунд** на весь пакет, включая накладные расходы самого
`go test`. Интеграционный тест с реальными Postgres+Redis в Docker —
**секунды** (~5.6 с даже на прогретом кеше образов, без единой сетевой
загрузки слоёв). Это **разница на два порядка** — устойчивый вывод; но
точный множитель — иллюстративная точка одиночного прогона, а не
усреднённый бенчмарк, и он зависит от окружения. Кросс-окруженческое
сравнение (0.109 с юниты на нативном Windows против 5.642 с интеграции в
WSL2) даёт **~52×**; замер в ОДНОМ окружении (оба прогона в WSL2: `-short`
≈ 0.035–0.041 с против интеграции ≈ 5.6 с) — **~150×**. Оба показывают
одно: не конкретное число, а порядок величины — миллисекунды против
секунд. Разница — не накладные расходы теста, а
реальное время старта двух отдельных контейнеров БД + wait-стратегии
(`BasicWaitStrategies()` для Postgres ждёт двойной лог рестарта + порт,
Redis — лог `Ready to accept connections`) + сам сетевой round-trip к
Postgres/Redis внутри контейнеров. Отсюда практический вывод для дизайна
тестовой пирамиды: юнит-тесты с фейками — для быстрой обратной связи в
цикле разработки (можно гонять на каждое сохранение), testcontainers-
интеграция — для CI/pre-merge проверки реальных контрактов с БД, но не для
цикла «сохранил — увидел результат» из-за на порядок большего wall-time.

Отдельно: `1.012s` у `TestSyncCache_NoRace` под `-race` — **не**
компиляция и **не** более медленный вариант интеграции, это фиксированная
послетестовая пауза race-рантайма (`GORACE=atexit_sleep_ms=1000` по
умолчанию), см. разбор в §race выше — при `atexit_sleep_ms=0` та же
команда укладывается в `0.010s`. Это показывает лишь, что та секунда — это
послетестовая пауза `atexit_sleep_ms`, а не работа детектора; собственно
overhead инструментирования `-race` этим микротестом НЕ измеряется (в
общем случае — 2–20× по памяти и времени).
