package queries

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ageQueries — угол «граф поверх PostgreSQL». Тот же openCypher, что у Neo4j, но
// обёрнутый в SQL-вызов cypher(). Важное ограничение AGE 1.6: нет shortestPath(),
// neds nodes(path) и list comprehension по пути — поэтому Distance выражен через
// length(p) ORDER BY LIMIT 1, а множества собираются без UNION (двумя вызовами).
type ageQueries struct {
	pool *pgxpool.Pool
}

func newAGE(ctx context.Context, cfg Config) (*ageQueries, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PgDSN)
	if err != nil {
		return nil, fmt.Errorf("age dsn: %w", err)
	}
	// каждое соединение пула должно загрузить AGE и выставить search_path
	pcfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, "LOAD 'age'"); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `SET search_path = ag_catalog, "$user", public`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("age pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("age ping: %w", err)
	}
	return &ageQueries{pool: pool}, nil
}

func (q *ageQueries) Backend() string { return "age" }

func (q *ageQueries) Close() error { q.pool.Close(); return nil }

// cypherInts выполняет cypher-тело, возвращающее одну колонку id (agtype), и
// парсит её в []int64.
func (q *ageQueries) cypherInts(ctx context.Context, body string) ([]int64, error) {
	sql := fmt.Sprintf(`SELECT * FROM cypher('platform', $$ %s $$) AS (id agtype)`, body)
	rows, err := q.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		n, err := parseAgtypeInt(s)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// parseAgtypeInt разбирает целое из agtype-представления ("331" или "331::...").
func parseAgtypeInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "::"); i >= 0 { // agtype иногда добавляет ::тип
		s = s[:i]
	}
	s = strings.Trim(s, `"`)
	return strconv.ParseInt(s, 10, 64)
}

func (q *ageQueries) AccessibleResources(ctx context.Context, userID int64) ([]int64, error) {
	viaTeam := fmt.Sprintf(
		`MATCH (u:User)-[:MEMBER_OF]->(:Team)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = %d RETURN res.id`, userID)
	direct := fmt.Sprintf(
		`MATCH (u:User)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = %d RETURN res.id`, userID)
	a, err := q.cypherInts(ctx, viaTeam)
	if err != nil {
		return nil, err
	}
	b, err := q.cypherInts(ctx, direct)
	if err != nil {
		return nil, err
	}
	return sortedUnique(append(a, b...)), nil
}

func (q *ageQueries) ImpactOf(ctx context.Context, serviceID int64, maxDepth int) ([]int64, error) {
	body := fmt.Sprintf(
		`MATCH (dep:Service)-[:DEPENDS_ON*1..%d]->(t:Service) WHERE t.id = %d RETURN DISTINCT dep.id`,
		maxDepth, serviceID)
	ids, err := q.cypherInts(ctx, body)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *ageQueries) CyclicServices(ctx context.Context, maxLen int) ([]int64, error) {
	body := fmt.Sprintf(
		`MATCH (s:Service)-[:DEPENDS_ON*1..%d]->(s) RETURN DISTINCT s.id`, maxLen)
	ids, err := q.cypherInts(ctx, body)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *ageQueries) Distance(ctx context.Context, aID, bID int64, maxLen int) (int, error) {
	// AGE не умеет shortestPath — берём минимальную длину среди путей до maxLen.
	body := fmt.Sprintf(
		`MATCH p=(a:User)-[:COLLABORATES*1..%d]-(b:User) WHERE a.id = %d AND b.id = %d RETURN length(p) ORDER BY length(p) LIMIT 1`,
		maxLen, aID, bID)
	sql := fmt.Sprintf(`SELECT * FROM cypher('platform', $$ %s $$) AS (len agtype)`, body)
	var s string
	err := q.pool.QueryRow(ctx, sql).Scan(&s)
	if err != nil {
		if err == pgx.ErrNoRows {
			return -1, nil
		}
		return 0, err
	}
	n, err := parseAgtypeInt(s)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (q *ageQueries) RingMembers(ctx context.Context, length int) ([]int64, error) {
	body := fmt.Sprintf(
		`MATCH (u:User)-[:COLLABORATES*%d..%d]-(u) RETURN DISTINCT u.id`, length, length)
	ids, err := q.cypherInts(ctx, body)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}
