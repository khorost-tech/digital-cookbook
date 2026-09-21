package main

import (
	"bufio"
	"fmt"
	"strings"
)

const batchSize = 500

// ── реляционный формат (psql, COPY) ──────────────────────────────────────────

// WriteRelational рендерит граф в SQL с COPY-блоками для honest baseline.
// Файл идемпотентен: сначала TRUNCATE, потом загрузка.
func (g *Graph) WriteRelational(w *bufio.Writer) {
	fmt.Fprintln(w, "-- сгенерировано dataset/generate — не редактировать вручную")
	fmt.Fprintln(w, "BEGIN;")
	fmt.Fprintln(w, "TRUNCATE interacts, owns, depends_on, collaborates, grants, has_role, member_of,")
	fmt.Fprintln(w, "         packages, services, resources, roles, teams, users;")

	copyNodes := func(table, prefix string, lo, hi int64) {
		if hi < lo {
			return
		}
		fmt.Fprintf(w, "COPY %s (id, name) FROM stdin;\n", table)
		for id := lo; id <= hi; id++ {
			fmt.Fprintf(w, "%d\t%s_%d\n", id, prefix, id)
		}
		fmt.Fprintln(w, `\.`)
	}
	copyNodes("users", "user", g.UserLo, g.UserHi)
	copyNodes("teams", "team", g.TeamLo, g.TeamHi)
	copyNodes("roles", "role", g.RoleLo, g.RoleHi)
	copyNodes("resources", "resource", g.ResLo, g.ResHi)
	copyNodes("services", "service", g.SvcLo, g.SvcHi)
	copyNodes("packages", "package", g.PkgLo, g.PkgHi)

	copyPairs := func(table, colA, colB string, pairs []Pair) {
		if len(pairs) == 0 {
			return
		}
		fmt.Fprintf(w, "COPY %s (%s, %s) FROM stdin;\n", table, colA, colB)
		for _, p := range pairs {
			fmt.Fprintf(w, "%d\t%d\n", p.From, p.To)
		}
		fmt.Fprintln(w, `\.`)
	}
	copyPairs("member_of", "user_id", "team_id", g.MemberOf)

	if len(g.HasRole) > 0 {
		fmt.Fprintln(w, "COPY has_role (subject_id, subject_kind, role_id) FROM stdin;")
		for _, e := range g.HasRole {
			fmt.Fprintf(w, "%d\t%s\t%d\n", e.SubjectID, e.SubjectKind, e.RoleID)
		}
		fmt.Fprintln(w, `\.`)
	}

	copyPairs("grants", "role_id", "resource_id", g.Grants)
	copyPairs("collaborates", "a_id", "b_id", g.Collaborates)

	if len(g.DependsOn) > 0 {
		fmt.Fprintln(w, "COPY depends_on (src_id, dst_id, kind) FROM stdin;")
		for _, e := range g.DependsOn {
			fmt.Fprintf(w, "%d\t%d\t%s\n", e.SrcID, e.DstID, e.Kind)
		}
		fmt.Fprintln(w, `\.`)
	}

	copyPairs("owns", "team_id", "service_id", g.Owns)
	copyPairs("interacts", "user_id", "resource_id", g.Interacts)

	fmt.Fprintln(w, "COMMIT;")
}

// ── Neo4j (cypher-shell) ─────────────────────────────────────────────────────

// WriteNeo4jCypher рендерит граф в Cypher: сброс, ограничения уникальности,
// узлы диапазонами, рёбра батчами UNWIND ... MATCH ... CREATE.
func (g *Graph) WriteNeo4jCypher(w *bufio.Writer) {
	fmt.Fprintln(w, "// сгенерировано dataset/generate — не редактировать вручную")
	// сброс графа перед загрузкой (одной транзакцией — рассчитано на масштаб стенда, десятки тыс. узлов)
	fmt.Fprintln(w, "MATCH (n) DETACH DELETE n;")

	for _, lbl := range []string{"User", "Team", "Role", "Resource", "Service", "Package"} {
		fmt.Fprintf(w, "CREATE CONSTRAINT %s_id IF NOT EXISTS FOR (n:%s) REQUIRE n.id IS UNIQUE;\n",
			strings.ToLower(lbl), lbl)
	}

	nodeRange := func(lbl string, lo, hi int64) {
		if hi < lo {
			return
		}
		fmt.Fprintf(w, "UNWIND range(%d,%d) AS id CREATE (:%s {id:id});\n", lo, hi, lbl)
	}
	nodeRange("User", g.UserLo, g.UserHi)
	nodeRange("Team", g.TeamLo, g.TeamHi)
	nodeRange("Role", g.RoleLo, g.RoleHi)
	nodeRange("Resource", g.ResLo, g.ResHi)
	nodeRange("Service", g.SvcLo, g.SvcHi)
	nodeRange("Package", g.PkgLo, g.PkgHi)

	edges := func(labA, labB, rel string, pairs []Pair) {
		for start := 0; start < len(pairs); start += batchSize {
			end := start + batchSize
			if end > len(pairs) {
				end = len(pairs)
			}
			var b strings.Builder
			b.WriteString("UNWIND [")
			for i, p := range pairs[start:end] {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "[%d,%d]", p.From, p.To)
			}
			b.WriteString("] AS e ")
			fmt.Fprintf(&b, "MATCH (a:%s {id:e[0]}),(x:%s {id:e[1]}) CREATE (a)-[:%s]->(x);", labA, labB, rel)
			fmt.Fprintln(w, b.String())
		}
	}

	edges("User", "Team", "MEMBER_OF", g.MemberOf)
	// has_role разбит по типу субъекта
	var hrUser, hrTeam []Pair
	for _, e := range g.HasRole {
		if e.SubjectKind == "user" {
			hrUser = append(hrUser, Pair{e.SubjectID, e.RoleID})
		} else {
			hrTeam = append(hrTeam, Pair{e.SubjectID, e.RoleID})
		}
	}
	edges("User", "Role", "HAS_ROLE", hrUser)
	edges("Team", "Role", "HAS_ROLE", hrTeam)
	edges("Role", "Resource", "GRANTS", g.Grants)
	edges("User", "User", "COLLABORATES", g.Collaborates)
	// depends_on разбит по kind
	var depSvc, depPkg []Pair
	for _, e := range g.DependsOn {
		if e.Kind == "service" {
			depSvc = append(depSvc, Pair{e.SrcID, e.DstID})
		} else {
			depPkg = append(depPkg, Pair{e.SrcID, e.DstID})
		}
	}
	edges("Service", "Service", "DEPENDS_ON", depSvc)
	edges("Package", "Package", "DEPENDS_ON", depPkg)
	edges("Team", "Service", "OWNS", g.Owns)
	edges("User", "Resource", "INTERACTS", g.Interacts)
}

// ── Apache AGE (psql) ────────────────────────────────────────────────────────

// WriteAGECypher рендерит граф для AGE: пересоздание графа, узлы, функциональные
// индексы по id, рёбра батчами. Тот же openCypher, что и для Neo4j, обёрнутый в SQL.
func (g *Graph) WriteAGECypher(w *bufio.Writer) {
	fmt.Fprintln(w, "-- сгенерировано dataset/generate — не редактировать вручную")
	fmt.Fprintln(w, "LOAD 'age';")
	fmt.Fprintln(w, `SET search_path = ag_catalog, "$user", public;`)
	fmt.Fprintln(w, `DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name='platform') THEN
    PERFORM ag_catalog.drop_graph('platform', true);
  END IF;
  PERFORM ag_catalog.create_graph('platform');
END $$;`)

	cypher := func(body string) {
		fmt.Fprintf(w, "SELECT * FROM cypher('platform', $$ %s $$) AS (v agtype);\n", body)
	}

	nodeRange := func(lbl string, lo, hi int64) {
		if hi < lo {
			return
		}
		cypher(fmt.Sprintf("UNWIND range(%d,%d) AS id CREATE (:%s {id: id})", lo, hi, lbl))
	}
	nodeRange("User", g.UserLo, g.UserHi)
	nodeRange("Team", g.TeamLo, g.TeamHi)
	nodeRange("Role", g.RoleLo, g.RoleHi)
	nodeRange("Resource", g.ResLo, g.ResHi)
	nodeRange("Service", g.SvcLo, g.SvcHi)
	nodeRange("Package", g.PkgLo, g.PkgHi)

	// функциональные индексы по id — иначе MATCH по id сканирует всю таблицу метки
	for _, lbl := range []string{"User", "Team", "Role", "Resource", "Service", "Package"} {
		fmt.Fprintf(w,
			`CREATE INDEX IF NOT EXISTS %s_id_idx ON platform."%s" USING btree (ag_catalog.agtype_access_operator(properties, '"id"'::agtype));`+"\n",
			strings.ToLower(lbl), lbl)
	}

	edges := func(labA, labB, rel string, pairs []Pair) {
		for start := 0; start < len(pairs); start += batchSize {
			end := start + batchSize
			if end > len(pairs) {
				end = len(pairs)
			}
			var lst strings.Builder
			lst.WriteByte('[')
			for i, p := range pairs[start:end] {
				if i > 0 {
					lst.WriteByte(',')
				}
				fmt.Fprintf(&lst, "[%d,%d]", p.From, p.To)
			}
			lst.WriteByte(']')
			body := fmt.Sprintf(
				"UNWIND %s AS e MATCH (a:%s), (x:%s) WHERE a.id = e[0] AND x.id = e[1] CREATE (a)-[:%s]->(x)",
				lst.String(), labA, labB, rel)
			cypher(body)
		}
	}

	edges("User", "Team", "MEMBER_OF", g.MemberOf)
	var hrUser, hrTeam []Pair
	for _, e := range g.HasRole {
		if e.SubjectKind == "user" {
			hrUser = append(hrUser, Pair{e.SubjectID, e.RoleID})
		} else {
			hrTeam = append(hrTeam, Pair{e.SubjectID, e.RoleID})
		}
	}
	edges("User", "Role", "HAS_ROLE", hrUser)
	edges("Team", "Role", "HAS_ROLE", hrTeam)
	edges("Role", "Resource", "GRANTS", g.Grants)
	edges("User", "User", "COLLABORATES", g.Collaborates)
	var depSvc, depPkg []Pair
	for _, e := range g.DependsOn {
		if e.Kind == "service" {
			depSvc = append(depSvc, Pair{e.SrcID, e.DstID})
		} else {
			depPkg = append(depPkg, Pair{e.SrcID, e.DstID})
		}
	}
	edges("Service", "Service", "DEPENDS_ON", depSvc)
	edges("Package", "Package", "DEPENDS_ON", depPkg)
	edges("Team", "Service", "OWNS", g.Owns)
	edges("User", "Resource", "INTERACTS", g.Interacts)
}
