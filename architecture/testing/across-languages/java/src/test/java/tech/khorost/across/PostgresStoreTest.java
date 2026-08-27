package tech.khorost.across;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.mock;

import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Tag;
import org.junit.jupiter.api.Test;
import org.postgresql.ds.PGSimpleDataSource;
import org.testcontainers.containers.PostgreSQLContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;

/**
 * ПРИЁМ 4: Testcontainers — тот же самый Postgres, что и в Go/Python-частях.
 * Именно поэтому Testcontainers и есть «общий знаменатель»: API отличается,
 * контейнер и проверяемое поведение — одни и те же.
 *
 * <p>Запуск: {@code mvn test -Dgroups=integration}
 */
@Tag("integration")
@Testcontainers
class PostgresStoreTest {

    @Container
    @SuppressWarnings("resource") // жизненным циклом управляет @Testcontainers
    static final PostgreSQLContainer<?> PG = new PostgreSQLContainer<>("postgres:18.1-alpine")
            .withDatabaseName("orders")
            .withUsername("test")
            .withPassword("test");

    private PostgresStore store;

    @BeforeEach
    void setUp() throws Exception {
        PGSimpleDataSource ds = new PGSimpleDataSource();
        ds.setUrl(PG.getJdbcUrl());
        ds.setUser(PG.getUsername());
        ds.setPassword(PG.getPassword());
        store = new PostgresStore(ds);

        try (var c = ds.getConnection(); var st = c.createStatement()) {
            st.execute(PostgresStore.SCHEMA);
            st.execute("TRUNCATE orders"); // изоляция: каждый тест с чистого листа
        }
    }

    @Test
    void заказДоезжаетДоPostgres() {
        OrderService svc = new OrderService(store, mock(Notifier.class));

        svc.create("o-1", "u-1", 25_000);

        List<Order> got = store.byUser("u-1");
        assertEquals(1, got.size());
        assertEquals(22_500, got.get(0).priceCent(), "250.00 минус 10%");
    }

    /** Первичный ключ — то, чего фейк не знает вовсе. */
    @Test
    void дубльIdОтклоняетсяБазой() {
        Order o = new Order("o-1", "u-1", 10_000, 9_500);
        store.save(o);

        assertThrows(IllegalStateException.class, () -> store.save(o),
                "повторный save с тем же ID должен отклоняться первичным ключом");
    }
}
