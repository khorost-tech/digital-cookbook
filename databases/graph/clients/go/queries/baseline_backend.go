package queries

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// baselineQueries — honest SQL-only угол: тот же граф в реляционных таблицах,
// обход через WITH RECURSIVE. Показывает, где чистого PostgreSQL достаточно и
// чем именно платит рекурсивный CTE на глубоком traversal.
type baselineQueries struct {
	pool *pgxpool.Pool
}

func newBaseline(ctx context.Context, cfg Config) (*baselineQueries, error) {
	pool, err := pgxpool.New(ctx, cfg.PgDSN)
	if err != nil {
		return nil, fmt.Errorf("baseline pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("baseline ping: %w", err)
	}
	return &baselineQueries{pool: pool}, nil
}

func (q *baselineQueries) Backend() string { return "baseline" }

func (q *baselineQueries) Close() error { q.pool.Close(); return nil }

func (q *baselineQueries) queryInts(ctx context.Context, sql string, args ...any) ([]int64, error) {
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (q *baselineQueries) AccessibleResources(ctx context.Context, userID int64) ([]int64, error) {
	const sql = `
		SELECT g.resource_id
		FROM member_of m
		JOIN has_role hr ON hr.subject_id = m.team_id AND hr.subject_kind = 'team'
		JOIN grants g   ON g.role_id = hr.role_id
		WHERE m.user_id = $1
		UNION
		SELECT g.resource_id
		FROM has_role hr
		JOIN grants g ON g.role_id = hr.role_id
		WHERE hr.subject_id = $1 AND hr.subject_kind = 'user'`
	ids, err := q.queryInts(ctx, sql, userID)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *baselineQueries) ImpactOf(ctx context.Context, serviceID int64, maxDepth int) ([]int64, error) {
	const sql = `
		WITH RECURSIVE up AS (
			SELECT src_id, 1 AS depth
			FROM depends_on WHERE dst_id = $1 AND kind = 'service'
			UNION
			SELECT d.src_id, up.depth + 1
			FROM depends_on d JOIN up ON d.dst_id = up.src_id
			WHERE d.kind = 'service' AND up.depth < $2
		)
		SELECT DISTINCT src_id FROM up`
	ids, err := q.queryInts(ctx, sql, serviceID, maxDepth)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *baselineQueries) CyclicServices(ctx context.Context, maxLen int) ([]int64, error) {
	const sql = `
		WITH RECURSIVE reach AS (
			SELECT src_id AS start, dst_id AS cur, 1 AS depth
			FROM depends_on WHERE kind = 'service'
			UNION ALL
			SELECT r.start, d.dst_id, r.depth + 1
			FROM depends_on d JOIN reach r ON d.src_id = r.cur
			WHERE d.kind = 'service' AND r.depth < $1
		)
		SELECT DISTINCT start FROM reach WHERE cur = start`
	ids, err := q.queryInts(ctx, sql, maxLen)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}

func (q *baselineQueries) Distance(ctx context.Context, aID, bID int64, maxLen int) (int, error) {
	// BFS рекурсивным CTE; UNION дедуплицирует одинаковые (node,dist), граница dist
	// гарантирует остановку. MIN(dist) до целевого узла — искомое расстояние.
	const sql = `
		WITH RECURSIVE bfs AS (
			SELECT $1::bigint AS node, 0 AS dist
			UNION
			SELECT CASE WHEN c.a_id = b.node THEN c.b_id ELSE c.a_id END, b.dist + 1
			FROM bfs b
			JOIN collaborates c ON (c.a_id = b.node OR c.b_id = b.node)
			WHERE b.dist < $3
		)
		SELECT COALESCE(MIN(dist), -1) FROM bfs WHERE node = $2`
	var d int
	err := q.pool.QueryRow(ctx, sql, aID, bID, maxLen).Scan(&d)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func (q *baselineQueries) RingMembers(ctx context.Context, length int) ([]int64, error) {
	// Простые кольца ровно из length рёбер: строим простые пути из length узлов,
	// где стартовый узел — минимальный (остальные строго больше, что канонизирует
	// цикл), узлы не повторяются; затем требуем замыкающее ребро в старт.
	const sql = `
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
			WHERE w.len < $1 - 1
			  AND e.nb > w.start
			  AND NOT e.nb = ANY(w.path)
		)
		SELECT DISTINCT unnest(path) AS id
		FROM walk w
		WHERE w.len = $1 - 1
		  AND EXISTS (
			SELECT 1 FROM collaborates c
			WHERE (c.a_id = w.cur AND c.b_id = w.start)
			   OR (c.b_id = w.cur AND c.a_id = w.start)
		  )`
	ids, err := q.queryInts(ctx, sql, length)
	if err != nil {
		return nil, err
	}
	return sortedUnique(ids), nil
}
