# go-concurrency — конкурентность в Go на живом стенде

Примеры к серии статей [«Конкурентность в Go»](https://khorost.tech/go/go-concurrency-goroutines-channels/) на khorost.tech.
Чистый Go, без docker. Один модуль `tech.khorost/go-concurrency-cookbook`, пять подкаталогов — по статье серии.

Идея стенда: **корректные паттерны покрыты тестами, которые проходят под `-race` и `goleak`; типичные дефекты (гонки, утечки, дедлоки) вынесены в отдельные запускаемые демо** `cmd/*` — их запускают руками, чтобы своими глазами увидеть отчёт детектора или сообщение рантайма. Поэтому `go test ./...` всегда зелёный, а «сломанное» не мешает suite.

## Запуск

    # весь модуль: сборка, vet, тесты
    go build ./...
    go vet ./...
    go test ./...

    # с детектором гонок (нужен C-компилятор; на Windows без gcc — через контейнер)
    go test -race ./...
    docker run --rm -e CGO_ENABLED=1 -v "$PWD":/src -w /src golang:1.26 go test -race ./...

    # бенчмарки (числа снимайте у себя — в стенде их намеренно нет)
    go test -bench=. -benchmem ./sync/

## Что где

| Подкаталог | Статья серии | Содержимое |
|---|---|---|
| [`goroutines/`](goroutines) | #1 Горутины и каналы | worker pool (`select` на приёме И отправке), pipeline с отменой по ctx, fan-out/fan-in, `errgroup`; тесты на корректность + `goleak` |
| [`sync/`](sync) | #2 Пакет sync и атомики | Mutex vs `atomic.Int64`, `RWMutex`-кэш, `sync.Pool`, `sync.Map`, CAS-цикл, `Once`/`OnceValue`, бенч false sharing (паддинг) |
| [`memory-model/`](memory-model) | #3 Модель памяти (happens-before) | безопасная публикация (`atomic.Pointer`), сигнал через `atomic.Bool`/канал, happens-before на каналах; сломанные версии — в `cmd/` |
| [`patterns/`](patterns) | #4 Паттерны конкурентности | pipeline, fan-out/in, worker pool, `semaphore.Weighted`, `errgroup` (SetLimit/TryGo), `singleflight`, rate limiting, or-done, tee, bridge, батчинг/дебаунс — каждый с тестом на отсутствие утечек |
| [`debugging/`](debugging) | #5 Отладка: гонки, утечки, дедлоки | исправленные счётчик/воркер + профили block/mutex (тесты); демо гонки/утечки/дедлоков — в `cmd/` |

## Демо дефектов (запускать вручную)

Эти программы **специально сломаны** — они показывают, как дефект выглядит в выводе инструмента:

| Команда | Что увидите (сверено живьём) |
|---|---|
| `go run -race ./debugging/cmd/race-counter` | `WARNING: DATA RACE` — конкурентные Read/Write одной ячейки из разных горутин; итог меньше ожидаемого |
| `go run ./debugging/cmd/goroutine-leak` | `runtime.NumGoroutine()` растёт и не падает — горутины навсегда висят на `<-ch` (виден в goroutine-профиле) |
| `go run ./debugging/cmd/full-deadlock` | `fatal error: all goroutines are asleep - deadlock!` — рантайм сам аварийно завершает процесс |
| `go run ./debugging/cmd/partial-deadlock` | программа виснет, детектор рантайма **молчит** (main жив); дефект видно только в goroutine-дампе (два `sync.Mutex.Lock` в обратном порядке) |
| `go run -race ./memory-model/cmd/broken-flag` | `WARNING: DATA RACE` на незащищённом флаге; без синхронизации busy-wait может крутиться вечно |
| `go run -race ./memory-model/cmd/publish-unsafe` | `WARNING: DATA RACE` — читатель видит ненулевой указатель, но недостроенные поля |
| `go run ./memory-model/cmd/race-condition-no-data-race` | детектор молчит (каждая операция под замком), но составной инвариант нарушен — двойное списание и отрицательный баланс |

> `goroutine-leak` печатает рост числа горутин и их дамп, затем процесс завершается (зависшие горутины видны в дампе до выхода). `partial-deadlock` **зависает навсегда** — прервите его (Ctrl+C).

## Версии

Проверено 2026-07 на **Go 1.26.3** (локально) и **golang:1.26** (контейнер, go1.26.4) — `go build/vet/test ./...` и `go test -race ./...` зелёные, демо-дефекты воспроизводятся.
Модуль объявляет `go 1.25.0` (нужен для `testing/synctest`, `WaitGroup.Go`, cgroup-aware `GOMAXPROCS`). Зависимости: `go.uber.org/goleak v1.3.0`, `golang.org/x/sync v0.22.0`, `golang.org/x/time v0.15.0`.
