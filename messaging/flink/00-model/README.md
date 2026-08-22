# 00-model — event-time, watermarks и окна

Демо к статье [«Модель Flink: dataflow, event-time, watermarks и окна»](https://khorost.tech/messaging/flink-model-event-time-watermarks/).

Самодостаточный стенд **без Kafka**: источник — встроенный коннектор `datagen`, сток — `print`.
Показывает суть модели времени Flink на живых данных.

## Запуск

```bash
docker compose up -d
```

Flink Dashboard: <http://localhost:8081> (JobManager, слоты, метрики).

## SQL client

```bash
docker compose exec jobmanager ./bin/sql-client.sh
```

В клиенте выполнить блоки из [`sql/windows.sql`](sql/windows.sql):
создать источник `clicks` (с `WATERMARK`), сток `out_counts` и `INSERT` с tumbling-окном по event-time.

Результат окон печатается в лог TaskManager:

```bash
docker compose logs -f taskmanager
```

Строки вида `+I[2026-08-19T15:00:10, 2026-08-19T15:00:20, 187]` — закрытые 10-секундные окна
и число событий в них. При `rows-per-second = 20` на окно приходится в среднем 200 событий; фактически
окна набирают в среднем около 180 (разброс по окнам ~150–205) — самые опоздавшие приходят уже после
закрытия окна.
Окно закрывается **по watermark**, а не по стенным часам.

## Что попробовать

- **Опоздавшие события.** `datagen` даёт разброс event_time до 10 c, а watermark допускает опоздание 5 c
  (`event_time - INTERVAL '5' SECOND`). События старше — «слишком поздние» и в окно не попадают.
  Уменьшите допуск до `1 SECOND` и увидите, как счётчики окон падают: около 180 -> около 130.
- **Event-time vs processing-time.** Замените окно на processing-time
  (`TUMBLE` по `PROCTIME()`), и результат перестанет зависеть от разброса задержек —
  но станет «нечестным»: одно и то же событие в разных прогонах попадёт в разные окна.
- **Sliding-окно.** `HOP(TABLE clicks, DESCRIPTOR(event_time), INTERVAL '5' SECOND, INTERVAL '10' SECOND)` —
  окна по 10 c с шагом 5 c, каждое событие в двух окнах.
- **Session-окно.** `SESSION(...)` — окно закрывается после паузы без событий.

## Остановка

```bash
docker compose down -v
```
