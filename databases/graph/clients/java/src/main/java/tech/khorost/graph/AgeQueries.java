package tech.khorost.graph;

import java.sql.*;
import java.util.ArrayList;
import java.util.List;

/**
 * Угол «граф поверх PostgreSQL». Тот же openCypher, обёрнутый в SQL-вызов cypher().
 * Ограничение AGE 1.6: нет shortestPath(), nodes(path), list comprehension — поэтому
 * distance выражен через length(p) ORDER BY LIMIT 1, множества собираются без UNION.
 */
public class AgeQueries implements GraphQueries {

    private final Connection conn;

    public AgeQueries(Config cfg) {
        try {
            this.conn = DriverManager.getConnection(cfg.pgUrl());
            try (Statement st = conn.createStatement()) {
                st.execute("LOAD 'age'");
                st.execute("SET search_path = ag_catalog, \"$user\", public");
            }
        } catch (SQLException e) {
            throw new RuntimeException("age connect: " + e.getMessage(), e);
        }
    }

    @Override
    public String backend() {
        return "age";
    }

    @Override
    public void close() {
        try {
            conn.close();
        } catch (SQLException ignored) {
        }
    }

    private List<Long> cypherIds(String body) {
        String sql = "SELECT * FROM cypher('platform', $$ " + body + " $$) AS (id agtype)";
        List<Long> out = new ArrayList<>();
        try (Statement st = conn.createStatement(); ResultSet rs = st.executeQuery(sql)) {
            while (rs.next()) {
                out.add(parseAgtypeLong(rs.getString(1)));
            }
        } catch (SQLException e) {
            throw new RuntimeException("age query: " + e.getMessage(), e);
        }
        return out.stream().distinct().sorted().toList();
    }

    static long parseAgtypeLong(String s) {
        s = s.trim();
        int i = s.indexOf("::");
        if (i >= 0) s = s.substring(0, i);
        s = s.replace("\"", "");
        return Long.parseLong(s);
    }

    @Override
    public List<Long> accessibleResources(long userId) {
        List<Long> out = new ArrayList<>();
        out.addAll(cypherIds("MATCH (u:User)-[:MEMBER_OF]->(:Team)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = "
                + userId + " RETURN res.id"));
        out.addAll(cypherIds("MATCH (u:User)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = "
                + userId + " RETURN res.id"));
        return out.stream().distinct().sorted().toList();
    }

    @Override
    public List<Long> impactOf(long serviceId, int maxDepth) {
        return cypherIds("MATCH (dep:Service)-[:DEPENDS_ON*1.." + maxDepth
                + "]->(t:Service) WHERE t.id = " + serviceId + " RETURN DISTINCT dep.id");
    }

    @Override
    public List<Long> cyclicServices(int maxLen) {
        return cypherIds("MATCH (s:Service)-[:DEPENDS_ON*1.." + maxLen + "]->(s) RETURN DISTINCT s.id");
    }

    @Override
    public long distance(long aId, long bId, int maxLen) {
        String body = "MATCH p=(a:User)-[:COLLABORATES*1.." + maxLen + "]-(b:User) WHERE a.id = "
                + aId + " AND b.id = " + bId + " RETURN length(p) ORDER BY length(p) LIMIT 1";
        String sql = "SELECT * FROM cypher('platform', $$ " + body + " $$) AS (len agtype)";
        try (Statement st = conn.createStatement(); ResultSet rs = st.executeQuery(sql)) {
            if (!rs.next()) return -1L;
            return parseAgtypeLong(rs.getString(1));
        } catch (SQLException e) {
            throw new RuntimeException("age distance: " + e.getMessage(), e);
        }
    }

    @Override
    public List<Long> ringMembers(int length) {
        return cypherIds("MATCH (u:User)-[:COLLABORATES*" + length + ".." + length + "]-(u) RETURN DISTINCT u.id");
    }
}
