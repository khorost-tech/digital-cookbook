package queries

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Интеграционный тест: грузит крошечную фикстуру с руками посчитанными ответами
// во все три угла и проверяет, что neo4j, age и baseline дают один и тот же результат.
//
// ВНИМАНИЕ: тест сбрасывает содержимое всех трёх хранилищ. Запускать на стенде,
// не на данных, которые нужны. Требует поднятых neo4j и postgres (docker compose up -d).

var fixtureCollab = [][2]int64{{1, 2}, {2, 3}, {5, 6}, {6, 7}, {7, 8}, {5, 8}}
var fixtureDeps = []struct {
	a, b int64
}{{40, 41}, {41, 42}, {43, 44}, {44, 45}, {45, 43}}

func cfg() Config { return ConfigFromEnv() }

func mustLoadFixtures(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	loadRelationalAndAGE(t, ctx)
	loadNeo4j(t, ctx)
}

func loadRelationalAndAGE(t *testing.T, ctx context.Context) {
	t.Helper()
	conn, err := pgx.Connect(ctx, cfg().PgDSN)
	if err != nil {
		t.Fatalf("pg connect: %v", err)
	}
	defer conn.Close(ctx)

	// реляционная схема из единственного источника
	schema, err := os.ReadFile("../../../dataset/schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	for _, stmt := range splitSQL(string(schema)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema stmt: %v\n%s", err, stmt)
		}
	}

	exec := func(sql string, args ...any) {
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	for _, id := range []int64{1, 2, 3, 5, 6, 7, 8} {
		exec("INSERT INTO users(id,name) VALUES($1,$2)", id, "u")
	}
	exec("INSERT INTO teams(id,name) VALUES(10,'t')")
	exec("INSERT INTO roles(id,name) VALUES(20,'r')")
	exec("INSERT INTO resources(id,name) VALUES(30,'a'),(31,'b')")
	for _, id := range []int64{40, 41, 42, 43, 44, 45} {
		exec("INSERT INTO services(id,name) VALUES($1,'s')", id)
	}
	exec("INSERT INTO member_of(user_id,team_id) VALUES(1,10)")
	exec("INSERT INTO has_role(subject_id,subject_kind,role_id) VALUES(10,'team',20),(2,'user',20)")
	exec("INSERT INTO grants(role_id,resource_id) VALUES(20,30),(20,31)")
	for _, e := range fixtureCollab {
		exec("INSERT INTO collaborates(a_id,b_id) VALUES($1,$2)", e[0], e[1])
	}
	for _, d := range fixtureDeps {
		exec("INSERT INTO depends_on(src_id,dst_id,kind) VALUES($1,$2,'service')", d.a, d.b)
	}

	// AGE: пересоздать граф и залить ту же фикстуру
	if _, err := conn.Exec(ctx, "LOAD 'age'"); err != nil {
		t.Fatalf("load age: %v", err)
	}
	if _, err := conn.Exec(ctx, `SET search_path = ag_catalog, "$user", public`); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	ageExec := func(sql string) {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("age exec %q: %v", sql, err)
		}
	}
	ageExec(`DO $$ BEGIN
	  IF EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name='platform') THEN
	    PERFORM ag_catalog.drop_graph('platform', true);
	  END IF;
	  PERFORM ag_catalog.create_graph('platform');
	END $$;`)
	cy := func(body string) {
		ageExec("SELECT * FROM cypher('platform', $$ " + body + " $$) AS (v agtype);")
	}
	cy("UNWIND [1,2,3,5,6,7,8] AS id CREATE (:User {id: id})")
	cy("CREATE (:Team {id: 10})")
	cy("CREATE (:Role {id: 20})")
	cy("UNWIND [30,31] AS id CREATE (:Resource {id: id})")
	cy("UNWIND [40,41,42,43,44,45] AS id CREATE (:Service {id: id})")
	cy("MATCH (u:User),(t:Team) WHERE u.id=1 AND t.id=10 CREATE (u)-[:MEMBER_OF]->(t)")
	cy("MATCH (t:Team),(r:Role) WHERE t.id=10 AND r.id=20 CREATE (t)-[:HAS_ROLE]->(r)")
	cy("MATCH (u:User),(r:Role) WHERE u.id=2 AND r.id=20 CREATE (u)-[:HAS_ROLE]->(r)")
	cy("MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=30 CREATE (r)-[:GRANTS]->(x)")
	cy("MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=31 CREATE (r)-[:GRANTS]->(x)")
	for _, d := range fixtureDeps {
		cy(sprintfEdge("Service", "Service", "DEPENDS_ON", d.a, d.b))
	}
	for _, e := range fixtureCollab {
		cy(sprintfEdge("User", "User", "COLLABORATES", e[0], e[1]))
	}
}

// splitSQL срезает построчные комментарии (-- …) и делит скрипт на выражения по ';'.
// schema.sql не содержит строковых литералов с '--' или ';', поэтому этого достаточно.
func splitSQL(script string) []string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, stmt := range strings.Split(b.String(), ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func sprintfEdge(la, lb, rel string, a, b int64) string {
	return "MATCH (x:" + la + "),(y:" + lb + ") WHERE x.id=" + strconv.FormatInt(a, 10) +
		" AND y.id=" + strconv.FormatInt(b, 10) + " CREATE (x)-[:" + rel + "]->(y)"
}

func loadNeo4j(t *testing.T, ctx context.Context) {
	t.Helper()
	c := cfg()
	d, err := neo4j.NewDriverWithContext(c.Neo4jURI, neo4j.BasicAuth(c.Neo4jUser, c.Neo4jPass, ""))
	if err != nil {
		t.Fatalf("neo4j driver: %v", err)
	}
	defer d.Close(ctx)
	stmts := []string{
		"MATCH (n) DETACH DELETE n",
		"UNWIND [1,2,3,5,6,7,8] AS id CREATE (:User {id:id})",
		"CREATE (:Team {id:10})",
		"CREATE (:Role {id:20})",
		"UNWIND [30,31] AS id CREATE (:Resource {id:id})",
		"UNWIND [40,41,42,43,44,45] AS id CREATE (:Service {id:id})",
		"MATCH (u:User{id:1}),(t:Team{id:10}) CREATE (u)-[:MEMBER_OF]->(t)",
		"MATCH (t:Team{id:10}),(r:Role{id:20}) CREATE (t)-[:HAS_ROLE]->(r)",
		"MATCH (u:User{id:2}),(r:Role{id:20}) CREATE (u)-[:HAS_ROLE]->(r)",
		"MATCH (r:Role{id:20}),(x:Resource{id:30}) CREATE (r)-[:GRANTS]->(x)",
		"MATCH (r:Role{id:20}),(x:Resource{id:31}) CREATE (r)-[:GRANTS]->(x)",
	}
	for _, d := range fixtureDeps {
		stmts = append(stmts, sprintfEdge("Service", "Service", "DEPENDS_ON", d.a, d.b))
	}
	for _, e := range fixtureCollab {
		stmts = append(stmts, sprintfEdge("User", "User", "COLLABORATES", e[0], e[1]))
	}
	for _, s := range stmts {
		if _, err := neo4j.ExecuteQuery(ctx, d, s, nil, neo4j.EagerResultTransformer); err != nil {
			t.Fatalf("neo4j stmt %q: %v", s, err)
		}
	}
}

func TestBackendsAgree(t *testing.T) {
	mustLoadFixtures(t)
	ctx := context.Background()

	for _, backend := range []string{"neo4j", "age", "baseline"} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			q, err := New(ctx, backend, cfg())
			if err != nil {
				t.Fatalf("New(%s): %v", backend, err)
			}
			defer q.Close()

			checkInts := func(name string, got []int64, err error, want []int64) {
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %v, want %v", name, got, want)
				}
			}

			r1, err := q.AccessibleResources(ctx, 1)
			checkInts("AccessibleResources(1)", r1, err, []int64{30, 31})
			r2, err := q.AccessibleResources(ctx, 2)
			checkInts("AccessibleResources(2)", r2, err, []int64{30, 31})
			r3, err := q.AccessibleResources(ctx, 3)
			checkInts("AccessibleResources(3)", r3, err, []int64{})

			i42, err := q.ImpactOf(ctx, 42, 10)
			checkInts("ImpactOf(42)", i42, err, []int64{40, 41})
			i41, err := q.ImpactOf(ctx, 41, 10)
			checkInts("ImpactOf(41)", i41, err, []int64{40})

			cyc, err := q.CyclicServices(ctx, 10)
			checkInts("CyclicServices", cyc, err, []int64{43, 44, 45})

			ring, err := q.RingMembers(ctx, 4)
			checkInts("RingMembers(4)", ring, err, []int64{5, 6, 7, 8})
			ring3, err := q.RingMembers(ctx, 3)
			checkInts("RingMembers(3)", ring3, err, []int64{})

			checkDist := func(name string, got int, err error, want int) {
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got != want {
					t.Errorf("%s = %d, want %d", name, got, want)
				}
			}
			d13, err := q.Distance(ctx, 1, 3, 10)
			checkDist("Distance(1,3)", d13, err, 2)
			d57, err := q.Distance(ctx, 5, 7, 10)
			checkDist("Distance(5,7)", d57, err, 2)
			d58, err := q.Distance(ctx, 5, 8, 10)
			checkDist("Distance(5,8)", d58, err, 1)
			d17, err := q.Distance(ctx, 1, 7, 10)
			checkDist("Distance(1,7) unreachable", d17, err, -1)
		})
	}
}
