// Java-стенд к статье «Что кладём в событие: notification vs event-carried state
// transfer» на khorost.tech
// (/architecture/event-notification-vs-state-transfer/).
//
// Полный аналог Go-стенда (event-payload/go/main.go): один и тот же поток
// «заказ оплачен» проигрывается в двух формах события — тонком (notification)
// и толстом (event-carried state transfer) — и сравнивается по трём измеримым осям:
//
//   - обращения к источнику: сколько раз консьюмеры дёрнули source.get(orderId);
//   - размер payload: байты сериализованного (JSON) события в каждом варианте;
//   - свежесть данных: видит ли консьюмер изменение, внесённое в источник ПОСЛЕ
//     публикации события (staleness толстого события против «GET-storm» тонкого).
//
// Всё in-process, без брокеров и docker: источник — мапа под замком со
// счётчиком обращений, «шина» — сериализованные байты события, консьюмеры —
// обычные методы. JSON сериализуется вручную (без внешних зависимостей),
// чтобы байты совпали с Go-вариантом.
//
// Запуск:
//
//   cd event-payload/java
//   mvn -q compile exec:java
//
// Программа сама проверяет инварианты (ненулевой exit при расхождении) и печатает
// итоговую таблицу метрик.
package tech.khorost.eventpayload;

import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.HashMap;
import java.util.stream.Collectors;

public final class Main {

    // NUM_CONSUMERS — сколько независимых потребителей реагирует на каждое событие.
    // Именно этот множитель превращает тонкое событие в «GET-storm»: на N консьюмеров
    // приходится N обращений к источнику на КАЖДОЕ событие.
    static final int NUM_CONSUMERS = 4;

    // -------------------------------------------------------------------------
    // Доменная сущность
    // -------------------------------------------------------------------------

    // Order — доменная сущность «заказ». В толстое событие кладётся её полная копия,
    // в тонкое — только number (идентификатор).
    record Order(String number, String status, long amount, String currency, List<String> items) {
        Order withStatus(String newStatus) {
            return new Order(number, newStatus, amount, currency, items);
        }
    }

    // -------------------------------------------------------------------------
    // Источник истины (owning service)
    // -------------------------------------------------------------------------

    // Source — сервис-владелец заказов. Хранит состояние в мапе под замком и
    // считает, сколько раз к нему обратились за деталями. Флаг available моделирует
    // временную недоступность источника (сеть/деплой/перегрузка).
    static final class Source {
        private final Object lock = new Object();
        private final Map<String, Order> orders = new HashMap<>();
        private int getCount;          // сколько раз вызвали get — метрика «обращений к источнику»
        private boolean available = true; // false → источник недоступен, get бросит исключение

        // put — источник кладёт или обновляет заказ у себя. Обращением «наружу»
        // не считается (это внутренняя мутация владельца).
        void put(Order o) {
            synchronized (lock) {
                orders.put(o.number(), o);
            }
        }

        // get — единственный способ для консьюмера дотянуться до деталей заказа.
        // Каждый вызов инкрементит getCount. Если источник недоступен — бросает
        // исключение (демонстрация ВРЕМЕННОЙ связанности тонкого события с источником).
        Order get(String orderId) {
            synchronized (lock) {
                getCount++;
                if (!available) {
                    throw new IllegalStateException("source unavailable: cannot fetch " + orderId);
                }
                Order o = orders.get(orderId);
                if (o == null) {
                    throw new IllegalStateException("order " + orderId + " not found");
                }
                return o;
            }
        }

        int getCount() {
            synchronized (lock) {
                return getCount;
            }
        }

        void resetCount() {
            synchronized (lock) {
                getCount = 0;
            }
        }

        void setAvailable(boolean v) {
            synchronized (lock) {
                available = v;
            }
        }
    }

    // -------------------------------------------------------------------------
    // Формы события
    // -------------------------------------------------------------------------

    // NotificationEvent — тонкое событие: несёт только идентификатор. Консьюмер
    // ОБЯЗАН сходить в источник за деталями.
    record NotificationEvent(String type, String orderId) {}

    // StateTransferEvent — толстое событие (event-carried state transfer): несёт
    // полный снимок состояния заказа. Консьюмер автономен, но снимок может устареть.
    record StateTransferEvent(String type, Order order) {}

    // -------------------------------------------------------------------------
    // Результат прогона одного варианта
    // -------------------------------------------------------------------------

    record Result(
            String variant,
            int sourceHits,   // суммарно обращений к источнику при обработке события
            int payloadSize,  // байт сериализованного события
            String seenStatus, // какой статус заказа «увидели» консьюмеры
            boolean fresh      // отражает ли увиденный статус свежее изменение источника
    ) {}

    // -------------------------------------------------------------------------
    // Ручная JSON-сериализация (совпадает с Go byte-в-byte)
    // -------------------------------------------------------------------------

    // Домен простой (ASCII-значения, без спецсимволов), поэтому достаточно
    // минимального сериализатора: экранируем только то, что реально встречается.
    static String jsonString(String s) {
        StringBuilder sb = new StringBuilder("\"");
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"' -> sb.append("\\\"");
                case '\\' -> sb.append("\\\\");
                default -> sb.append(c);
            }
        }
        return sb.append('"').toString();
    }

    static String marshalOrder(Order o) {
        String items = o.items().stream()
                .map(Main::jsonString)
                .collect(Collectors.joining(","));
        return "{"
                + "\"orderId\":" + jsonString(o.number()) + ","
                + "\"status\":" + jsonString(o.status()) + ","
                + "\"amount\":" + o.amount() + ","
                + "\"currency\":" + jsonString(o.currency()) + ","
                + "\"items\":[" + items + "]"
                + "}";
    }

    static String marshalNotification(NotificationEvent e) {
        return "{"
                + "\"type\":" + jsonString(e.type()) + ","
                + "\"orderId\":" + jsonString(e.orderId())
                + "}";
    }

    static String marshalStateTransfer(StateTransferEvent e) {
        return "{"
                + "\"type\":" + jsonString(e.type()) + ","
                + "\"order\":" + marshalOrder(e.order())
                + "}";
    }

    static byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    // -------------------------------------------------------------------------
    // Сценарий
    // -------------------------------------------------------------------------

    public static void main(String[] args) {
        final String orderId = "ORD-42";

        // Исходное состояние заказа на момент оплаты.
        Order initial = new Order(
                orderId,
                "PAID",
                1_599_900L, // 15 999.00
                "RUB",
                List.of("keyboard-noon-edition", "wrist-rest", "usb-c-cable"));

        System.out.println("Стенд: notification vs event-carried state transfer");
        System.out.printf(
                "Поток «заказ оплачен», %d консьюмера(ов) на событие, заказ %s (status=%s)%n%n",
                NUM_CONSUMERS, orderId, initial.status());

        Result notif = runNotification(initial);
        Result state = runStateTransfer(initial);

        printTable(notif, state);
        printPayloads(orderId, initial);
        verify(notif, state);

        System.out.println("\nOK: все инварианты выполнены.");
    }

    // runNotification проигрывает тонкое событие.
    //
    // Ключевой момент staleness-теста: источник МЕНЯЕТ заказ уже ПОСЛЕ публикации
    // события (PAID → REFUNDED). Тонкий консьюмер читает детали лениво, в момент
    // обработки, — и поэтому видит уже свежий статус. Плата — NUM_CONSUMERS обращений
    // к источнику на одно событие (GET-storm).
    static Result runNotification(Order initial) {
        Source src = new Source();
        src.put(initial);

        // Публикуем тонкое событие.
        NotificationEvent ev = new NotificationEvent("order.paid", initial.number());
        byte[] payload = bytes(marshalNotification(ev));

        // Источник обновляет заказ ПОСЛЕ публикации события.
        Order updated = initial.withStatus("REFUNDED");
        src.put(updated);

        // N консьюмеров обрабатывают одно и то же событие: каждый идёт в источник.
        src.resetCount();
        String seen = null;
        for (int i = 0; i < NUM_CONSUMERS; i++) {
            NotificationEvent got = parseNotification(payload); // «десериализация» из шины
            Order o = src.get(got.orderId()); // ← обращение к источнику
            seen = o.status();
        }
        int hits = src.getCount(); // фиксируем GET-storm ДО демонстрации недоступности

        // Демонстрация временной связанности: если источник недоступен —
        // тонкий консьюмер не может обработать событие вообще.
        src.setAvailable(false);
        try {
            src.get(initial.number());
            fail("notification: ожидалась ошибка недоступного источника");
        } catch (IllegalStateException e) {
            System.out.printf(
                    "[notification] источник недоступен → консьюмер не может обработать событие: %s%n",
                    e.getMessage());
        }
        src.setAvailable(true);

        return new Result(
                "notification",
                hits,
                payload.length,
                seen,
                seen != null && seen.equals(updated.status())); // увидели свежий REFUNDED?
    }

    // runStateTransfer проигрывает толстое событие.
    //
    // Событие несёт снимок состояния на момент публикации (PAID). Источник затем
    // меняет заказ (PAID → REFUNDED), но консьюмеры работают по снимку внутри события
    // и в источник не ходят вовсе — 0 обращений. Плата — staleness: свежий REFUNDED
    // они не видят.
    static Result runStateTransfer(Order initial) {
        Source src = new Source();
        src.put(initial);

        // Публикуем толстое событие — снимок ПОЛНОГО состояния на текущий момент.
        StateTransferEvent ev = new StateTransferEvent("order.paid", initial);
        byte[] payload = bytes(marshalStateTransfer(ev));

        // Источник обновляет заказ ПОСЛЕ публикации события — как и в тонком варианте.
        Order updated = initial.withStatus("REFUNDED");
        src.put(updated);

        // N консьюмеров обрабатывают событие автономно: читают состояние из payload,
        // в источник не обращаются.
        src.resetCount();
        String seen = null;
        for (int i = 0; i < NUM_CONSUMERS; i++) {
            StateTransferEvent got = parseStateTransfer(payload); // «десериализация» из шины
            seen = got.order().status(); // ← данные из события, без похода в источник
        }

        // Контроль: показываем, что источник УЖЕ содержит свежий статус,
        // которого толстый консьюмер не увидел.
        Order fromSource = src.get(initial.number());
        System.out.printf(
                "[state-transfer] источник уже содержит status=%s, но консьюмер увидел status=%s (staleness)%n",
                fromSource.status(), seen);
        // Один get выше — контрольный, для наглядности лога; в метрику автономности
        // консьюмеров он не входит (они не обращались к источнику вообще).
        int hits = src.getCount() - 1;

        return new Result(
                "state-transfer",
                hits,
                payload.length,
                seen,
                seen != null && seen.equals(updated.status())); // false: увидели старый PAID
    }

    // -------------------------------------------------------------------------
    // «Десериализация» из шины
    // -------------------------------------------------------------------------
    //
    // Событие по шине передаётся как сериализованные байты — консьюмер получает
    // именно их. Домен фиксирован, поэтому «парсинг» тривиален: восстанавливаем
    // объект из известной формы (сам факт передачи байтов и есть модель шины).

    static NotificationEvent parseNotification(byte[] payload) {
        // Форма зафиксирована стендом; байты — то, что реально ушло в «шину».
        String json = new String(payload, StandardCharsets.UTF_8);
        return new NotificationEvent(
                extract(json, "type"),
                extract(json, "orderId"));
    }

    static StateTransferEvent parseStateTransfer(byte[] payload) {
        String json = new String(payload, StandardCharsets.UTF_8);
        // Вложенный order восстанавливаем по известным полям снимка.
        Order order = new Order(
                extract(json, "orderId"),
                extract(json, "status"),
                Long.parseLong(extractRaw(json, "amount")),
                extract(json, "currency"),
                extractStringArray(json, "items"));
        return new StateTransferEvent(extract(json, "type"), order);
    }

    // extract — вытаскивает строковое значение поля "key":"value" (домен без
    // экранированных кавычек внутри значений, чего достаточно для стенда).
    static String extract(String json, String key) {
        String needle = "\"" + key + "\":\"";
        int start = json.indexOf(needle);
        if (start < 0) {
            throw new IllegalStateException("поле не найдено: " + key);
        }
        start += needle.length();
        int end = json.indexOf('"', start);
        return json.substring(start, end);
    }

    // extractRaw — вытаскивает числовое значение поля "key":value.
    static String extractRaw(String json, String key) {
        String needle = "\"" + key + "\":";
        int start = json.indexOf(needle) + needle.length();
        int end = start;
        while (end < json.length() && (Character.isDigit(json.charAt(end)) || json.charAt(end) == '-')) {
            end++;
        }
        return json.substring(start, end);
    }

    // extractStringArray — вытаскивает массив строк "key":["a","b",...].
    static List<String> extractStringArray(String json, String key) {
        String needle = "\"" + key + "\":[";
        int start = json.indexOf(needle) + needle.length();
        int end = json.indexOf(']', start);
        String body = json.substring(start, end).trim();
        if (body.isEmpty()) {
            return List.of();
        }
        return java.util.Arrays.stream(body.split(","))
                .map(String::trim)
                .map(t -> t.substring(1, t.length() - 1)) // снять кавычки
                .collect(Collectors.toList());
    }

    // -------------------------------------------------------------------------
    // Вывод и проверки
    // -------------------------------------------------------------------------

    static String hr() {
        return "─".repeat(74);
    }

    static void printTable(Result... rs) {
        System.out.println("\n" + hr());
        System.out.printf("%-16s | %-20s | %-14s | %-14s%n",
                "вариант", "обращений к источн.", "payload, байт", "свежие данные");
        System.out.println(hr());
        for (Result r : rs) {
            String fresh = r.fresh() ? "да" : "нет (staleness)";
            System.out.printf("%-16s | %-20d | %-14d | %-14s%n",
                    r.variant(), r.sourceHits(), r.payloadSize(), fresh);
        }
        System.out.println(hr());
    }

    // printPayloads печатает фактические сериализованные события, чтобы разница в
    // размере была не абстрактным числом, а видимой глазами.
    static void printPayloads(String orderId, Order o) {
        String n = marshalNotification(new NotificationEvent("order.paid", orderId));
        String s = marshalStateTransfer(new StateTransferEvent("order.paid", o));
        int nLen = bytes(n).length;
        int sLen = bytes(s).length;
        System.out.printf("%nnotification  payload (%d байт): %s%n", nLen, n);
        System.out.printf("state-transfer payload (%d байт): %s%n", sLen, s);
        if (sLen > nLen) {
            System.out.printf("толстое событие тяжелее в %.1f× (+%d байт)%n",
                    (double) sLen / (double) nLen, sLen - nLen);
        }
    }

    // verify проверяет инварианты статьи. Расхождение — ненулевой exit-код.
    static void verify(Result notif, Result state) {
        // Тонкое событие: обращения к источнику > 0 и данные свежие.
        if (notif.sourceHits() <= 0) {
            fail("инвариант нарушен: notification должно обращаться к источнику (>0), получили " + notif.sourceHits());
        }
        if (notif.sourceHits() != NUM_CONSUMERS) {
            fail("инвариант нарушен: ожидали " + NUM_CONSUMERS + " обращений (GET-storm), получили " + notif.sourceHits());
        }
        if (!notif.fresh()) {
            fail("инвариант нарушен: notification должно видеть свежие данные");
        }
        // Толстое событие: 0 обращений, но staleness.
        if (state.sourceHits() != 0) {
            fail("инвариант нарушен: state-transfer должно быть автономным (0 обращений), получили " + state.sourceHits());
        }
        if (state.fresh()) {
            fail("инвариант нарушен: state-transfer должно демонстрировать staleness (устаревший снимок)");
        }
        // Размер: толстое событие крупнее тонкого.
        if (state.payloadSize() <= notif.payloadSize()) {
            fail("инвариант нарушен: state-transfer payload (" + state.payloadSize()
                    + ") должен быть больше notification (" + notif.payloadSize() + ")");
        }
    }

    // fail — печатает причину в stderr и завершает процесс с ненулевым кодом
    // (аналог log.Fatalf в Go-стенде).
    static void fail(String msg) {
        System.err.println(msg);
        System.exit(1);
    }

    private Main() {
    }
}
