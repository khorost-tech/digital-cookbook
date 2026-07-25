package tech.khorost.dataaccess;

import org.jooq.DSLContext;
import org.jooq.Field;
import org.jooq.Record3;
import org.jooq.Result;
import org.jooq.SQLDialect;
import org.jooq.Table;
import org.jooq.impl.DSL;

import javax.sql.DataSource;
import java.sql.Connection;
import java.sql.SQLException;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.TreeMap;

/**
 * (3) jOOQ: типобезопасный DSL. Table/Field объявлены вручную через DSL.name()
 * вместо codegen (codegen требует живую БД на этапе mvn generate-sources —
 * см. комментарий в pom.xml), но выражение запроса всё равно строится через
 * Java-типы (select/from/join/on), а не конкатенацию строк — компилятор ловит
 * опечатки в структуре запроса, хотя имена колонок здесь не проверяются
 * компилятором (это и есть разница с полноценным codegen).
 */
public final class JooqDemo {
    private static final Table<?> AUTHORS = DSL.table(DSL.name("authors"));
    private static final Table<?> BOOKS = DSL.table(DSL.name("books"));

    private static final Field<Long> A_ID = DSL.field(DSL.name("authors", "id"), Long.class);
    private static final Field<String> A_NAME = DSL.field(DSL.name("authors", "name"), String.class);
    private static final Field<Long> B_ID = DSL.field(DSL.name("books", "id"), Long.class);
    private static final Field<Long> B_AUTHOR_ID = DSL.field(DSL.name("books", "author_id"), Long.class);
    private static final Field<String> B_TITLE = DSL.field(DSL.name("books", "title"), String.class);

    private JooqDemo() {
    }

    public static Map<String, java.util.List<String>> authorsWithBooks(DataSource ds) throws SQLException {
        Map<String, java.util.List<String>> result = new LinkedHashMap<>();
        try (Connection conn = ds.getConnection()) {
            DSLContext ctx = DSL.using(conn, SQLDialect.POSTGRES);
            Result<Record3<Long, String, String>> rows = ctx
                    .select(A_ID, A_NAME, B_TITLE)
                    .from(AUTHORS)
                    .join(BOOKS).on(B_AUTHOR_ID.eq(A_ID))
                    .orderBy(A_ID, B_ID)
                    .fetch();

            for (var r : rows) {
                result.computeIfAbsent(r.value2(), k -> new java.util.ArrayList<>()).add(r.value3());
            }
        }
        System.out.println("jOOQ: 1 SQL-запрос (JOIN authors+books через DSL), authors=" + result.size());
        return new TreeMap<>(result);
    }
}
