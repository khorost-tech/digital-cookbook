// Package queries реализует одни и те же графовые вопросы на трёх углах стенда:
// нативном Neo4j, Apache AGE (граф поверх PostgreSQL) и honest baseline на
// рекурсивных CTE. Все методы возвращают множества id или скаляр — намеренно:
// это подмножество, которое одинаково выразимо во всех трёх движках. То, что
// умеет только Neo4j (shortestPath, nodes(path), list comprehension), в общий
// интерфейс не входит и разбирается в статье отдельно.
package queries

import (
	"context"
	"fmt"
	"os"
	"sort"
)

// GraphQueries — общий контракт трёх backend-ов. Каждый метод принимает context
// и уважает его отмену/таймаут.
type GraphQueries interface {
	// AccessibleResources — ресурсы, доступные пользователю через прямые роли и
	// через членство в командах (access graph: MEMBER_OF→HAS_ROLE→GRANTS).
	AccessibleResources(ctx context.Context, userID int64) ([]int64, error)
	// ImpactOf — сервисы, транзитивно зависящие от заданного (dependency impact),
	// в пределах maxDepth переходов.
	ImpactOf(ctx context.Context, serviceID int64, maxDepth int) ([]int64, error)
	// CyclicServices — сервисы, лежащие на цикле DEPENDS_ON (в пределах maxLen).
	CyclicServices(ctx context.Context, maxLen int) ([]int64, error)
	// Distance — длина кратчайшей цепочки COLLABORATES между двумя пользователями
	// в пределах maxLen; -1, если пути нет.
	Distance(ctx context.Context, aID, bID int64, maxLen int) (int, error)
	// RingMembers — пользователи на простом кольце COLLABORATES ровно заданной длины.
	RingMembers(ctx context.Context, length int) ([]int64, error)

	Backend() string
	Close() error
}

// Config — точки подключения к обоим движкам.
type Config struct {
	Neo4jURI  string
	Neo4jUser string
	Neo4jPass string
	PgDSN     string
}

// ConfigFromEnv читает конфигурацию из окружения с дефолтами под локальный запуск.
func ConfigFromEnv() Config {
	return Config{
		Neo4jURI:  env("NEO4J_URI", "bolt://localhost:7687"),
		Neo4jUser: env("NEO4J_USER", "neo4j"),
		Neo4jPass: env("NEO4J_PASS", "graphlab-pass"),
		PgDSN:     env("PG_DSN", "postgres://graph:graph@localhost:5432/graphlab"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// New создаёт backend по имени: "neo4j" | "age" | "baseline".
func New(ctx context.Context, backend string, cfg Config) (GraphQueries, error) {
	switch backend {
	case "neo4j":
		return newNeo4j(ctx, cfg)
	case "age":
		return newAGE(ctx, cfg)
	case "baseline":
		return newBaseline(ctx, cfg)
	default:
		return nil, fmt.Errorf("неизвестный backend %q (ожидается neo4j|age|baseline)", backend)
	}
}

// sortedUnique нормализует множество id: сортировка + дедупликация. Приводит
// ответы всех backend-ов к сравнимому виду.
func sortedUnique(in []int64) []int64 {
	if len(in) == 0 {
		return []int64{}
	}
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
