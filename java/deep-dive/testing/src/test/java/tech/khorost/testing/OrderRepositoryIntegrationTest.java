package tech.khorost.testing;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.Optional;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.testcontainers.containers.PostgreSQLContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.utility.DockerImageName;

import tech.khorost.testing.Order.OrderStatus;

/**
 * Интеграционный уровень: {@link JdbcOrderRepository} гоняется против настоящего
 * Postgres, поднятого Testcontainers в отдельном sibling-контейнере через Docker
 * demon. Контраст с {@link OrderServiceUnitTest}: здесь секунды (старт контейнера
 * postgres:18.4), а не микросекунды — это видно в "Time elapsed" surefire для
 * этого класса против OrderServiceUnitTest.
 *
 * <p>ПОДВОДНЫЙ КАМЕНЬ Docker Desktop (Windows, WSL2-бэкенд) при DinD-топологии (сам
 * {@code mvn test} тоже гоняется внутри контейнера с проброшенным
 * {@code docker.sock}): опубликованный host-порт
 * Postgres-контейнера (тот, что видно в {@code POSTGRES.getJdbcUrl()} /
 * {@code getMappedPort()}) оказался НЕДОСТИЖИМ ни с bridge-шлюза
 * (172.17.0.1), ни с host.docker.internal — стабильно "Connection refused"
 * даже спустя 30+ секунд после старта контейнера, при этом тот же самый
 * bridge/dual-stack-порт, поднятый вручную через голый {@code docker run}
 * (в том числе из контейнера с тем же проброшенным сокетом), был достижим
 * СРАЗУ. Похоже на специфику Docker Desktop socket-прокси
 * (Labels: dockerSocketProxied) для контейнеров, порождённых через
 * docker-java/testcontainers из вложенного контейнера — публикация порта
 * на хосте не долетает до соседних контейнеров, хотя сам Postgres уже
 * принимает соединения. Обход (официально рекомендованный testcontainers
 * паттерн для "test runner в контейнере"): поднимать test-runner
 * (maven-контейнер) и Postgres-контейнер на ОДНОЙ общей user-defined
 * docker-сети и соединяться по внутреннему IP:5432 контейнера, а не через
 * опубликованный host-порт. Имя сети передаётся через env
 * TC_TESTCONTAINERS_NETWORK (её нужно создать заранее и подключить к ней
 * maven-контейнер флагом --network при запуске mvn test).
 * Без этой переменной (например, при обычном хостовом
 * mvn test не в DinD) используется штатный getJdbcUrl() через
 * опубликованный порт.
 */
@Testcontainers
class OrderRepositoryIntegrationTest {

    private static final Logger log = LoggerFactory.getLogger(OrderRepositoryIntegrationTest.class);

    /** Имя предсозданной docker-сети, на которой сидит и test-runner (maven-контейнер),
     *  и этот Postgres-контейнер — обход недостижимости опубликованного host-порта
     *  в DinD-топологии Docker Desktop (см. class javadoc). */
    private static final String SHARED_NETWORK = System.getenv("TC_TESTCONTAINERS_NETWORK");

    @Container
    static final PostgreSQLContainer<?> POSTGRES = createContainer();

    private static PostgreSQLContainer<?> createContainer() {
        PostgreSQLContainer<?> container = new PostgreSQLContainer<>(DockerImageName.parse("postgres:18.4"))
                .withDatabaseName("orders_test")
                .withUsername("orders")
                .withPassword("orders");
        if (SHARED_NETWORK != null && !SHARED_NETWORK.isBlank()) {
            container.withNetworkMode(SHARED_NETWORK);
        }
        return container;
    }

    @BeforeAll
    static void applySchema() throws Exception {
        String schema = Files.readString(Path.of("src/test/resources/schema.sql"));
        try (Connection connection = openConnection();
             Statement statement = connection.createStatement()) {
            statement.execute(schema);
        }
        log.info("Postgres container started: {} (network={}, container IP={})",
                jdbcUrl(), SHARED_NETWORK, containerIpOrNull());
    }

    private static String containerIpOrNull() {
        if (SHARED_NETWORK == null || SHARED_NETWORK.isBlank()) {
            return null;
        }
        return POSTGRES.getContainerInfo().getNetworkSettings()
                .getNetworks().get(SHARED_NETWORK).getIpAddress();
    }

    /** JDBC-URL: по внутреннему container-IP на общей сети (DinD-обход, см. class
     *  javadoc), либо штатный getJdbcUrl() через опубликованный host-порт вне DinD. */
    private static String jdbcUrl() {
        if (SHARED_NETWORK != null && !SHARED_NETWORK.isBlank()) {
            return "jdbc:postgresql://" + containerIpOrNull() + ":5432/orders_test";
        }
        return POSTGRES.getJdbcUrl();
    }

    /** Небольшой ретрай с бэкоффом на случай короткого окна между "контейнер
     *  стартовал" (по логам) и фактической готовностью TCP-порта принимать
     *  соединения. */
    private static Connection openConnection() throws SQLException {
        SQLException lastFailure = null;
        for (int attempt = 1; attempt <= 10; attempt++) {
            try {
                return DriverManager.getConnection(jdbcUrl(), POSTGRES.getUsername(), POSTGRES.getPassword());
            } catch (SQLException e) {
                lastFailure = e;
                try {
                    Thread.sleep(500);
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw e;
                }
            }
        }
        throw lastFailure;
    }

    @Test
    void savesAndReadsOrder_againstRealPostgres() throws Exception {
        long start = System.nanoTime();

        OrderRepository repository = new JdbcOrderRepository(() -> {
            try {
                return openConnection();
            } catch (SQLException e) {
                throw new RuntimeException(e);
            }
        });
        OrderService service = new OrderService(repository);

        Order created = service.createOrder("Dave", 12_000);
        assertTrue(created.id() != null && created.id() > 0, "generated id from real Postgres sequence");
        assertEquals(OrderStatus.REVIEW, created.status());

        Optional<Order> reloaded = repository.findById(created.id());
        assertTrue(reloaded.isPresent(), "row must be readable back from Postgres");
        assertEquals("Dave", reloaded.get().customerName());
        assertEquals(12_000L, reloaded.get().amountCents());
        assertEquals(OrderStatus.REVIEW, reloaded.get().status());

        log.info("savesAndReadsOrder_againstRealPostgres took {} ms", (System.nanoTime() - start) / 1_000_000);
    }

    @Test
    void missingOrderReturnsEmpty_againstRealPostgres() {
        OrderRepository repository = new JdbcOrderRepository(() -> {
            try {
                return openConnection();
            } catch (SQLException e) {
                throw new RuntimeException(e);
            }
        });

        assertTrue(repository.findById(999_999L).isEmpty());
    }
}
