package main

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
)

// Params задаёт масштаб и форму генерируемого графа. Один и тот же seed даёт
// побайтово одинаковый граф — на этом держится воспроизводимость замеров.
type Params struct {
	Users     int
	Teams     int
	Roles     int
	Resources int
	Services  int
	Packages  int
	Fanout    int // средняя степень исходящих рёбер там, где это применимо
	Depth     int // длина гарантированной цепочки COLLABORATES (для shortest path)
	Seed      int64
}

// HasRoleEdge — ребро «у субъекта есть роль»; субъектом может быть пользователь или команда.
type HasRoleEdge struct {
	SubjectID   int64
	SubjectKind string // "user" | "team"
	RoleID      int64
}

// DependsEdge — ребро зависимости; kind различает граф сервисов и граф пакетов.
type DependsEdge struct {
	SrcID int64
	DstID int64
	Kind  string // "service" | "package"
}

// Pair — направленное ребро между двумя узлами.
type Pair struct{ From, To int64 }

// Graph — сгенерированный граф в нейтральном виде, из которого рендерятся все три формата.
type Graph struct {
	P Params

	// диапазоны id по типам узлов (id глобально уникальны)
	UserLo, UserHi int64
	TeamLo, TeamHi int64
	RoleLo, RoleHi int64
	ResLo, ResHi   int64
	SvcLo, SvcHi   int64
	PkgLo, PkgHi   int64

	HubResource int64 // ресурс с намеренно высоким fan-out

	MemberOf     []Pair        // user -> team
	HasRole      []HasRoleEdge // subject -> role
	Grants       []Pair        // role -> resource
	Collaborates []Pair        // user <-> user (каноническая форма From < To)
	DependsOn    []DependsEdge // service->service / package->package
	Owns         []Pair        // team -> service
	Interacts    []Pair        // user -> resource
}

// ids возвращает срез последовательных идентификаторов [lo..hi].
func ids(lo, hi int64) []int64 {
	if hi < lo {
		return nil
	}
	out := make([]int64, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

// Generate строит детерминированный граф по параметрам.
func Generate(p Params) *Graph {
	rng := rand.New(rand.NewSource(p.Seed))
	g := &Graph{P: p}

	// раскладка id: единый счётчик по всем типам узлов
	var c int64
	alloc := func(n int) (lo, hi int64) {
		lo = c + 1
		c += int64(n)
		hi = c
		return
	}
	g.UserLo, g.UserHi = alloc(p.Users)
	g.TeamLo, g.TeamHi = alloc(p.Teams)
	g.RoleLo, g.RoleHi = alloc(p.Roles)
	g.ResLo, g.ResHi = alloc(p.Resources)
	g.SvcLo, g.SvcHi = alloc(p.Services)
	g.PkgLo, g.PkgHi = alloc(p.Packages)
	g.HubResource = g.ResLo // первый ресурс — хаб

	users := ids(g.UserLo, g.UserHi)
	teams := ids(g.TeamLo, g.TeamHi)
	roles := ids(g.RoleLo, g.RoleHi)
	resources := ids(g.ResLo, g.ResHi)
	services := ids(g.SvcLo, g.SvcHi)
	packages := ids(g.PkgLo, g.PkgHi)

	pick := func(pool []int64, k int) []int64 {
		if k >= len(pool) {
			out := make([]int64, len(pool))
			copy(out, pool)
			return out
		}
		// частичный Фишер–Йейтс на копии индексов
		idx := make([]int64, len(pool))
		copy(idx, pool)
		for i := 0; i < k; i++ {
			j := i + rng.Intn(len(idx)-i)
			idx[i], idx[j] = idx[j], idx[i]
		}
		return append([]int64(nil), idx[:k]...)
	}

	// ── member_of: каждый пользователь в 1–2 командах ──────────────────────────
	if len(teams) > 0 {
		for _, u := range users {
			for _, t := range pick(teams, 1+rng.Intn(2)) {
				g.MemberOf = append(g.MemberOf, Pair{u, t})
			}
		}
	}

	// ── has_role: у команды 1–3 роли; ~20% пользователей — прямая роль ─────────
	if len(roles) > 0 {
		for _, t := range teams {
			for _, r := range pick(roles, 1+rng.Intn(3)) {
				g.HasRole = append(g.HasRole, HasRoleEdge{t, "team", r})
			}
		}
		for _, u := range users {
			if rng.Float64() < 0.2 {
				r := roles[rng.Intn(len(roles))]
				g.HasRole = append(g.HasRole, HasRoleEdge{u, "user", r})
			}
		}
	}

	// ── grants: роль даёт доступ к fanout ресурсам; половина ролей — к хабу ────
	if len(resources) > 0 {
		for _, r := range roles {
			seen := map[int64]bool{}
			for _, res := range pick(resources, g.P.Fanout) {
				if !seen[res] {
					seen[res] = true
					g.Grants = append(g.Grants, Pair{r, res})
				}
			}
			if rng.Float64() < 0.5 && !seen[g.HubResource] {
				g.Grants = append(g.Grants, Pair{r, g.HubResource})
			}
		}
	}

	// ── collaborates: гарантированная длинная цепочка + случайные рёбра + кольца ─
	addCollab := func(a, b int64) {
		if a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		g.Collaborates = append(g.Collaborates, Pair{a, b})
	}
	if len(users) >= 2 {
		// гарантированная простая цепочка длины Depth: u0-u1-...-uDepth
		chainLen := g.P.Depth
		if chainLen > len(users)-1 {
			chainLen = len(users) - 1
		}
		for i := 0; i < chainLen; i++ {
			addCollab(users[i], users[i+1])
		}
		// случайные сотрудничества
		for _, u := range users {
			for j := 0; j < 1+rng.Intn(g.P.Fanout); j++ {
				v := users[rng.Intn(len(users))]
				addCollab(u, v)
			}
		}
		// намеренные fraud-кольца длины 5
		ringLen := 5
		if ringLen <= len(users) {
			for ring := 0; ring < 3; ring++ {
				start := rng.Intn(len(users) - ringLen + 1)
				for i := 0; i < ringLen; i++ {
					addCollab(users[start+i], users[start+(i+1)%ringLen])
				}
			}
		}
	}
	g.Collaborates = dedupPairs(g.Collaborates)

	// ── depends_on (сервисы): DAG рёбрами high→low + намеренный цикл ────────────
	buildDeps := func(nodes []int64, kind string) {
		for i := 1; i < len(nodes); i++ {
			maxBack := i
			if maxBack > g.P.Fanout {
				maxBack = g.P.Fanout
			}
			k := 1 + rng.Intn(maxBack)
			seen := map[int64]bool{}
			for j := 0; j < k; j++ {
				dst := nodes[rng.Intn(i)] // строго меньший индекс → ацикличность
				if !seen[dst] {
					seen[dst] = true
					g.DependsOn = append(g.DependsOn, DependsEdge{nodes[i], dst, kind})
				}
			}
		}
		// намеренный цикл среди первых трёх узлов: n2->n1->n0->n2
		if len(nodes) >= 3 {
			g.DependsOn = append(g.DependsOn,
				DependsEdge{nodes[2], nodes[1], kind},
				DependsEdge{nodes[1], nodes[0], kind},
				DependsEdge{nodes[0], nodes[2], kind}, // замыкающее ребро
			)
		}
	}
	buildDeps(services, "service")
	buildDeps(packages, "package")
	g.DependsOn = dedupDeps(g.DependsOn)

	// ── owns: команда владеет 1–3 сервисами ───────────────────────────────────
	if len(services) > 0 {
		for _, t := range teams {
			for _, s := range pick(services, 1+rng.Intn(3)) {
				g.Owns = append(g.Owns, Pair{t, s})
			}
		}
	}

	// ── interacts: пользователь взаимодействует с fanout ресурсами; хаб — часто ─
	if len(resources) > 0 {
		for _, u := range users {
			seen := map[int64]bool{}
			for _, res := range pick(resources, g.P.Fanout) {
				if !seen[res] {
					seen[res] = true
					g.Interacts = append(g.Interacts, Pair{u, res})
				}
			}
			if rng.Float64() < 0.4 && !seen[g.HubResource] {
				g.Interacts = append(g.Interacts, Pair{u, g.HubResource})
			}
		}
	}

	return g
}

func dedupPairs(in []Pair) []Pair {
	seen := map[Pair]bool{}
	out := in[:0]
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func dedupDeps(in []DependsEdge) []DependsEdge {
	type key struct {
		a, b int64
	}
	seen := map[key]bool{}
	out := in[:0]
	for _, e := range in {
		k := key{e.SrcID, e.DstID}
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// Checksum — стабильный хэш отсортированного списка всех рёбер. Одинаковый seed
// обязан давать одинаковый checksum; иначе генерация недетерминирована.
func (g *Graph) Checksum() uint64 {
	lines := make([]string, 0,
		len(g.MemberOf)+len(g.HasRole)+len(g.Grants)+len(g.Collaborates)+
			len(g.DependsOn)+len(g.Owns)+len(g.Interacts))
	for _, e := range g.MemberOf {
		lines = append(lines, fmt.Sprintf("MEMBER_OF:%d>%d", e.From, e.To))
	}
	for _, e := range g.HasRole {
		lines = append(lines, fmt.Sprintf("HAS_ROLE:%s:%d>%d", e.SubjectKind, e.SubjectID, e.RoleID))
	}
	for _, e := range g.Grants {
		lines = append(lines, fmt.Sprintf("GRANTS:%d>%d", e.From, e.To))
	}
	for _, e := range g.Collaborates {
		lines = append(lines, fmt.Sprintf("COLLAB:%d>%d", e.From, e.To))
	}
	for _, e := range g.DependsOn {
		lines = append(lines, fmt.Sprintf("DEPENDS:%s:%d>%d", e.Kind, e.SrcID, e.DstID))
	}
	for _, e := range g.Owns {
		lines = append(lines, fmt.Sprintf("OWNS:%d>%d", e.From, e.To))
	}
	for _, e := range g.Interacts {
		lines = append(lines, fmt.Sprintf("INTERACTS:%d>%d", e.From, e.To))
	}
	sort.Strings(lines)
	h := fnv.New64a()
	for _, l := range lines {
		_, _ = h.Write([]byte(l))
		_, _ = h.Write([]byte{'\n'})
	}
	return h.Sum64()
}
