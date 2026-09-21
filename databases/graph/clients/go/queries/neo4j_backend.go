package queries

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// neo4jQueries — нативный угол. Cypher параметризован (никакой ручной склейки
// строк с данными), обход выражен идиоматично для графовой БД.
type neo4jQueries struct {
	driver neo4j.DriverWithContext
}

func newNeo4j(ctx context.Context, cfg Config) (*neo4jQueries, error) {
	d, err := neo4j.NewDriverWithContext(cfg.Neo4jURI, neo4j.BasicAuth(cfg.Neo4jUser, cfg.Neo4jPass, ""))
	if err != nil {
		return nil, fmt.Errorf("neo4j driver: %w", err)
	}
	if err := d.VerifyConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("neo4j connect: %w", err)
	}
	return &neo4jQueries{driver: d}, nil
}

func (q *neo4jQueries) Backend() string { return "neo4j" }

func (q *neo4jQueries) Close() error { return q.driver.Close(context.Background()) }

// query выполняет read-запрос и собирает целочисленную колонку col.
func (q *neo4jQueries) queryInts(ctx context.Context, col, cypher string, params map[string]any) ([]int64, error) {
	res, err := neo4j.ExecuteQuery(ctx, q.driver, cypher, params,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(res.Records))
	for _, rec := range res.Records {
		v, _, err := neo4j.GetRecordValue[int64](rec, col)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (q *neo4jQueries) AccessibleResources(ctx context.Context, userID int64) ([]int64, error) {
	const cypher = `
		MATCH (u:User {id:$uid})-[:MEMBER_OF]->(:Team)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource)
		RETURN res.id AS id
		UNION
		MATCH (u:User {id:$uid})-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource)
		RETURN res.id AS id`
	ids, err := q.queryInts(ctx, "id", cypher, map[string]any{"uid": userID})
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *neo4jQueries) ImpactOf(ctx context.Context, serviceID int64, maxDepth int) ([]int64, error) {
	cypher := fmt.Sprintf(`
		MATCH (dep:Service)-[:DEPENDS_ON*1..%d]->(t:Service {id:$sid})
		RETURN DISTINCT dep.id AS id`, maxDepth)
	ids, err := q.queryInts(ctx, "id", cypher, map[string]any{"sid": serviceID})
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *neo4jQueries) CyclicServices(ctx context.Context, maxLen int) ([]int64, error) {
	cypher := fmt.Sprintf(`
		MATCH (s:Service)-[:DEPENDS_ON*1..%d]->(s)
		RETURN DISTINCT s.id AS id`, maxLen)
	ids, err := q.queryInts(ctx, "id", cypher, nil)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *neo4jQueries) Distance(ctx context.Context, aID, bID int64, maxLen int) (int, error) {
	cypher := fmt.Sprintf(`
		MATCH (a:User {id:$a}), (b:User {id:$b})
		MATCH p = shortestPath((a)-[:COLLABORATES*1..%d]-(b))
		RETURN length(p) AS len`, maxLen)
	res, err := neo4j.ExecuteQuery(ctx, q.driver, cypher, map[string]any{"a": aID, "b": bID},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return 0, err
	}
	if len(res.Records) == 0 {
		return -1, nil
	}
	v, _, err := neo4j.GetRecordValue[int64](res.Records[0], "len")
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

func (q *neo4jQueries) RingMembers(ctx context.Context, length int) ([]int64, error) {
	// Кольцо ровно из length рёбер. Каждый член кольца сам является замкнутым обходом
	// длины length, поэтому DISTINCT u.id даёт всё множество участников без nodes(path).
	// Между парой пользователей не больше одного ребра, а var-length не переиспользует
	// рёбра — поэтому замкнутый обход здесь эквивалентен простому циклу.
	cypher := fmt.Sprintf(`
		MATCH (u:User)-[:COLLABORATES*%d]-(u)
		RETURN DISTINCT u.id AS id`, length)
	ids, err := q.queryInts(ctx, "id", cypher, nil)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}
