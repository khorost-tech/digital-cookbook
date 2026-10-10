package tech.khorost.temporal;

// Профиль 07-languages, часть Java. Тот же сценарий, что в остальных
// четырёх SDK: activity -> таймер -> ожидание сигнала с таймаутом ->
// компенсация при отказе.
//
// Детерминизм в Java-SDK достигается собственным планировщиком поверх
// потоков: Workflow.sleep и Workflow.await НЕ являются Thread.sleep и
// не блокируют настоящий поток — их проигрывает рантайм SDK.

import io.temporal.activity.ActivityInterface;
import io.temporal.activity.ActivityMethod;
import io.temporal.activity.ActivityOptions;
import io.temporal.client.WorkflowClient;
import io.temporal.client.WorkflowOptions;
import io.temporal.client.WorkflowStub;
import io.temporal.serviceclient.WorkflowServiceStubs;
import io.temporal.serviceclient.WorkflowServiceStubsOptions;
import io.temporal.worker.Worker;
import io.temporal.worker.WorkerFactory;
import io.temporal.workflow.SignalMethod;
import io.temporal.workflow.Workflow;
import io.temporal.workflow.WorkflowInterface;
import io.temporal.workflow.WorkflowMethod;

import java.time.Duration;

public class Main {

    static final String SIGNAL = "confirm";

    @ActivityInterface
    public interface Acts {
        @ActivityMethod
        String reserve(String resource);

        @ActivityMethod
        String allocate(String reservationId);

        @ActivityMethod
        String cancelReservation(String reservationId);
    }

    public static class ActsImpl implements Acts {
        @Override
        public String reserve(String resource) {
            System.out.println(">>> ACTIVITY reserve(" + resource + ")");
            return "res-" + resource + "-001";
        }

        @Override
        public String allocate(String reservationId) {
            System.out.println(">>> ACTIVITY allocate(" + reservationId + ")");
            return "выделено " + reservationId;
        }

        @Override
        public String cancelReservation(String reservationId) {
            System.out.println(">>> ACTIVITY cancelReservation(" + reservationId + ")");
            return "снято " + reservationId;
        }
    }

    @WorkflowInterface
    public interface CrossLangWorkflow {
        @WorkflowMethod
        String run(String resource);

        @SignalMethod(name = SIGNAL)
        void confirm(boolean approved);
    }

    public static class CrossLangWorkflowImpl implements CrossLangWorkflow {
        private Boolean approved = null;

        private final Acts acts = Workflow.newActivityStub(
                Acts.class,
                ActivityOptions.newBuilder()
                        .setStartToCloseTimeout(Duration.ofSeconds(30))
                        .build());

        @Override
        public String run(String resource) {
            String rid = acts.reserve(resource);

            // Таймер: проигрывается рантаймом, а не спит в потоке.
            Workflow.sleep(Duration.ofSeconds(2));

            // Ожидание сигнала с таймаутом. Возвращает false, если
            // условие так и не стало истинным за отведённое время.
            boolean got = Workflow.await(Duration.ofSeconds(60), () -> approved != null);

            if (got && Boolean.TRUE.equals(approved)) {
                acts.allocate(rid);
                return "allocated";
            }
            acts.cancelReservation(rid);
            return "cancelled";
        }

        @Override
        public void confirm(boolean value) {
            this.approved = value;
        }
    }

    public static void main(String[] args) throws Exception {
        String address = envOr("TEMPORAL_ADDRESS", "temporal-frontend:7233");
        String queue = envOr("TASK_QUEUE", "lang-java-tq");
        String sdk = versionOf();

        WorkflowServiceStubs service = WorkflowServiceStubs.newServiceStubs(
                WorkflowServiceStubsOptions.newBuilder().setTarget(address).build());
        WorkflowClient client = WorkflowClient.newInstance(service);

        boolean runMode = args.length > 0 && "run".equals(args[0]);
        boolean approve = false;
        for (String a : args) {
            if ("--approve".equals(a)) {
                approve = true;
            }
        }

        if (!runMode) {
            WorkerFactory factory = WorkerFactory.newInstance(client);
            Worker worker = factory.newWorker(queue);
            worker.registerWorkflowImplementationTypes(CrossLangWorkflowImpl.class);
            worker.registerActivitiesImplementations(new ActsImpl());
            System.out.println("[worker] lang=java sdk=" + sdk + " queue=" + queue);
            factory.start();
            Thread.currentThread().join();
            return;
        }

        CrossLangWorkflow wf = client.newWorkflowStub(
                CrossLangWorkflow.class,
                WorkflowOptions.newBuilder()
                        .setWorkflowId("lang-java")
                        .setTaskQueue(queue)
                        .build());

        WorkflowClient.start(wf::run, "gpu-node-7");
        Thread.sleep(4000);
        wf.confirm(approve);

        String outcome = WorkflowStub.fromTyped(wf).getResult(String.class);
        System.out.println("ЯЗЫК lang=java sdk=" + sdk + " queue=" + queue + " outcome=" + outcome);
        System.exit(0);
    }

    private static String envOr(String key, String fallback) {
        String v = System.getenv(key);
        return (v == null || v.isEmpty()) ? fallback : v;
    }

    // Версия SDK. Сначала пробуем манифест пакета — это самый честный
    // источник, «что реально загружено». В толстом jar его нет: shade
    // объединяет классы в один артефакт, и Implementation-Version
    // зависимости теряется. Тогда берём значение из ресурса, который
    // Maven заполнил из свойства pom при сборке ЭТОГО образа.
    private static String versionOf() {
        Package p = WorkflowClient.class.getPackage();
        String v = (p == null) ? null : p.getImplementationVersion();
        if (v != null && !v.isEmpty()) {
            return v;
        }
        try (java.io.InputStream in = Main.class.getResourceAsStream("/sdk.properties")) {
            if (in != null) {
                java.util.Properties props = new java.util.Properties();
                props.load(in);
                String fromProps = props.getProperty("temporal.sdk.version");
                if (fromProps != null && !fromProps.isEmpty()) {
                    return fromProps;
                }
            }
        } catch (java.io.IOException ignored) {
            // падать из-за версии в логе — хуже, чем напечатать «неизвестна»
        }
        return "неизвестна";
    }
}
