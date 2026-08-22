package tech.khorost.cookbook.flink;

import org.apache.flink.api.common.eventtime.WatermarkStrategy;
import org.apache.flink.api.common.state.ValueState;
import org.apache.flink.api.common.state.ValueStateDescriptor;
import org.apache.flink.api.common.typeinfo.TypeInformation;
import org.apache.flink.api.common.typeinfo.Types;
import org.apache.flink.api.connector.source.util.ratelimit.RateLimiterStrategy;
import org.apache.flink.configuration.Configuration;
import org.apache.flink.connector.datagen.source.DataGeneratorSource;
import org.apache.flink.connector.datagen.source.GeneratorFunction;
import org.apache.flink.streaming.api.datastream.SingleOutputStreamOperator;
import org.apache.flink.streaming.api.environment.StreamExecutionEnvironment;
import org.apache.flink.streaming.api.functions.KeyedProcessFunction;
import org.apache.flink.util.Collector;
import org.apache.flink.util.OutputTag;

import java.time.Duration;

/**
 * SilenceDetector — детект РАЗРЫВА в event-time по ключу (нет событий дольше GAP по времени
 * событий): то, что Flink SQL выражает разве что session-окном, а руками — гибче.
 *
 * <p>Демонстрирует низкоуровневый DataStream API и три тонкости, которые легко упустить:
 * <ul>
 *   <li>{@link KeyedProcessFunction} + {@link ValueState} — хранит МАКСИМУМ event-time по ключу;
 *   <li>event-time таймеры (register/delete/onTimer) — срабатывают по watermark;
 *   <li>ЯВНАЯ политика late-событий: пришедшее ниже watermark уводится в side output, а не
 *       регистрирует таймер в уже пройденном времени (иначе — ложный мгновенный GAP);
 *   <li>устойчивость к out-of-order: событие не новее максимума не откатывает дедлайн.
 * </ul>
 * <b>Это НЕ wall-clock liveness</b> (для «источник замолчал» нужен processing-time таймер — в статье).
 */
public final class SilenceDetector {

    static final long SILENCE_GAP_MS = 5_000;
    static final long OOO_BOUND_MS = 5_000;   // допуск неупорядоченности watermark-стратегии
    static final long EVENTS = 400;
    static final int KEYS = 4;
    static final long STEP_MS = 500;
    static final long T0 = 1_700_000_000_000L;

    /** Опоздавшие (ниже watermark) — политика вынесена явно. */
    public static final OutputTag<Event> LATE =
            new OutputTag<>("late", TypeInformation.of(Event.class));
    /** Не по порядку, но не опоздавшие (между watermark и максимумом) — дедлайн не двигаем. */
    public static final OutputTag<Event> OUT_OF_ORDER =
            new OutputTag<>("out-of-order", TypeInformation.of(Event.class));

    public static void main(String[] args) throws Exception {
        StreamExecutionEnvironment env = StreamExecutionEnvironment.getExecutionEnvironment();
        env.setParallelism(1);   // строгий порядок источника -> out-of-order только от намеренных
                                 // сдвигов в gen(), число OOO воспроизводимо (иначе шаффл добавляет шум)

        DataGeneratorSource<Event> source = new DataGeneratorSource<>(
                (GeneratorFunction<Long, Event>) SilenceDetector::gen,
                EVENTS,
                RateLimiterStrategy.perSecond(2_000),
                TypeInformation.of(Event.class));

        WatermarkStrategy<Event> wm = WatermarkStrategy
                .<Event>forBoundedOutOfOrderness(Duration.ofMillis(OOO_BOUND_MS))
                .withTimestampAssigner((e, ts) -> e.eventTime);

        SingleOutputStreamOperator<String> alerts = env
                .fromSource(source, wm, "datagen")
                .keyBy(e -> e.key)
                .process(new Silence())
                .name("silence-detector");

        alerts.print("ALERT");
        alerts.getSideOutput(LATE).map(e -> "LATE key=" + e.key + " eventTime=" + e.eventTime).print("LATE");
        alerts.getSideOutput(OUT_OF_ORDER).map(e -> "OOO key=" + e.key + " eventTime=" + e.eventTime).print("OOO");

        env.execute("silence-detector (DataStream)");
    }

    /** Детерминированный генератор события по индексу. */
    static Event gen(long i) {
        int k = (int) (i % KEYS);
        // sensor-3 «замолкает» на второй половине потока -> разрыв в его event-time детектится таймером.
        if (k == 3 && i >= EVENTS / 2) {
            k = (int) (i % 3);
        }
        long eventTime = T0 + i * STEP_MS;
        // Каждое 20-е событие приходит НЕ ПО ПОРЯДКУ на 3с назад: для своего ключа (события раз в
        // 2с) это старше предыдущего, но в пределах OOO-границы 5с -> ветка out-of-order реально
        // исполняется (не late). Проверяемо: в логе появляются строки OOO.
        if (i % 20 == 0 && i > KEYS) {
            eventTime -= 3_000;   // 3с назад: старше предыдущего события ключа (раз в 2с), но в пределах 5с
        }
        return new Event("sensor-" + k, eventTime, i);
    }

    static final class Silence extends KeyedProcessFunction<String, Event, String> {
        /** Максимум event-time, виденный по ключу (не «последний пришедший» — из-за out-of-order). */
        private transient ValueState<Long> maxTs;

        @Override
        public void open(Configuration cfg) {
            maxTs = getRuntimeContext().getState(new ValueStateDescriptor<>("maxTs", Types.LONG));
        }

        @Override
        public void processElement(Event e, Context ctx, Collector<String> out) throws Exception {
            long watermark = ctx.timerService().currentWatermark();
            // Late: watermark уже прошёл — событий с таким timestamp больше не ждём. Явная политика:
            // в side output (не трогаем state/таймер, иначе зарегистрировали бы таймер в прошлом).
            if (watermark != Long.MIN_VALUE && e.eventTime <= watermark) {
                ctx.output(LATE, e);
                return;
            }
            Long max = maxTs.value();
            // Out-of-order (не late): активность есть, но дедлайн привязан к МАКСИМУМУ — не откатываем.
            if (max != null && e.eventTime <= max) {
                ctx.output(OUT_OF_ORDER, e);
                return;
            }
            if (max != null) {
                ctx.timerService().deleteEventTimeTimer(max + SILENCE_GAP_MS);
            }
            maxTs.update(e.eventTime);
            ctx.timerService().registerEventTimeTimer(e.eventTime + SILENCE_GAP_MS);
        }

        @Override
        public void onTimer(long ts, OnTimerContext ctx, Collector<String> out) throws Exception {
            Long max = maxTs.value();
            if (max != null && ts == max + SILENCE_GAP_MS) {   // null-safe: только актуальный дедлайн
                out.collect("GAP key=" + ctx.getCurrentKey()
                        + " afterEventTime=" + max + " gapMs=" + SILENCE_GAP_MS);
                maxTs.clear();
            }
        }
    }

    /** Событие: ключ, event-time (ms) и значение. Публичный POJO с пустым конструктором — для сериализатора Flink. */
    public static final class Event {
        public String key;
        public long eventTime;
        public long value;

        public Event() {}

        public Event(String key, long eventTime, long value) {
            this.key = key;
            this.eventTime = eventTime;
            this.value = value;
        }
    }
}
