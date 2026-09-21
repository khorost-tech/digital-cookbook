package tech.khorost.graph;

import java.sql.*;
import java.util.ArrayList;
import java.util.List;

/** Honest SQL-only угол: тот же граф в реляционных таблицах, обход через WITH RECURSIVE. */
public class BaselineQueries implements GraphQueries {

    private final Connection conn;

    public BaselineQueries(Config cfg) {
        try {
            this.conn = DriverManager.getConnection(cfg.pgUrl());
        } catch (SQLException e) {
            throw new RuntimeException("baseline connect: " + e.getMessage(), e);
        }
    }

    @Override
    public String backend() {
        return "baseline";
    }

    @Override
    public void close() {
        try {
            conn.close();
        } catch (SQLException ignored) {
        }
    }

    private List<Long> queryIds(String sql, Object... args) {
        List<Long> out = new ArrayList<>();
        try (PreparedStatement ps = conn.prepareStatement(sql)) {
            for (int i = 0; i < args.length; i++) ps.setObject(i + 1, args[i]);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) out.add(rs.getLong(1));
            }
        } catch (SQLException e) {
            throw new RuntimeException("baseline query: " + e.getMessage(), e);
        }
        return out.stream().distinct().sorted().toList();
    }

    @Override
    public List<Long> accessibleResources(long userId) {
        String sql = """
                SELECT g.resource_id
                FROM member_of m
                JOIN has_role hr ON hr.subject_id = m.team_id AND hr.subject_kind = 'team'
                JOIN grants g   ON g.role_id = hr.role_id
                WHERE m.user_id = ?
                UNION
                SELECT g.resource_id
                FROM has_role hr
                JOIN grants g ON g.role_id = hr.role_id
                WHERE hr.subject_id = ? AND hr.subject_kind = 'user'""";
        return queryIds(sql, userId, userId);
    }

    @Override
    public List<Long> impactOf(long serviceId, int maxDepth) {
        String sql = """
                WITH RECURSIVE up AS (
                    SELECT src_id, 1 AS depth
                    FROM depends_on WHERE dst_id = ? AND kind = 'service'
                    UNION
                    SELECT d.src_id, up.depth + 1
                    FROM depends_on d JOIN up ON d.dst_id = up.src_id
                    WHERE d.kind = 'service' AND up.depth < ?
                )
                SELECT DISTINCT src_id FROM up""";
        return queryIds(sql, serviceId, maxDepth);
    }

    @Override
    public List<Long> cyclicServices(int maxLen) {
        String sql = """
                WITH RECURSIVE reach AS (
                    SELECT src_id AS start, dst_id AS cur, 1 AS depth
                    FROM depends_on WHERE kind = 'service'
                    UNION ALL
                    SELECT r.start, d.dst_id, r.depth + 1
                    FROM depends_on d JOIN reach r ON d.src_id = r.cur
                    WHERE d.kind = 'service' AND r.depth < ?
                )
                SELECT DISTINCT start FROM reach WHERE cur = start""";
        return queryIds(sql, maxLen);
    }

    @Override
    public long distance(long aId, long bId, int maxLen) {
        String sql = """
                WITH RECURSIVE bfs AS (
                    SELECT ?::bigint AS node, 0 AS dist
                    UNION
                    SELECT CASE WHEN c.a_id = b.node THEN c.b_id ELSE c.a_id END, b.dist + 1
                    FROM bfs b
                    JOIN collaborates c ON (c.a_id = b.node OR c.b_id = b.node)
                    WHERE b.dist < ?
                )
                SELECT COALESCE(MIN(dist), -1) FROM bfs WHERE node = ?""";
        try (PreparedStatement ps = conn.prepareStatement(sql)) {
            ps.setLong(1, aId);
            ps.setInt(2, maxLen);
            ps.setLong(3, bId);
            try (ResultSet rs = ps.executeQuery()) {
                return rs.next() ? rs.getLong(1) : -1L;
            }
        } catch (SQLException e) {
            throw new RuntimeException("baseline distance: " + e.getMessage(), e);
        }
    }

    @Override
    public List<Long> ringMembers(int length) {
        String sql = """
                WITH RECURSIVE walk AS (
                    SELECT u.id AS start, ARRAY[u.id] AS path, u.id AS cur, 0 AS len
                    FROM users u
                    UNION ALL
                    SELECT w.start, w.path || e.nb, e.nb, w.len + 1
                    FROM walk w
                    CROSS JOIN LATERAL (
                        SELECT CASE WHEN c.a_id = w.cur THEN c.b_id ELSE c.a_id END AS nb
                        FROM collaborates c WHERE w.cur IN (c.a_id, c.b_id)
                    ) e
                    WHERE w.len < ? - 1
                      AND e.nb > w.start
                      AND NOT e.nb = ANY(w.path)
                )
                SELECT DISTINCT unnest(path) AS id
                FROM walk w
                WHERE w.len = ? - 1
                  AND EXISTS (
                    SELECT 1 FROM collaborates c
                    WHERE (c.a_id = w.cur AND c.b_id = w.start)
                       OR (c.b_id = w.cur AND c.a_id = w.start)
                  )""";
        return queryIds(sql, length, length);
    }
}
