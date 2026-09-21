package tech.khorost.graph;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.Statement;
import java.util.List;
import java.util.stream.Collectors;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * Интеграционный тест: грузит ту же крошечную фикстуру, что и Go-клиент, и проверяет,
 * что neo4j, age и baseline на Java дают те же руками посчитанные ответы.
 *
 * ВНИМАНИЕ: сбрасывает содержимое всех трёх хранилищ. Требует поднятых neo4j и postgres.
 */
class QueriesTest {

    static final long[][] COLLAB = {{1, 2}, {2, 3}, {5, 6}, {6, 7}, {7, 8}, {5, 8}};
    static final long[][] DEPS = {{40, 41}, {41, 42}, {43, 44}, {44, 45}, {45, 43}};

    static Config cfg;

    @BeforeAll
    static void loadFixtures() throws Exception {
        cfg = Config.fromEnv();
        loadPg();
        loadNeo4j();
    }

    static void loadPg() throws Exception {
        try (Connection c = DriverManager.getConnection(cfg.pgUrl()); Statement st = c.createStatement()) {
            String schema = Files.readString(Path.of("../../dataset/schema.sql"));
            for (String stmt : splitSql(schema)) st.execute(stmt);

            for (long id : new long[]{1, 2, 3, 5, 6, 7, 8})
                st.execute("INSERT INTO users(id,name) VALUES(" + id + ",'u')");
            st.execute("INSERT INTO teams(id,name) VALUES(10,'t')");
            st.execute("INSERT INTO roles(id,name) VALUES(20,'r')");
            st.execute("INSERT INTO resources(id,name) VALUES(30,'a'),(31,'b')");
            for (long id : new long[]{40, 41, 42, 43, 44, 45})
                st.execute("INSERT INTO services(id,name) VALUES(" + id + ",'s')");
            st.execute("INSERT INTO member_of(user_id,team_id) VALUES(1,10)");
            st.execute("INSERT INTO has_role(subject_id,subject_kind,role_id) VALUES(10,'team',20),(2,'user',20)");
            st.execute("INSERT INTO grants(role_id,resource_id) VALUES(20,30),(20,31)");
            for (long[] e : COLLAB)
                st.execute("INSERT INTO collaborates(a_id,b_id) VALUES(" + e[0] + "," + e[1] + ")");
            for (long[] d : DEPS)
                st.execute("INSERT INTO depends_on(src_id,dst_id,kind) VALUES(" + d[0] + "," + d[1] + ",'service')");

            // AGE: пересоздать граф и залить ту же фикстуру
            st.execute("LOAD 'age'");
            st.execute("SET search_path = ag_catalog, \"$user\", public");
            st.execute("""
                    DO $$ BEGIN
                      IF EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name='platform') THEN
                        PERFORM ag_catalog.drop_graph('platform', true);
                      END IF;
                      PERFORM ag_catalog.create_graph('platform');
                    END $$;""");
            cy(st, "UNWIND [1,2,3,5,6,7,8] AS id CREATE (:User {id: id})");
            cy(st, "CREATE (:Team {id: 10})");
            cy(st, "CREATE (:Role {id: 20})");
            cy(st, "UNWIND [30,31] AS id CREATE (:Resource {id: id})");
            cy(st, "UNWIND [40,41,42,43,44,45] AS id CREATE (:Service {id: id})");
            cy(st, "MATCH (u:User),(t:Team) WHERE u.id=1 AND t.id=10 CREATE (u)-[:MEMBER_OF]->(t)");
            cy(st, "MATCH (t:Team),(r:Role) WHERE t.id=10 AND r.id=20 CREATE (t)-[:HAS_ROLE]->(r)");
            cy(st, "MATCH (u:User),(r:Role) WHERE u.id=2 AND r.id=20 CREATE (u)-[:HAS_ROLE]->(r)");
            cy(st, "MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=30 CREATE (r)-[:GRANTS]->(x)");
            cy(st, "MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=31 CREATE (r)-[:GRANTS]->(x)");
            for (long[] d : DEPS)
                cy(st, edge("Service", "Service", "DEPENDS_ON", d[0], d[1]));
            for (long[] e : COLLAB)
                cy(st, edge("User", "User", "COLLABORATES", e[0], e[1]));
        }
    }

    static void cy(Statement st, String body) throws Exception {
        st.execute("SELECT * FROM cypher('platform', $$ " + body + " $$) AS (v agtype)");
    }

    static String edge(String la, String lb, String rel, long a, long b) {
        return "MATCH (x:" + la + "),(y:" + lb + ") WHERE x.id=" + a + " AND y.id=" + b
                + " CREATE (x)-[:" + rel + "]->(y)";
    }

    static void loadNeo4j() {
        var driver = org.neo4j.driver.GraphDatabase.driver(cfg.neo4jUri(),
                org.neo4j.driver.AuthTokens.basic(cfg.neo4jUser(), cfg.neo4jPass()));
        try (var s = driver.session()) {
            s.run("MATCH (n) DETACH DELETE n").consume();
            s.run("UNWIND [1,2,3,5,6,7,8] AS id CREATE (:User {id:id})").consume();
            s.run("CREATE (:Team {id:10})").consume();
            s.run("CREATE (:Role {id:20})").consume();
            s.run("UNWIND [30,31] AS id CREATE (:Resource {id:id})").consume();
            s.run("UNWIND [40,41,42,43,44,45] AS id CREATE (:Service {id:id})").consume();
            s.run("MATCH (u:User{id:1}),(t:Team{id:10}) CREATE (u)-[:MEMBER_OF]->(t)").consume();
            s.run("MATCH (t:Team{id:10}),(r:Role{id:20}) CREATE (t)-[:HAS_ROLE]->(r)").consume();
            s.run("MATCH (u:User{id:2}),(r:Role{id:20}) CREATE (u)-[:HAS_ROLE]->(r)").consume();
            s.run("MATCH (r:Role{id:20}),(x:Resource{id:30}) CREATE (r)-[:GRANTS]->(x)").consume();
            s.run("MATCH (r:Role{id:20}),(x:Resource{id:31}) CREATE (r)-[:GRANTS]->(x)").consume();
            for (long[] d : DEPS)
                s.run(edge("Service", "Service", "DEPENDS_ON", d[0], d[1])).consume();
            for (long[] e : COLLAB)
                s.run(edge("User", "User", "COLLABORATES", e[0], e[1])).consume();
        } finally {
            driver.close();
        }
    }

    static List<String> splitSql(String script) {
        StringBuilder b = new StringBuilder();
        for (String line : script.split("\n")) {
            int i = line.indexOf("--");
            if (i >= 0) line = line.substring(0, i);
            b.append(line).append('\n');
        }
        return java.util.Arrays.stream(b.toString().split(";"))
                .map(String::trim).filter(s -> !s.isEmpty()).collect(Collectors.toList());
    }

    @Test
    void allBackendsAgree() {
        for (String backend : new String[]{"neo4j", "age", "baseline"}) {
            try (GraphQueries q = Config.create(backend, cfg)) {
                assertEquals(List.of(30L, 31L), q.accessibleResources(1), backend + " access(1)");
                assertEquals(List.of(30L, 31L), q.accessibleResources(2), backend + " access(2)");
                assertEquals(List.of(), q.accessibleResources(3), backend + " access(3)");
                assertEquals(List.of(40L, 41L), q.impactOf(42, 10), backend + " impact(42)");
                assertEquals(List.of(40L), q.impactOf(41, 10), backend + " impact(41)");
                assertEquals(List.of(43L, 44L, 45L), q.cyclicServices(10), backend + " cyclic");
                assertEquals(List.of(5L, 6L, 7L, 8L), q.ringMembers(4), backend + " ring(4)");
                assertEquals(List.of(), q.ringMembers(3), backend + " ring(3)");
                assertEquals(2L, q.distance(1, 3, 10), backend + " dist(1,3)");
                assertEquals(2L, q.distance(5, 7, 10), backend + " dist(5,7)");
                assertEquals(1L, q.distance(5, 8, 10), backend + " dist(5,8)");
                assertEquals(-1L, q.distance(1, 7, 10), backend + " dist(1,7)");
            }
        }
    }
}
