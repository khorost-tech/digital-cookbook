package tech.khorost.dataaccess;

import javax.sql.DataSource;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.Random;

/**
 * Схема + сид: authors 1:N books — классика N+1 (список авторов, у каждого
 * коллекция книг). AUTHORS авторов, у каждого от MIN_BOOKS до MAX_BOOKS книг.
 * Пересоздаём схему при каждом запуске (DROP+CREATE) — стенд идемпотентный.
 */
public final class SchemaInit {
    public static final int AUTHORS = 20;
    private static final int MIN_BOOKS = 2;
    private static final int MAX_BOOKS = 6;
    private static final long SEED = 42L; // детерминированный сид — воспроизводимые числа

    private SchemaInit() {
    }

    public static void run(DataSource ds) throws SQLException {
        try (Connection conn = ds.getConnection()) {
            try (Statement st = conn.createStatement()) {
                st.execute("DROP TABLE IF EXISTS books");
                st.execute("DROP TABLE IF EXISTS authors");
                st.execute("""
                        CREATE TABLE authors (
                            id   BIGSERIAL PRIMARY KEY,
                            name TEXT NOT NULL
                        )""");
                st.execute("""
                        CREATE TABLE books (
                            id              BIGSERIAL PRIMARY KEY,
                            author_id       BIGINT NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
                            title           TEXT NOT NULL,
                            published_year  INT
                        )""");
                st.execute("CREATE INDEX idx_books_author_id ON books(author_id)");
            }

            Random rnd = new Random(SEED);
            conn.setAutoCommit(false);
            int totalBooks = 0;
            try (PreparedStatement insAuthor = conn.prepareStatement(
                    "INSERT INTO authors(name) VALUES (?)", Statement.RETURN_GENERATED_KEYS);
                 PreparedStatement insBook = conn.prepareStatement(
                         "INSERT INTO books(author_id, title, published_year) VALUES (?, ?, ?)")) {

                for (int i = 1; i <= AUTHORS; i++) {
                    insAuthor.setString(1, "Author " + i);
                    insAuthor.executeUpdate();
                    long authorId;
                    try (ResultSet keys = insAuthor.getGeneratedKeys()) {
                        keys.next();
                        authorId = keys.getLong(1);
                    }

                    int books = MIN_BOOKS + rnd.nextInt(MAX_BOOKS - MIN_BOOKS + 1);
                    for (int b = 1; b <= books; b++) {
                        insBook.setLong(1, authorId);
                        insBook.setString(2, "Author " + i + " — Book " + b);
                        insBook.setInt(3, 1990 + rnd.nextInt(35));
                        insBook.addBatch();
                        totalBooks++;
                    }
                }
                insBook.executeBatch();
            }
            conn.commit();
            conn.setAutoCommit(true);
            System.out.printf("SchemaInit: создано authors=%d, books=%d (seed=%d)%n", AUTHORS, totalBooks, SEED);
        }
    }
}
