package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	var (
		p       Params
		outDir  string
		verbose bool
	)
	flag.IntVar(&p.Users, "users", 2000, "число пользователей")
	flag.IntVar(&p.Teams, "teams", 150, "число команд")
	flag.IntVar(&p.Roles, "roles", 200, "число ролей")
	flag.IntVar(&p.Resources, "resources", 800, "число ресурсов")
	flag.IntVar(&p.Services, "services", 300, "число сервисов")
	flag.IntVar(&p.Packages, "packages", 1200, "число пакетов")
	flag.IntVar(&p.Fanout, "fanout", 6, "средний исходящий fan-out")
	flag.IntVar(&p.Depth, "depth", 8, "длина гарантированной цепочки COLLABORATES")
	flag.Int64Var(&p.Seed, "seed", 42, "seed RNG (фиксирует граф)")
	flag.StringVar(&outDir, "out", "./out", "каталог для сгенерированных файлов")
	flag.BoolVar(&verbose, "v", false, "печатать сводку")
	flag.Parse()

	g := Generate(p)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	write := func(name string, fn func(*bufio.Writer)) {
		path := filepath.Join(outDir, name)
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "create:", err)
			os.Exit(1)
		}
		defer f.Close()
		bw := bufio.NewWriter(f)
		fn(bw)
		if err := bw.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "flush:", err)
			os.Exit(1)
		}
	}

	write("relational.sql", g.WriteRelational)
	write("neo4j.cypher", g.WriteNeo4jCypher)
	write("age.sql", g.WriteAGECypher)

	fmt.Printf("seed=%d checksum=%d\n", p.Seed, g.Checksum())
	fmt.Printf("узлы: users=%d teams=%d roles=%d resources=%d services=%d packages=%d\n",
		p.Users, p.Teams, p.Roles, p.Resources, p.Services, p.Packages)
	fmt.Printf("рёбра: member_of=%d has_role=%d grants=%d collaborates=%d depends_on=%d owns=%d interacts=%d\n",
		len(g.MemberOf), len(g.HasRole), len(g.Grants), len(g.Collaborates),
		len(g.DependsOn), len(g.Owns), len(g.Interacts))
	fmt.Printf("файлы в %s: relational.sql, neo4j.cypher, age.sql\n", outDir)
}
