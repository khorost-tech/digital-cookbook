# Temporal: durable execution вглубь — стенды

Живые стенды к серии статей «Temporal: durable execution вглубь» на
[khorost.tech](https://khorost.tech).

Этот каталог — **фундамент серии** (8 статей). Здесь общий Temporal-сервер в
режиме `server start-dev` и суб-стенды, по одному на большую тему. На момент
создания готов только первый суб-стенд — **`00-paradigm`** (durable execution
на пальцах). Остальные — заготовки под следующие заходы:

| Суб-стенд        | Тема                                                        | Статус       |
|------------------|-------------------------------------------------------------|--------------|
| `00-paradigm`    | Durable execution: выполнение переживает падение воркера     | **готов**    |
| `01-internals`   | Внутренности: event history, replay, task queues            | планируется  |
| `02-timers-signals` | Таймеры, сигналы, запросы, `ContinueAsNew`               | планируется  |
| `03-saga`        | Saga / компенсации на длинных транзакциях                    | планируется  |
| `04-versioning`  | Версионирование воркфлоу, безопасный деплой изменений        | планируется  |
| `05-testing`     | Тестирование воркфлоу (replay-тесты, time-skipping)         | планируется  |
| `06-observability` | Наблюдаемость: метрики воркера, трейсинг, Web UI          | планируется  |
| `07-languages`   | Один воркфлоу на разных SDK (Go/Java/…)                     | планируется  |

## Версии (сверено живьём 2026-07-08)

| Компонент | Версия | Как проверено |
|---|---|---|
| Temporal (образ `temporalio/temporal`) | `1.7.2` (CLI) = **Server 1.31.1**, **UI 2.49.1** | `docker pull temporalio/temporal` + `docker run --rm temporalio/temporal:1.7.2 --version`. Тег `latest` на момент сборки указывал ровно на `1.7.2`; в compose пин зафиксирован явным тегом. |
| Temporal Go SDK | `go.temporal.io/sdk v1.46.0` | `go get go.temporal.io/sdk@latest` → `go.mod` (module proxy, latest на 2026-07-08) |
| Go | `1.25.x` (`go.mod` требует `go 1.25`, тулчейн подтянул `1.25.4`) | сборка `go build ./...`; кросс-компиляция `CGO_ENABLED=0 GOOS=linux` для прогона в контейнере |

## Топология

Один контейнер `temporal-devserver` (`server start-dev`) поднимает сразу весь
Temporal (frontend/history/matching/worker-роли), встроенное in-memory
хранилище и Web UI. Это **не** прод-топология (в проде — раздельные роли +
PostgreSQL/Cassandra + Elasticsearch), а компактный фундамент для локальной
разработки, на котором стоят все суб-стенды серии.

| Что | Внутри контейнера | С хоста | Примечание |
|---|---|---|---|
| gRPC frontend (SDK-клиент/воркер) | `7233` | **`7253`** | `7233` занят стендом event-coordination, `7243` — saga |
| Web UI | `8233` | **`8253`** | http://localhost:8253 |

- Имя проекта compose зафиксировано (`name: temporal-cookbook`) — иначе
  docker compose берёт имя каталога (`compose`), общее у всех стендов
  репозитория, и начинает считать чужие контейнеры «орфанами».
- Данные эфемерны (in-memory dev store): после `down` вся история воркфлоу
  пропадает. Durable execution здесь демонстрируется в пределах жизни **сервера**
  при падении **воркера**, а не при перезапуске самого сервера.

## Как поднять сервер

```bash
docker compose -f temporal/compose/compose.yml up -d
docker compose -f temporal/compose/compose.yml ps      # дождаться "healthy"
# Web UI: http://localhost:8253
docker compose -f temporal/compose/compose.yml down     # остановить
```

Хостового Temporal CLI / Go-тулчейна для проверки не требуется — воркфлоу
можно гонять как локальным Go-бинарём, так и внутри контейнера (см. ниже).

## Суб-стенд `00-paradigm`: durable execution

Демонстрирует **главное свойство Temporal**: выполнение воркфлоу переживает
падение воркера. Воркфлоу «провизионинг ресурса»:

```
CheckAvailability → Reserve → durable-пауза (Sleep 15s)
  → ждём Signal "confirmation" с таймаутом (5m)
    → Allocate            (если подтверждено)
    → CancelReservation   (если таймаут / явный отказ — компенсация)
```

Ключевые идеи, которые видно в коде и логе:

- **Детерминизм.** Код воркфлоу (`ProvisioningWorkflow` в `workflow.go`) не
  делает прямого I/O — только через `workflow.*` API. Любой побочный эффект
  вынесен в **activity**. Поэтому Temporal может «переиграть» (replay) историю
  событий после падения воркера и получить ровно то же состояние.
- **Activity не выполняются дважды.** Результат каждой activity записан в
  историю на сервере. После перезапуска воркфлоу проигрывается заново, но
  activity, уже отработавшие, **не вызываются повторно** — результат берётся
  из истории. Доказательство — счётчик «РЕАЛЬНОЕ выполнение №N» в логе воркера:
  он локален процессу, после рестарта считает с нуля, и уже сделанных activity
  в логе нового процесса нет.
- **Signal как «человек в цикле».** Подтверждение приходит внешним сигналом
  `confirmation`; ожидание сигнала и таймер живут на **сервере**, а не в памяти
  воркера, поэтому переживают его падение.

### Три процесса (по терминалу на каждый)

```bash
# 1) Воркер — ЕГО мы убиваем и перезапускаем
go run . start-worker

# 2) Стартер — запускает воркфлоу и блокируется, ожидая итог
go run . start-workflow -resource=gpu-node-7

# 3) Подтверждение (человек в цикле)
go run . send-signal -approve          # Allocate (успех)
go run . send-signal                    # без -approve → отказ → CancelReservation
```

Все три ходят на `localhost:7253` по умолчанию (флаг `-address`).

> **Прогон с хоста в этом окружении.** Локально собранный Go-бинарь на данной
> Windows-машине не смог достучаться до `localhost:7253` / `127.0.0.1:7253`
> (dial висит на `[::1]`/IPv4 и отваливается по таймауту) — та же особенность
> ОС/файрвола, что задокументирована в стенде `kafka` (порт открыт и
> подтверждён, но локальные бинари блокируются). Надёжный способ, которым и
> сделан прогон ниже, — запускать воркер/стартер **внутри сети compose**,
> подключаясь к сервису `temporal:7233`:
>
> ```bash
> # кросс-компиляция статического бинаря и прогон в контейнере на сети стенда
> CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/paradigm ./00-paradigm
> NET=temporal-cookbook_default
> docker run -d  --name tw1    --network $NET -v /tmp/paradigm:/paradigm:ro alpine:3 /paradigm start-worker   -address=temporal:7233
> docker run -d  --name tstart --network $NET -v /tmp/paradigm:/paradigm:ro alpine:3 /paradigm start-workflow -address=temporal:7233 -resource=gpu-node-7
> docker kill tw1                                                            # убить воркер в середине
> docker run -d  --name tw2    --network $NET -v /tmp/paradigm:/paradigm:ro alpine:3 /paradigm start-worker   -address=temporal:7233
> docker run --rm             --network $NET -v /tmp/paradigm:/paradigm:ro alpine:3 /paradigm send-signal    -address=temporal:7233 -approve
> ```

### Пошаговый сценарий демонстрации durability

1. Поднимите сервер (`docker compose … up -d`, дождитесь `healthy`).
2. **Терминал A:** `go run . start-worker` — воркер ждёт задачи.
3. **Терминал B:** `go run . start-workflow -resource=gpu-node-7` — воркфлоу
   стартует; в терминале A видно `CheckAvailability` и `Reserve`, затем
   «durable-пауза … sleep 15s».
4. **Убейте воркер A** (Ctrl+C или `kill -9`) во время паузы или ожидания
   сигнала. Стартер в терминале B продолжает спокойно ждать — состояние
   воркфлоу на сервере, не в воркере.
5. **Терминал A снова:** `go run . start-worker` — новый воркер подхватывает
   воркфлоу. В его логе **нет** ни `CheckAvailability`, ни `Reserve` — только
   продолжение с точки ожидания сигнала.
6. **Терминал C:** `go run . send-signal -approve` — приходит подтверждение,
   выполняется `Allocate` (в логе нового воркера — «РЕАЛЬНОЕ выполнение №1»,
   счётчик с нуля), воркфлоу завершается, стартер печатает результат.
7. Загляните в Web UI http://localhost:8253 → воркфлоу `provisioning-demo`:
   видно всю историю событий, момент простоя между воркерами и итог `Completed`.

Для ветки компенсации повторите без `-approve` (или дождитесь 5-минутного
таймаута) — вместо `Allocate` отработает `CancelReservation`.

### Реальный прогон (проверено живьём 2026-07-08)

Воркер #1 запустил воркфлоу, выполнил две первые activity и вошёл в паузу —
после чего был **убит** (`docker kill`):

```
[worker] запущен, очередь="provisioning-tq", сервер=temporal:7233
INFO  workflow старт  RunID 019f41c7-9d6a-734e-ac23-2cdad437f8fb  resource gpu-node-7
>>> ACTIVITY CheckAvailability("gpu-node-7") — РЕАЛЬНОЕ выполнение №1 в этом процессе воркера
>>> ACTIVITY Reserve("gpu-node-7") — РЕАЛЬНОЕ выполнение №2 → res-gpu-node-7-001
INFO  ресурс зарезервирован  reservationID res-gpu-node-7-001
INFO  durable-пауза перед ожиданием подтверждения  sleep 15s
                                          <-- здесь воркер #1 УБИТ (docker kill) -->
```

Воркер #2 (новый процесс, другой `WorkerID`) подхватил **тот же** `RunID` и
продолжил с точки ожидания сигнала — **`CheckAvailability`/`Reserve` заново НЕ
выполнялись** (их нет в логе), а `Allocate` идёт как «выполнение №1» (счётчик
процесса с нуля):

```
[worker] запущен, очередь="provisioning-tq", сервер=temporal:7233
INFO  Started Worker  WorkerID 1@95b60cac6881@
INFO  ждём сигнал подтверждения  RunID 019f41c7-9d6a-734e-ac23-2cdad437f8fb  signal confirmation timeout 5m
INFO  получен сигнал подтверждения  approved true by operator
>>> ACTIVITY Allocate("res-gpu-node-7-001") — РЕАЛЬНОЕ выполнение №1
INFO  workflow завершён успешно  result ресурс выделен по брони res-gpu-node-7-001
```

Стартер всё это время просто ждал и в конце получил результат:

```
[starter] воркфлоу запущен: WorkflowID=provisioning-demo RunID=019f41c7-9d6a-734e-ac23-2cdad437f8fb
[starter] жду результат воркфлоу (это НЕ мешает убивать/перезапускать воркер)...
[starter] РЕЗУЛЬТАТ: ресурс выделен по брони res-gpu-node-7-001
```

**Что доказано.** Один и тот же `RunID 019f41c7-…` в логах обоих воркеров —
это одно продолжающееся durable-выполнение, разорванное убийством воркера
посередине. Уже сделанные `CheckAvailability` и `Reserve` в новом процессе не
повторились (их результат — из истории на сервере), а воркфлоу дошло до
`Allocate` и завершилось успешно после перезапуска воркера. Это и есть durable
execution.

> Абсолютные тайминги и `RunID`/`WorkerID` в вашем прогоне будут другими —
> host-зависимо. Инвариант, который воспроизводится всегда: тот же `RunID`
> продолжается на новом воркере, а уже отработавшие activity повторно не
> вызываются.

## Структура

```
temporal/
  compose/compose.yml        # Temporal server start-dev (порты 7253/8253), name: temporal-cookbook
  go/
    go.mod                    # module tech.khorost/temporal-cookbook, go.temporal.io/sdk v1.46.0
    go.sum
    00-paradigm/              # суб-стенд #0: durable execution
      main.go                 # подкоманды start-worker | start-workflow | send-signal
      workflow.go             # ProvisioningWorkflow + activity (детерминизм, счётчик выполнений)
      worker.go               # воркер: регистрация воркфлоу/activity, тот, кого убиваем
      starter.go              # запуск воркфлоу (блокирующий) + отправка сигнала
  README.md
  .gitignore
```
