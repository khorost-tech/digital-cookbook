package tech.khorost.pgcompat;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DatabaseMetaData;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.List;

/**
 * Прогоняет probes.tsv через JDBC. Драйвер определяется схемой URL:
 * jdbc:postgresql://... — штатный драйвер PostgreSQL,
 * jdbc:picodata://...   — фирменный драйвер Picodata.
 *
 * Формат вывода общий для всех раннеров стенда: "id\tOK|FAIL\tсообщение".
 * Имя и версия фактически использованного драйвера печатаются в stderr — чтобы
 * в отчёте нельзя было перепутать, кто именно отвечал на пробы.
 */
public final class Runner {

    private static final int MSG_LIMIT = 160;

    private Runner() {
    }

    private static String oneLine(String text) {
        String s = String.valueOf(text).replaceAll("\\s+", " ").trim();
        return s.length() > MSG_LIMIT ? s.substring(0, MSG_LIMIT) : s;
    }

    /** Считает строки в ResultSet, закрывая его. */
    private static int count(ResultSet rs) throws Exception {
        int n = 0;
        try (ResultSet r = rs) {
            while (r.next()) {
                n++;
            }
        }
        return n;
    }

    /** Пробы уровня JDBC-метаданных — то, чем пользуется GUI-клиент. */
    private static void metadataProbes(Connection conn) {
        DatabaseMetaData md;
        try {
            md = conn.getMetaData();
        } catch (Exception e) {
            System.out.println("metadata_available\tFAIL\t" + oneLine(e.getMessage()));
            return;
        }

        probe("md_product_name", () -> md.getDatabaseProductName() + " " + md.getDatabaseProductVersion());
        probe("md_get_tables", () -> "таблиц: " + count(md.getTables(null, null, "%", new String[]{"TABLE"})));
        probe("md_get_columns", () -> "колонок products: " + count(md.getColumns(null, null, "products", "%")));
        probe("md_get_schemas", () -> "схем: " + count(md.getSchemas()));
        probe("md_get_primary_keys", () -> "ключей products: " + count(md.getPrimaryKeys(null, null, "products")));
        probe("md_get_type_info", () -> "типов: " + count(md.getTypeInfo()));
        probe("md_supports_transactions", () -> "заявляет поддержку транзакций: " + md.supportsTransactions());
    }

    /** Обёртка: печатает OK со значением либо FAIL с сообщением. */
    private static void probe(String id, ThrowingSupplier body) {
        try {
            System.out.println(id + "\tOK\t" + oneLine(body.get()));
        } catch (Exception e) {
            System.out.println(id + "\tFAIL\t" + oneLine(e.getMessage()));
        }
    }

    @FunctionalInterface
    private interface ThrowingSupplier {
        String get() throws Exception;
    }

    public static void main(String[] args) throws Exception {
        if (args.length < 2) {
            System.err.println("usage: Runner <jdbc-url> <probes.tsv> [user] [password]");
            System.exit(2);
        }
        String url = args[0];
        Path probes = Path.of(args[1]);
        String user = args.length > 2 ? args[2] : "admin";
        String password = args.length > 3 ? args[3] : "Picodata1";

        Connection conn = DriverManager.getConnection(url, user, password);
        System.err.println("драйвер: " + conn.getMetaData().getDriverName()
                + " " + conn.getMetaData().getDriverVersion());

        // Режим метаданных: SQL-пробы у обоих драйверов совпали построчно, значит
        // граница совместимости лежит на сервере. Остаётся вопрос, зачем тогда
        // фирменный драйвер, — и самое вероятное место различий не SQL, а
        // DatabaseMetaData: именно на нём GUI-клиенты вроде DBeaver строят дерево
        // таблиц и колонок. Эти вызовы к SQL-грамматике отношения не имеют.
        if ("--metadata".equals(args[1])) {
            metadataProbes(conn);
            conn.close();
            return;
        }

        List<String> lines = Files.readAllLines(probes, StandardCharsets.UTF_8);
        for (String line : lines) {
            if (line.isEmpty() || line.startsWith("#")) {
                continue;
            }
            String[] parts = line.split("\t", 3);
            if (parts.length < 3) {
                continue;
            }
            String id = parts[0];
            String sql = parts[2];
            try (Statement st = conn.createStatement()) {
                st.execute(sql);
                System.out.println(id + "\tOK\t");
            } catch (Exception e) {
                System.out.println(id + "\tFAIL\t" + oneLine(e.getMessage()));
                // Соединение после ошибки может остаться непригодным, и тогда
                // все следующие пробы вернули бы FAIL по инерции — матрица
                // выглядела бы правдоподобно и была бы ложной.
                try {
                    conn.close();
                } catch (Exception ignored) {
                    // соединение уже нерабочее — важно только пересоздать его
                }
                conn = DriverManager.getConnection(url, user, password);
            }
        }
        conn.close();
    }
}
