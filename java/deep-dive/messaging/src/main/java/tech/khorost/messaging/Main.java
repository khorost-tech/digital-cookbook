package tech.khorost.messaging;

/**
 * Оркестратор стенда "Kafka: producer/consumer, consumer groups, exactly-once,
 * обработка ошибок" (статья №11, java-deep-dive). Режим выбирается первым
 * аргументом: all (по умолчанию) | basic | groups | eos | errors.
 * KAFKA_BOOTSTRAP — см. {@link Kafka} (по умолчанию kafka:9092, внутренний
 * листенер брокера на compose-сети).
 */
public class Main {
    public static void main(String[] args) throws Exception {
        String mode = args.length > 0 ? args[0] : "all";
        System.out.println("=== messaging: Kafka producer/consumer, consumer groups, EOS, error-handling ===");
        System.out.println("KAFKA_BOOTSTRAP=" + Kafka.BOOTSTRAP + ", mode=" + mode);
        System.out.println();

        if (mode.equals("all") || mode.equals("basic")) {
            System.out.println("--- (1) Producer/consumer: базовая доставка end-to-end ---");
            int n = 20;
            int received = ProducerConsumerDemo.run(n);
            if (received != n) {
                throw new IllegalStateException(
                        "Базовая доставка сломана: отправлено=" + n + ", получено=" + received);
            }
            System.out.printf("АССЕРТ OK: отправлено=%d, получено=%d%n", n, received);
            System.out.println();
        }

        if (mode.equals("all") || mode.equals("groups")) {
            System.out.println("--- (2) Consumer groups: 2 консьюмера, ребаланс партиций ---");
            int n = 40;
            ConsumerGroupDemo.Result r = ConsumerGroupDemo.run(n);
            if (r.assignment1.isEmpty() || r.assignment2.isEmpty()) {
                throw new IllegalStateException(
                        "Ребаланс не распределил партиции: consumer-1=" + r.assignment1 + ", consumer-2=" + r.assignment2);
            }
            boolean disjoint = r.assignment1.stream().noneMatch(r.assignment2::contains);
            if (!disjoint) {
                throw new IllegalStateException("Партиции пересекаются между консьюмерами одной группы: "
                        + r.assignment1 + " / " + r.assignment2);
            }
            int totalPartitions = r.assignment1.size() + r.assignment2.size();
            if (totalPartitions != ConsumerGroupDemo.PARTITIONS) {
                throw new IllegalStateException("Не все партиции распределены: " + totalPartitions
                        + " != " + ConsumerGroupDemo.PARTITIONS);
            }
            int totalConsumed = r.consumed1 + r.consumed2;
            if (totalConsumed < n) {
                throw new IllegalStateException("Не все сообщения дочитаны: consumed=" + totalConsumed + " < " + n);
            }
            System.out.printf("АССЕРТ OK: партиции распределены без пересечений (%d+%d=%d из %d), сообщений дочитано=%d (>=%d)%n",
                    r.assignment1.size(), r.assignment2.size(), totalPartitions, ConsumerGroupDemo.PARTITIONS,
                    totalConsumed, n);
            System.out.println();
        }

        if (mode.equals("all") || mode.equals("eos")) {
            System.out.println("--- (3) Exactly-once: транзакционный producer + read_committed consumer ---");
            EosDemo.Result r = EosDemo.run();
            if (r.consumedVisible != r.committedLogical) {
                throw new IllegalStateException("EOS сломан: read_committed увидел " + r.consumedVisible
                        + ", ожидалось " + r.committedLogical + " (физически отправлено " + r.sentPhysically + ")");
            }
            System.out.printf("АССЕРТ OK: физически отправлено=%d, логически подтверждено=%d, "
                            + "read_committed увидел=%d (абортнутый батч невидим, дублей нет)%n",
                    r.sentPhysically, r.committedLogical, r.consumedVisible);
            System.out.println();
        }

        if (mode.equals("all") || mode.equals("errors")) {
            System.out.println("--- (4) Обработка ошибок: retry + dead-letter topic ---");
            int n = 15;
            int poisonEvery = 5; // каждое 5-е сообщение — "ядовитое"
            ErrorHandlingDemo.Result r = ErrorHandlingDemo.run(n, poisonEvery);
            int expectedGood = r.totalSent - r.poisonSent;
            if (r.goodProcessed != expectedGood) {
                throw new IllegalStateException("Часть нормальных сообщений не обработана: "
                        + r.goodProcessed + " != " + expectedGood);
            }
            if (r.sentToDlt != r.poisonSent) {
                throw new IllegalStateException("В DLT попало не всё ядовитое: sentToDlt=" + r.sentToDlt
                        + " != poisonSent=" + r.poisonSent);
            }
            if (r.dltConsumed != r.sentToDlt) {
                throw new IllegalStateException("DLT-топик при перечитывании дал другое число: "
                        + r.dltConsumed + " != " + r.sentToDlt);
            }
            System.out.printf("АССЕРТ OK: успешно обработано=%d, в DLT после %d ретраев=%d (перепроверено=%d)%n",
                    r.goodProcessed, ErrorHandlingDemo.MAX_RETRIES, r.sentToDlt, r.dltConsumed);
            System.out.println();
        }

        System.out.println("=== готово ===");
    }
}
