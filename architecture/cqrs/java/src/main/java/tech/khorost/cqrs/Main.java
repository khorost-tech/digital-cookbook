package tech.khorost.cqrs;

import java.sql.Connection;
import java.sql.DriverManager;
import java.util.List;
import java.util.stream.Collectors;

/**
 * Стенд к статье «CQRS на практике» — https://khorost.tech/architecture/cqrs-in-practice/
 * (Java-порт стенда cqrs/go на чистом JDBC; тот же сценарий и те же ассерты).
 * <p>
 * Что показывает (домен — заказы):
 * <ul>
 *   <li>write-модель — append-only лог событий заказов (order_events); команды только
 *       добавляют события, текущее состояние получается сверткой лога;</li>
 *   <li>async-проекция — отдельный проектор строит денормализованную read-таблицу
 *       orders_read (заточена под «список заказов пользователя»), применяет лог идемпотентно
 *       (UPSERT) и двигает чекпоинт позиции АТОМАРНО с проекцией;</li>
 *   <li>read-your-writes — сначала ПОЛОМКА (сразу после записи читаем из отстающей проекции),
 *       затем ДВА приёма обхода: чтение своей свежей записи с write-стороны и ожидание
 *       проекции по токену-позиции;</li>
 *   <li>лаг проекции как метрика — хвост лога минус чекпоинт проектора;</li>
 *   <li>blue-green rebuild — пересобираем orders_read_v2 полным реплеем без остановки чтения
 *       и атомарно переключаем таблицы местами.</li>
 * </ul>
 * Инварианты закреплены ассертами (ненулевой exit при расхождении): проекция после догона ==
 * свёртка write-стороны; приём read-your-writes возвращает свежие данные; проекция после
 * rebuild == онлайн-состоянию.
 * <p>
 * Запуск: docker compose -p cqrs -f cqrs/compose/compose.yml up -d; затем в cqrs/java:
 * mvn -q compile exec:java. Строка подключения — переменная DATABASE_URL (JDBC-URL), по
 * умолчанию jdbc:postgresql://localhost:5453/cqrs; креды — PGUSER/PGPASSWORD (по умолчанию cqrs).
 */
public final class Main {

    public static void main(String[] args) throws Exception {
        String url = System.getenv().getOrDefault("DATABASE_URL", "jdbc:postgresql://localhost:5453/cqrs");
        String user = System.getenv().getOrDefault("PGUSER", "cqrs");
        String pass = System.getenv().getOrDefault("PGPASSWORD", "cqrs");

        try (Connection db = DriverManager.getConnection(url, user, pass)) {
            db.setAutoCommit(true);

            WriteModel write = new WriteModel(db);
            Projector proj = new Projector(db);
            Rebuild rebuild = new Rebuild(db, proj);

            write.setup();
            proj.setup();

            // --- Затравка: у Bob два заказа, у Alice один; проектор их уже применил ---
            System.out.println("=== (0) затравка: пишем команды и один раз прогоняем проектор ===");
            long bob1 = write.createOrder("bob", 100).orderId();
            write.createOrder("bob", 250);
            write.payOrder(bob1);
            write.createOrder("alice", 300);

            int n = proj.projectOnce();
            long lag = proj.projectionLag(write);
            System.out.printf("применено событий: %d, лаг проекции: %d%n", n, lag);
            assertEq("лаг после догона затравки", lag, 0);

            // --- (1) read-your-writes: ПОЛОМКА ---
            // Alice делает новый заказ и СРАЗУ читает свой список из проекции. Проектор ещё не
            // вызывали → проекция отстаёт → своей только что созданной записи Alice не видит.
            System.out.println("\n=== (1) read-your-writes: поломка (читаем из отстающей проекции) ===");
            WriteModel.CreateResult aliceNew = write.createOrder("alice", 999);
            long token = aliceNew.seq();
            lag = proj.projectionLag(write);
            System.out.printf("создан заказ #%d (token seq=%d), лаг проекции теперь: %d%n",
                    aliceNew.orderId(), token, lag);

            List<Order> fromProj = proj.readUserOrdersFromProjection("alice");
            System.out.printf("проекция вернула Alice %d заказ(ов): %s%n", fromProj.size(), orderIDs(fromProj));
            if (containsOrder(fromProj, aliceNew.orderId())) {
                fail("АНСЕРТ: ожидали поломку read-your-writes, но проекция уже содержит #" + aliceNew.orderId());
            }
            System.out.printf("read-your-writes НАРУШЕН: заказа #%d в проекции нет (лаг=%d)%n",
                    aliceNew.orderId(), lag);

            // --- (2) read-your-writes: ПРИЁМ №1 — собственную свежую запись читаем с write-стороны ---
            System.out.println("\n=== (2) приём №1: свои свежие заказы читаем с write-стороны (свёртка лога) ===");
            List<Order> fromWrite = write.foldUserOrdersWriteSide("alice");
            System.out.printf("write-сторона вернула Alice %d заказ(ов): %s%n", fromWrite.size(), orderIDs(fromWrite));
            if (!containsOrder(fromWrite, aliceNew.orderId())) {
                fail("АНСЕРТ: приём read-your-writes должен видеть свежую запись #" + aliceNew.orderId() + ", но её нет");
            }
            System.out.printf("read-your-writes ВОССТАНОВЛЕН: заказ #%d виден сразу (write-сторона без лага)%n",
                    aliceNew.orderId());

            // «Чужие» заказы при этом спокойно читаются из проекции (быстро, денормализовано).
            List<Order> bobProj = proj.readUserOrdersFromProjection("bob");
            System.out.printf("заказы Bob (чужие для Alice) — из проекции: %s%n", orderIDs(bobProj));

            // --- (3) read-your-writes: ПРИЁМ №2 — ждём проекцию по токену-позиции ---
            System.out.println("\n=== (3) приём №2: ждём, пока проекция догонит токен, и читаем из неё ===");
            proj.waitForProjection(token);
            lag = proj.projectionLag(write);
            List<Order> afterWait = proj.readUserOrdersFromProjection("alice");
            System.out.printf("после догона (лаг=%d) проекция вернула Alice: %s%n", lag, orderIDs(afterWait));
            if (!containsOrder(afterWait, aliceNew.orderId())) {
                fail("АНСЕРТ: после ожидания по токену проекция обязана содержать #" + aliceNew.orderId());
            }

            // --- (4) проекция после догона == свёртке write-стороны ---
            System.out.println("\n=== (4) инвариант: догнавшая проекция == свёртка write-стороны ===");
            assertOrdersEqual("проекция(alice) == свёртка(alice)",
                    proj.readUserOrdersFromProjection("alice"), write.foldUserOrdersWriteSide("alice"));
            assertOrdersEqual("проекция(bob) == свёртка(bob)",
                    proj.readUserOrdersFromProjection("bob"), write.foldUserOrdersWriteSide("bob"));
            System.out.println("совпадает: проекция догнала лог и равна авторитетной свёртке");

            // --- (5) blue-green rebuild ---
            // Снимок «онлайн»-состояния до пересборки. Пересобираем orders_read_v2 полным
            // реплеем и атомарно переключаем. Новых команд между снимком и switch нет, поэтому
            // после переключения проекция обязана совпасть и со свёрткой, и со снимком «до».
            System.out.println("\n=== (5) blue-green rebuild: реплей в orders_read_v2 + атомарный switch ===");
            List<Order> onlineAliceBefore = proj.readUserOrdersFromProjection("alice");
            int replayed = rebuild.rebuildReadModel();
            lag = proj.projectionLag(write);
            System.out.printf("реплейнуто событий в v2: %d, лаг после switch: %d%n", replayed, lag);
            assertEq("лаг после rebuild", lag, 0);

            List<Order> rebuiltAlice = proj.readUserOrdersFromProjection("alice");
            assertOrdersEqual("проекция после rebuild == онлайн до rebuild", rebuiltAlice, onlineAliceBefore);
            assertOrdersEqual("проекция после rebuild == свёртка write-стороны",
                    rebuiltAlice, write.foldUserOrdersWriteSide("alice"));
            System.out.println("совпадает: пересобранная проекция идентична онлайн-состоянию и свёртке");

            System.out.println("\nВСЕ АНСЕРТЫ ПРОЙДЕНЫ ✔");
        }
    }

    // --- служебное ---

    private static void assertEq(String label, long got, long want) {
        if (got != want) {
            fail("АНСЕРТ [" + label + "]: получили " + got + ", ожидали " + want);
        }
    }

    /**
     * Сравнивает списки заказов по бизнес-полям (order_id/user_id/status/amount). updated_seq
     * не сравниваем: это техническая позиция применения.
     */
    private static void assertOrdersEqual(String label, List<Order> got, List<Order> want) {
        if (got.size() != want.size()) {
            fail("АНСЕРТ [" + label + "]: разное число заказов: " + got.size() + " vs " + want.size()
                    + "\n  got=" + orderIDs(got) + "\n  want=" + orderIDs(want));
        }
        for (int i = 0; i < got.size(); i++) {
            Order g = got.get(i);
            Order w = want.get(i);
            if (g.orderId() != w.orderId() || !g.userId().equals(w.userId())
                    || !g.status().equals(w.status()) || g.amount() != w.amount()) {
                fail("АНСЕРТ [" + label + "]: расхождение в позиции " + i + ": " + g + " vs " + w);
            }
        }
    }

    private static boolean containsOrder(List<Order> orders, long id) {
        return orders.stream().anyMatch(o -> o.orderId() == id);
    }

    /** Форматирует список как Go-шный %v над []string: {@code [#1002(new,300) #1003(new,999)]}. */
    private static String orderIDs(List<Order> orders) {
        return orders.stream().map(Order::tag).collect(Collectors.joining(" ", "[", "]"));
    }

    private static void fail(String msg) {
        System.err.println(msg);
        System.exit(1);
    }

    private Main() {
    }
}
