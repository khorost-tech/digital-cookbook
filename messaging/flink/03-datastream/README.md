# 03-datastream — Flink DataStream API: то, что SQL не выражает

Живой стенд к статье [«Flink DataStream API: когда SQL мало»](https://khorost.tech/messaging/flink-datastream-api/) серии [«Apache Flink: глубокое погружение»](https://khorost.tech/messaging/flink-model-event-time-watermarks/).

В отличие от модулей `00-model`/`02-sql-ops` (Flink SQL), здесь — **Java-первый DataStream API**: две джобы показывают то, чего декларативный SQL не выражает, — кастомную логику на событие, ручное состояние, реакцию по таймеру, корреляцию двух потоков и side output.

## Запуск

```bash
docker compose up -d --build
```

Multi-stage: `maven` собирает fat-jar → в образ с Flink CLI; кластер (`jobmanager` + `taskmanager`) поднимается, сервис `submit` сабмитит **обе джобы** и завершается. Вывод джоб — в логе TaskManager, дашборд — на <http://localhost:8081>.

## Джоба 1 — `SilenceDetector` (таймеры + состояние)

Детект **разрыва в event-time** по ключу `sensor-*`: алерт, если между событиями прошло > 5 c *по времени событий*. Показывает `KeyedProcessFunction` + `ValueState<Long>` (**максимум** event-time — корректно при событиях не по порядку) + **event-time таймеры** (register/delete/`onTimer`).

⚠️ Это **не** wall-clock liveness: event-time таймер срабатывает по watermark (при более позднем событии или на финальном watermark в конце потока). Остановившийся источник watermark не двигает — для «источник физически замолчал» нужен **processing-time** таймер (разобрано в статье). Генератор шлёт часть событий не по порядку на 3 c назад (в пределах bounded-out-of-orderness **5 c**) — max-логика не «откатывает» дедлайн, ложных алертов нет (в логе видно как `OOO`).

## Джоба 2 — `EnrichAndCorrelate` (connect + корреляция)

Слияние двух потоков (`orders` + `payments`) через `connect()` + `KeyedCoProcessFunction`: коррелирует заказ с платежом по `orderId`, обогащает пару вычисляемым `latencyMs`, а несостыкованные за 8 c (**event-time**) уводит в **side output** через таймаут-таймер. Кастомная логика корреляции + таймаут + маршрутизация — то, что `SELECT ... JOIN` так не описывает.

**Контракт:** ровно один заказ и один платёж на `orderId` (генератор гарантирует). `ValueState` держит по одной записи — для реальных потоков с дубликатами нужна дедупликация или `ListState`/`MapState`.

## Что смотреть (живые числа)

```bash
docker compose logs taskmanager | grep -c 'GAP key='          # 4   — разрывы event-time (см. ниже!)
docker compose logs taskmanager | grep -c 'OOO key='          # 19  — событий не по порядку (ветка обработана)
docker compose logs taskmanager | grep -c 'MATCH orderId='    # 265 — скоррелированных пар в окне [0..8с]
docker compose logs taskmanager | grep -c 'out-of-window'     # 1   — платёж вне окна (отвергнут в side output)
docker compose logs taskmanager | grep -cE 'без платежа|без заказа'  # 68 — несматченных по таймауту (side output)
```

Источник детерминированный (`DataGeneratorSource`, событие по индексу, `SilenceDetector` на parallelism=1), поэтому числа **воспроизводимы**: `GAP=4`, `OOO=19`, `MATCH=265`, `out-of-window=1`, `UNMATCHED=68`.

⚠️ **4 GAP читать честно:** только **один** реальный — `sensor-3`, замолчавший в середине потока; остальные **три** — от финального `MAX_WATERMARK` bounded-источника, который в конце «закрывает» все ключи (в бесконечном потоке этих трёх не было бы). `OOO=19` — намеренно сдвинутые назад события: max-логика их не принимает за новые (ложных GAP нет). `out-of-window=1` — демонстративный платёж на 100с позже заказа: matchOrReject проверяет окно и отвергает его, а не «матчит» вопреки таймауту.

Примеры строк:

```text
ALERT:1> GAP key=sensor-3 afterEventTime=1700000099500 gapMs=5000
OOO:1> OOO key=sensor-0 eventTime=...
MATCH:1> MATCH orderId=6 amount=106 latencyMs=500
UNMATCHED:1> out-of-window orderId=50 deltaMs=100000 (окно [0..8000]мс)
```

## Границы стенда

- **Агрегаты и простые джойны** — территория SQL; они в модулях `00-model`/`02-sql-ops`, здесь их намеренно нет.
- **Async I/O** (`AsyncDataStream`) и **broadcast state** — показаны фрагментами в статье: полноценное live-демо требует внешнего сервиса, а стенд держим «одна команда».
- **Processing-time liveness** («источник замолчал») и **маршрутизация опоздавших** — фрагментами в статье: в быстром bounded-прогоне processing-time таймеры и число опоздавших зависят от wall-clock/тайминга watermark'ов и невоспроизводимы; здесь показаны детерминированные event-time разрыв (джоба 1) и side output на несматченных (джоба 2).

## Версии

- Apache Flink `1.20.1` (Java 17) — пин серии; поведение state/timers меняется между минорами.
- Сборка jar — в контейнере `maven:3.9-eclipse-temurin-17` (на хост ничего не ставится).

## Остановка

```bash
docker compose down -v
```

## Лицензия

MIT — см. [LICENSE](../../LICENSE).
