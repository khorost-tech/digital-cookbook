package tech.khorost.cookbook.flink;

import org.apache.flink.api.common.eventtime.WatermarkStrategy;
import org.apache.flink.api.common.state.ValueState;
import org.apache.flink.api.common.state.ValueStateDescriptor;
import org.apache.flink.api.common.typeinfo.TypeInformation;
import org.apache.flink.api.connector.source.util.ratelimit.RateLimiterStrategy;
import org.apache.flink.configuration.Configuration;
import org.apache.flink.connector.datagen.source.DataGeneratorSource;
import org.apache.flink.connector.datagen.source.GeneratorFunction;
import org.apache.flink.streaming.api.datastream.DataStream;
import org.apache.flink.streaming.api.datastream.SingleOutputStreamOperator;
import org.apache.flink.streaming.api.environment.StreamExecutionEnvironment;
import org.apache.flink.streaming.api.functions.co.KeyedCoProcessFunction;
import org.apache.flink.util.Collector;
import org.apache.flink.util.OutputTag;

import java.time.Duration;

/**
 * EnrichAndCorrelate — «слияние» двух потоков, которого SQL-JOIN не выражает:
 * коррелировать заказ с платежом по одному ключу, с кастомным таймаутом, обогащая
 * пару вычисляемым полем (latency), а несостыкованные за таймаут — в side output.
 *
 * <p>Демонстрирует: {@code connect()} двух источников, {@link KeyedCoProcessFunction}
 * с общим состоянием по ключу, event-time таймеры для таймаута, side output.
 * Интервальный SQL-JOIN так гибко (произвольная логика + таймаут + маршрутизация
 * несматченных) не описывается.
 *
 * <p><b>Контракт:</b> ровно ОДИН заказ и ОДИН платёж на {@code orderId} (генератор это
 * гарантирует). {@link ValueState} держит по одной записи каждого типа — повторный
 * заказ/платёж на тот же ключ ПЕРЕЗАПИСАЛ бы прежний. Для реальных потоков с дубликатами
 * нужна дедупликация или {@code ListState}/{@code MapState} с явной политикой матчинга.
 *
 * <p><b>Таймаут — event-time</b> ({@code registerEventTimeTimer}): срабатывает, когда watermark
 * пройдёт дедлайн (при более поздних событиях или на финальном watermark), а не по wall-clock.
 */
public final class EnrichAndCorrelate {

    static final long MATCH_TIMEOUT_MS = 8_000;
    static final long ORDERS = 300;
    static final long STEP_MS = 500;
    static final long T0 = 1_700_000_000_000L;

    public static final OutputTag<String> UNMATCHED =
            new OutputTag<>("unmatched", TypeInformation.of(String.class));

    public static void main(String[] args) throws Exception {
        StreamExecutionEnvironment env = StreamExecutionEnvironment.getExecutionEnvironment();
        env.setParallelism(2);

        DataStream<Order> orders = env.fromSource(
                new DataGeneratorSource<>(
                        (GeneratorFunction<Long, Order>) EnrichAndCorrelate::order,
                        ORDERS, RateLimiterStrategy.perSecond(2_000), TypeInformation.of(Order.class)),
                WatermarkStrategy.<Order>forBoundedOutOfOrderness(Duration.ofSeconds(2))
                        .withTimestampAssigner((o, ts) -> o.eventTime),
                "orders");

        DataStream<Payment> payments = env.fromSource(
                new DataGeneratorSource<>(
                        (GeneratorFunction<Long, Payment>) EnrichAndCorrelate::payment,
                        ORDERS, RateLimiterStrategy.perSecond(2_000), TypeInformation.of(Payment.class)),
                WatermarkStrategy.<Payment>forBoundedOutOfOrderness(Duration.ofSeconds(2))
                        .withTimestampAssigner((p, ts) -> p.eventTime),
                "payments");

        SingleOutputStreamOperator<String> matched = orders
                .connect(payments)
                .keyBy(o -> o.orderId, p -> p.orderId)
                .process(new Matcher())
                .name("order-payment-matcher");

        matched.print("MATCH");
        matched.getSideOutput(UNMATCHED).print("UNMATCHED");

        env.execute("enrich-and-correlate (DataStream connect + CoProcess)");
    }

    /** Заказ orderId в [0..ORDERS). */
    static Order order(long i) {
        return new Order(i, T0 + i * STEP_MS, 100 + i % 50);
    }

    /**
     * Платёж по заказу. Каждый 9-й заказ платежа НЕ получает (несматченный по таймауту),
     * остальные приходят с задержкой 1–3 шага (иногда позже, иногда раньше порядка обработки).
     */
    static Payment payment(long i) {
        long orderId = (i % 9 == 0) ? ORDERS + i : i;      // «промах» — платёж за несуществующий заказ
        long delay = ((i % 3) + 1) * STEP_MS;
        // Платёж #50 приходит на 100с ПОЗЖЕ заказа — вне окна [0..8с]: демонстрирует, что
        // matchOrReject отвергает его (в side output), а не «матчит» вопреки таймауту.
        if (i == 50) {
            return new Payment(orderId, T0 + i * STEP_MS + 100_000);
        }
        return new Payment(orderId, T0 + i * STEP_MS + delay);
    }

    /** Матчер: держит заказ ИЛИ платёж в состоянии, пока не придёт пара; таймаут -> side output. */
    static final class Matcher extends KeyedCoProcessFunction<Long, Order, Payment, String> {
        private transient ValueState<Order> pendingOrder;
        private transient ValueState<Payment> pendingPayment;
        private transient ValueState<Long> timerTs;

        @Override
        public void open(Configuration cfg) {
            pendingOrder = getRuntimeContext().getState(
                    new ValueStateDescriptor<>("order", TypeInformation.of(Order.class)));
            pendingPayment = getRuntimeContext().getState(
                    new ValueStateDescriptor<>("payment", TypeInformation.of(Payment.class)));
            timerTs = getRuntimeContext().getState(
                    new ValueStateDescriptor<>("timerTs", TypeInformation.of(Long.class)));
        }

        @Override
        public void processElement1(Order o, Context ctx, Collector<String> out) throws Exception {
            Payment p = pendingPayment.value();
            if (p != null) {
                matchOrReject(o, p, ctx, out);
                clear(ctx);
            } else {
                pendingOrder.update(o);
                arm(ctx, o.eventTime + MATCH_TIMEOUT_MS);
            }
        }

        @Override
        public void processElement2(Payment p, Context ctx, Collector<String> out) throws Exception {
            Order o = pendingOrder.value();
            if (o != null) {
                matchOrReject(o, p, ctx, out);
                clear(ctx);
            } else {
                pendingPayment.update(p);
                arm(ctx, p.eventTime + MATCH_TIMEOUT_MS);
            }
        }

        @Override
        public void onTimer(long ts, OnTimerContext ctx, Collector<String> out) throws Exception {
            Long armed = timerTs.value();
            if (armed == null || armed != ts) {
                return; // устаревший таймер
            }
            Order o = pendingOrder.value();
            Payment p = pendingPayment.value();
            if (o != null) {
                ctx.output(UNMATCHED, "order orderId=" + o.orderId + " без платежа за " + MATCH_TIMEOUT_MS + "ms");
            } else if (p != null) {
                ctx.output(UNMATCHED, "payment orderId=" + p.orderId + " без заказа за " + MATCH_TIMEOUT_MS + "ms");
            }
            clearState();
        }

        /**
         * Матч действителен, только если платёж попал в окно [order, order + timeout] по EVENT-time.
         * Таймер ограничивает лишь удержание state; сам матч интервал НЕ проверял бы — поэтому
         * платёж с временем order+100s, пришедший до срабатывания таймера, без этой проверки
         * соединился бы, нарушив заявленный таймаут. Вне окна — в side output.
         */
        private void matchOrReject(Order o, Payment p, Context ctx, Collector<String> out) {
            long delta = p.eventTime - o.eventTime;   // обогащение: вычисляемое поле пары
            if (delta >= 0 && delta <= MATCH_TIMEOUT_MS) {
                out.collect("MATCH orderId=" + o.orderId + " amount=" + o.amount + " latencyMs=" + delta);
            } else {
                ctx.output(UNMATCHED, "out-of-window orderId=" + o.orderId
                        + " deltaMs=" + delta + " (окно [0.." + MATCH_TIMEOUT_MS + "]мс)");
            }
        }

        private void arm(Context ctx, long ts) throws Exception {
            Long prev = timerTs.value();
            if (prev != null) {
                ctx.timerService().deleteEventTimeTimer(prev);
            }
            ctx.timerService().registerEventTimeTimer(ts);
            timerTs.update(ts);
        }

        private void clear(Context ctx) throws Exception {
            Long prev = timerTs.value();
            if (prev != null) {
                ctx.timerService().deleteEventTimeTimer(prev);
            }
            clearState();
        }

        private void clearState() {
            pendingOrder.clear();
            pendingPayment.clear();
            timerTs.clear();
        }
    }

    /** Заказ. Публичный POJO с пустым конструктором — для сериализатора Flink. */
    public static final class Order {
        public long orderId;
        public long eventTime;
        public long amount;

        public Order() {}

        public Order(long orderId, long eventTime, long amount) {
            this.orderId = orderId;
            this.eventTime = eventTime;
            this.amount = amount;
        }
    }

    /** Платёж. */
    public static final class Payment {
        public long orderId;
        public long eventTime;

        public Payment() {}

        public Payment(long orderId, long eventTime) {
            this.orderId = orderId;
            this.eventTime = eventTime;
        }
    }
}
