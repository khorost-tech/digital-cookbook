-- Event-time, watermarks и окна на встроенном источнике datagen.
-- Запуск: в SQL client вставить блоками (см. README).

-- Источник кликов: event_time с искусственной задержкой и разбросом (out-of-order),
-- watermark допускает опоздание до 5 секунд.
CREATE TEMPORARY TABLE clicks (
    user_id    INT,
    url        STRING,
    -- событие "произошло" с отставанием 0..10 c от текущего времени -> out-of-order.
    -- Множитель интервала обязан быть целым: INTERVAL '10' SECOND * RAND() проходит
    -- валидацию, но падает на кодогенерации ("Unsupported casting from DOUBLE to INTERVAL"),
    -- поэтому разброс задаём в миллисекундах целым числом.
    event_time AS CAST(CURRENT_TIMESTAMP - INTERVAL '0.001' SECOND * CAST(RAND() * 10000 AS INT) AS TIMESTAMP(3)),
    WATERMARK FOR event_time AS event_time - INTERVAL '5' SECOND
) WITH (
    'connector' = 'datagen',
    'rows-per-second' = '20',
    'fields.user_id.min' = '1',
    'fields.user_id.max' = '5',
    'fields.url.length' = '8'
);

-- Печатаем в лог TaskManager.
CREATE TEMPORARY TABLE out_counts (
    window_start TIMESTAMP(3),
    window_end   TIMESTAMP(3),
    cnt          BIGINT
) WITH ('connector' = 'print');

-- Tumbling-окно 10 c по EVENT-TIME (TVF-синтаксис Flink SQL).
-- Окно закрывается по watermark, а не по стенным часам -> опоздавшие в пределах 5 c учтены.
INSERT INTO out_counts
SELECT window_start, window_end, COUNT(*) AS cnt
FROM TABLE(
    TUMBLE(TABLE clicks, DESCRIPTOR(event_time), INTERVAL '10' SECOND)
)
GROUP BY window_start, window_end;
