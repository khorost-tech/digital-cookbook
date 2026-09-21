package tech.khorost.graph;

import org.neo4j.driver.*;

import java.util.List;
import java.util.Map;

import static org.neo4j.driver.Values.parameters;

/** Нативный угол: Cypher параметризован, обход идиоматичен для графовой БД. */
public class Neo4jQueries implements GraphQueries {

    private final Driver driver;

    public Neo4jQueries(Config cfg) {
        this.driver = GraphDatabase.driver(cfg.neo4jUri(),
                AuthTokens.basic(cfg.neo4jUser(), cfg.neo4jPass()));
        this.driver.verifyConnectivity();
    }

    @Override
    public String backend() {
        return "neo4j";
    }

    @Override
    public void close() {
        driver.close();
    }

    private List<Long> queryIds(String cypher, Map<String, Object> params) {
        try (Session s = driver.session()) {
            return s.executeRead(tx -> {
                var res = tx.run(cypher, params == null ? parameters() : Values.value(params));
                return res.list(r -> r.get("id").asLong());
            }).stream().distinct().sorted().toList();
        }
    }

    @Override
    public List<Long> accessibleResources(long userId) {
        String cypher = """
                MATCH (u:User {id:$uid})-[:MEMBER_OF]->(:Team)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource)
                RETURN res.id AS id
                UNION
                MATCH (u:User {id:$uid})-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource)
                RETURN res.id AS id""";
        return queryIds(cypher, Map.of("uid", userId));
    }

    @Override
    public List<Long> impactOf(long serviceId, int maxDepth) {
        String cypher = """
                MATCH (dep:Service)-[:DEPENDS_ON*1..%d]->(t:Service {id:$sid})
                RETURN DISTINCT dep.id AS id""".formatted(maxDepth);
        return queryIds(cypher, Map.of("sid", serviceId));
    }

    @Override
    public List<Long> cyclicServices(int maxLen) {
        String cypher = """
                MATCH (s:Service)-[:DEPENDS_ON*1..%d]->(s)
                RETURN DISTINCT s.id AS id""".formatted(maxLen);
        return queryIds(cypher, null);
    }

    @Override
    public long distance(long aId, long bId, int maxLen) {
        String cypher = """
                MATCH (a:User {id:$a}), (b:User {id:$b})
                MATCH p = shortestPath((a)-[:COLLABORATES*1..%d]-(b))
                RETURN length(p) AS len""".formatted(maxLen);
        try (Session s = driver.session()) {
            return s.executeRead(tx -> {
                var res = tx.run(cypher, parameters("a", aId, "b", bId));
                if (!res.hasNext()) return -1L;
                return res.next().get("len").asLong();
            });
        }
    }

    @Override
    public List<Long> ringMembers(int length) {
        // каждый член кольца сам является замкнутым обходом длины length
        String cypher = """
                MATCH (u:User)-[:COLLABORATES*%d]-(u)
                RETURN DISTINCT u.id AS id""".formatted(length);
        return queryIds(cypher, null);
    }
}
