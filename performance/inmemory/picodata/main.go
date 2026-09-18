// Стенд Picodata: живой 3-инстансный кластер (2 репликасета, фактор
// репликации 2), Raft-топология, шардирование распределённым SQL,
// распределённая агрегация. Три сценария (-scenario=topology|sharding|sql),
// общий источник истины — PostgreSQL (ORIGIN_DSN), общий датасет из
// dataset/.
//
// Picodata поддерживает PG-протокол (--pg-listen), поэтому основной канал —
// pgx/v5 (тот же клиент, что и dataset/). Ровно ОДНА вещь не доступна через
// PG-протокол/SQL в 26.1.6 — состояние Raft-лидера кластерного governance
// (pico.raft_status(): leader_id, raft_state, term). Ни один "_pico_*"
// системный вид, доступный sbroad SQL, не содержит эту информацию (сверено
// живьём: "_pico_instance" не содержит leader-флага, "_pico_replicaset"
// содержит "current_master_name" — это МАСТЕР РЕПЛИКАСЕТА для vshard-записи,
// ДРУГОЕ понятие, не Raft-лидер кластера; box-уровневая "_raft_state" не
// экспортирована в sbroad; iproto CALL к "pico.raft_status" из внешнего
// клиента требует непокрытую документацией связку прав/auth-метода — попытка
// завела в тупик привилегий, не описанный ни в --help, ни в базовой
// документации). Поэтому raft-статус снимается ТЕМ ЖЕ каналом, что и Step 3
// брифа: `docker exec <container> picodata admin <sock>` с `\lua
// pico.raft_status()`. Это честный, не выдуманный канал — тот же самый,
// которым верифицируется топология в самом брифе.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- подключения ----------

func connectOrigin(ctx context.Context, dsn string) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgxpool (origin): %v", err)
	}
	return pool
}

// connectPicodata — PG-протокол Picodata (--pg-listen). Дефолтный порт
// ОТКЛОНЕНИЕ ОТ БРИФА: интерфейс задавал PICODATA_ADDR=127.0.0.1:3401 — это
// iproto-порт, не PG-wire. Сценарии подключаются через pgx (SQL, не
// iproto), поэтому переменная указывает на PG-порт 5441 (compose:
// picodata-1 pg-listen 5432 -> host 5441). Имя переменной сохранено
// (PICODATA_ADDR), значение по умолчанию — исправлено.
func connectPicodata(ctx context.Context, addr, user, password, db string) *pgxpool.Pool {
	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", user, password, addr, db)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgxpool (picodata): %v", err)
	}
	return pool
}

// ---------- raft-проба через admin console (docker exec) ----------

// raftProbe — снимок одного инстанса Picodata: собственное имя/репликасет,
// мнение О ГЛОБАЛЬНОМ Raft-лидере кластера (leader_id, свой raft_state,
// term) и, если таблица products уже существует, локальное число кортежей
// (используется сценарием sharding — тот же зонд, без второго docker exec).
type raftProbe struct {
	Container      string
	InstanceName   string
	ReplicasetName string
	LeaderID       int
	RaftState      string
	Term           int
	ProductsLen    int64
}

// Ответ admin-консоли — YAML-список из одного элемента (`\lua` возвращает
// одно значение), поэтому у ПЕРВОГО ключа строка выглядит как "- key: val"
// (дефис-маркер списка), у остальных — "  key: val" (два пробела отступа,
// без дефиса). `^[-\s]*` покрывает оба случая независимо от того, какой
// ключ попал первым (порядок ключей в Lua-таблице не гарантирован).
var probeFieldRe = map[string]*regexp.Regexp{
	"name":            regexp.MustCompile(`(?m)^[-\s]*name:\s*(\S+)\s*$`),
	"replicaset_name": regexp.MustCompile(`(?m)^[-\s]*replicaset_name:\s*(\S+)\s*$`),
	"leader_id":       regexp.MustCompile(`(?m)^[-\s]*leader_id:\s*(-?\d+)\s*$`),
	"raft_state":      regexp.MustCompile(`(?m)^[-\s]*raft_state:\s*(\S+)\s*$`),
	"term":            regexp.MustCompile(`(?m)^[-\s]*term:\s*(-?\d+)\s*$`),
	"products_len":    regexp.MustCompile(`(?m)^[-\s]*products_len:\s*(-?\d+)\s*$`),
}

// probeContainer — выполняет `docker exec <container> picodata admin
// <sock>` c одной lua-строкой на stdin и разбирает YAML-подобный ответ
// admin-консоли регулярками (ключ:значение, порядок ключей в ответе не
// гарантирован — pico.raft_status()/pico.instance_info() отдают Lua-таблицу,
// YAML-сериализация консоли не сохраняет порядок вставки).
func probeContainer(container, adminSock string) (raftProbe, error) {
	script := `local rs = pico.raft_status() local ii = pico.instance_info() local cnt = -1 pcall(function() cnt = box.space.products:len() end) return {name=ii.name, replicaset_name=ii.replicaset_name, leader_id=rs.leader_id, raft_state=rs.raft_state, term=rs.term, products_len=cnt}`

	cmd := exec.Command("docker", "exec", "-i", container, "picodata", "admin", adminSock)
	cmd.Stdin = strings.NewReader("\\lua\n" + script + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return raftProbe{}, fmt.Errorf("docker exec %s picodata admin: %v\nout=%s", container, err, out)
	}

	text := string(out)
	get := func(field string) (string, bool) {
		m := probeFieldRe[field].FindStringSubmatch(text)
		if m == nil {
			return "", false
		}
		return m[1], true
	}

	p := raftProbe{Container: container}
	if v, ok := get("name"); ok {
		p.InstanceName = v
	} else {
		return raftProbe{}, fmt.Errorf("не удалось разобрать 'name' в ответе %s:\n%s", container, text)
	}
	if v, ok := get("replicaset_name"); ok {
		p.ReplicasetName = v
	}
	if v, ok := get("leader_id"); ok {
		p.LeaderID, _ = strconv.Atoi(v)
	} else {
		return raftProbe{}, fmt.Errorf("не удалось разобрать 'leader_id' в ответе %s:\n%s", container, text)
	}
	if v, ok := get("raft_state"); ok {
		p.RaftState = v
	}
	if v, ok := get("term"); ok {
		p.Term, _ = strconv.Atoi(v)
	}
	if v, ok := get("products_len"); ok {
		n, _ := strconv.ParseInt(v, 10, 64)
		p.ProductsLen = n
	}
	return p, nil
}

func probeAll(containers []string, adminSock string) []raftProbe {
	out := make([]raftProbe, 0, len(containers))
	for _, c := range containers {
		p, err := probeContainer(c, adminSock)
		if err != nil {
			log.Fatalf("проба raft-статуса на %s: %v", c, err)
		}
		out = append(out, p)
	}
	return out
}

// ---------- сценарий topology ----------

type picoInstance struct {
	Name           string
	UUID           string
	RaftID         int
	ReplicasetName string
	ReplicasetUUID string
	CurrentState   string
	TargetState    string
	Tier           string
}

func loadPicoInstances(ctx context.Context, pool *pgxpool.Pool) []picoInstance {
	rows, err := pool.Query(ctx, `SELECT "name","uuid","raft_id","replicaset_name","replicaset_uuid","current_state","target_state","tier" FROM "_pico_instance" ORDER BY "raft_id"`)
	if err != nil {
		log.Fatalf(`SELECT "_pico_instance": %v`, err)
	}
	defer rows.Close()
	var out []picoInstance
	for rows.Next() {
		var i picoInstance
		if err := rows.Scan(&i.Name, &i.UUID, &i.RaftID, &i.ReplicasetName, &i.ReplicasetUUID, &i.CurrentState, &i.TargetState, &i.Tier); err != nil {
			log.Fatalf("scan _pico_instance: %v", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("_pico_instance rows: %v", err)
	}
	return out
}

type picoReplicaset struct {
	Name          string
	UUID          string
	CurrentMaster string
	Tier          string
	Weight        float64
	State         string
}

func loadPicoReplicasets(ctx context.Context, pool *pgxpool.Pool) []picoReplicaset {
	rows, err := pool.Query(ctx, `SELECT "name","uuid","current_master_name","tier","weight","state" FROM "_pico_replicaset" ORDER BY "name"`)
	if err != nil {
		log.Fatalf(`SELECT "_pico_replicaset": %v`, err)
	}
	defer rows.Close()
	var out []picoReplicaset
	for rows.Next() {
		var r picoReplicaset
		if err := rows.Scan(&r.Name, &r.UUID, &r.CurrentMaster, &r.Tier, &r.Weight, &r.State); err != nil {
			log.Fatalf("scan _pico_replicaset: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("_pico_replicaset rows: %v", err)
	}
	return out
}

func scenarioTopology(ctx context.Context, pico *pgxpool.Pool, containers []string, adminSock string) {
	instances := loadPicoInstances(ctx, pico)
	fmt.Printf("topology: \"_pico_instance\" содержит %d инстанс(ов)\n", len(instances))
	for _, i := range instances {
		fmt.Printf("  raft_id=%d name=%s replicaset=%s current_state=%s tier=%s\n",
			i.RaftID, i.Name, i.ReplicasetName, i.CurrentState, i.Tier)
	}
	// ОТКЛОНЕНИЕ ОТ БРИФА: бриф ожидал ровно 3 инстанса. Живой прогон показал,
	// что 3 инстанса при --init-replication-factor 2 структурно НЕ дают
	// реального шардирования — один репликасет остаётся "хвостовым" (1 член,
	// weight=0, state=not-ready) и НАВСЕГДА исключён vshard'ом из
	// распределения бакетов (не задержка ребалансировки — это прямое
	// следствие механики vshard: репликасет получает бакеты только по
	// достижении заданного RF, известное поведение с 22.11.0, не сюрприз
	// версии 26.1.6). Узел, держащий роль vshard-мастера соответствующего
	// репликасета (см. Weight/CurrentMaster в picoReplicaset, "_pico_replicaset"),
	// сам логирует "The cluster is balanced ok" сразу после того, как все
	// 3000 бакетов ушли на единственный полный репликасет — тег лога
	// vshard.rebalancer/vshard.storage, НЕ governor_loop; узел не фиксирован
	// на picodata-1, роль мастера может достаться любому инстансу (уточнено
	// по независимому ревью, перепроверено живьём дважды — см. README,
	// "Стенд 4", отклонение 4). Добавлен четвёртый инстанс (picodata-4),
	// достраивающий второй репликасет до фактора репликации 2 — см.
	// compose/picodata.yml и README. Ассерт обновлён с 3 на 4, суть проверки
	// (кластер собрался ЦЕЛИКОМ, а не частично) — та же.
	if len(instances) != 4 {
		log.Fatalf("АССЕРТ: в кластере %d инстансов, ожидалось 4", len(instances))
	}

	replicasets := loadPicoReplicasets(ctx, pico)
	fmt.Printf("topology: \"_pico_replicaset\" содержит %d репликасет(ов)\n", len(replicasets))
	for _, r := range replicasets {
		n := 0
		for _, i := range instances {
			if i.ReplicasetName == r.Name {
				n++
			}
		}
		fmt.Printf("  replicaset=%s master(vshard)=%s weight=%.0f state=%s инстансов=%d\n", r.Name, r.CurrentMaster, r.Weight, r.State, n)
	}

	// Raft-лидер кластерного governance-raft НЕ виден через SQL (см. комментарий
	// в шапке файла) — снимаем через admin console на каждом из трёх
	// контейнеров и проверяем, что все три СОГЛАСНЫ, кто лидер, и что лидер
	// РОВНО один.
	probes := probeAll(containers, adminSock)
	fmt.Println("topology: raft-статус (docker exec + admin console, pico.raft_status() на каждом инстансе):")
	leaderIDs := map[int]bool{}
	leaders := 0
	for _, p := range probes {
		fmt.Printf("  container=%s instance=%s replicaset=%s leader_id=%d raft_state=%s term=%d\n",
			p.Container, p.InstanceName, p.ReplicasetName, p.LeaderID, p.RaftState, p.Term)
		leaderIDs[p.LeaderID] = true
		if p.RaftState == "Leader" {
			leaders++
		}
	}
	if len(leaderIDs) != 1 {
		log.Fatalf("АССЕРТ: инстансы расходятся во мнении о leader_id: %v — расщепление мозга или гонка", leaderIDs)
	}
	// Ровно один лидер Raft.
	if leaders != 1 {
		log.Fatalf("АССЕРТ: лидеров %d, ожидался 1", leaders)
	}
	var leaderName string
	for _, p := range probes {
		if p.RaftState == "Leader" {
			leaderName = p.InstanceName
		}
	}
	fmt.Printf("topology: единственный Raft-лидер кластера — %s (leader_id=%d), все %d инстанса согласны\n",
		leaderName, probes[0].LeaderID, len(probes))
}

// ---------- сценарий sharding ----------

type pgProduct struct {
	ID         int64
	SKU        string
	Title      string
	PriceCents int64
	Category   string
}

func loadProductsFromPG(ctx context.Context, pool *pgxpool.Pool) []pgProduct {
	rows, err := pool.Query(ctx, `SELECT id, sku, title, price_cents, attrs->>'category' AS category FROM products ORDER BY id`)
	if err != nil {
		log.Fatalf("чтение products из PG: %v", err)
	}
	defer rows.Close()
	var out []pgProduct
	for rows.Next() {
		var p pgProduct
		if err := rows.Scan(&p.ID, &p.SKU, &p.Title, &p.PriceCents, &p.Category); err != nil {
			log.Fatalf("scan products (PG): %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("products rows (PG): %v", err)
	}
	return out
}

// createShardedTable — DDL из брифа подтверждён работающим дословно на
// 26.1.6 живым прогоном (`CREATE TABLE ... USING memtx DISTRIBUTED BY (id)`),
// единственное отклонение — явный DROP TABLE IF EXISTS перед созданием для
// идемпотентности повторного прогона сценария.
// raiseVDBEOpcodeLimit — НАХОДКА живого прогона, не в брифе: дефолт
// "sql_vdbe_opcode_max" (кластерный тюнабл, "_pico_db_config") = 45000.
// Распределённая агрегация (GROUP BY category) сценария sql на полном
// датасете (200k строк, 2 репликасета) упирается в этот потолок ("Reached
// a limit on max executed vdbe opcodes. Limit: 45000").
//
// ПРИЧИНА — установлена ЧАСТИЧНО. Доказано: порог зависит от числа строк,
// попадающих в GROUP BY, а не от размера таблицы. НЕ доказан конкретный
// VDBE-механизм: EXPLAIN даёт текстуально идентичный план для проходящего и
// падающего вариантов, профилировщика опкодов нет. Это точно НЕ "полный скан
// без индекса по category" (более ранняя версия комментария утверждала так —
// опровергнуто независимым ревью и переподтверждено самостоятельно).
// Бисекция по числу строк, реально попадающих в GROUP BY (`WHERE id < N`,
// БЕЗ индекса на category): id<6500 — проходит, id<7000 — падает (оба
// исхода воспроизведены дважды, детерминированно). Порог не зависит от
// размера таблицы (200000 строк) — только от числа строк, которые реально
// агрегируются: похоже на по-строчную стоимость VDBE при локальной
// группировке на каждом репликасете и последующем merge-motion между ними,
// а не на характер скана.
//
// CREATE INDEX ON products(category) НЕ помогает и не является фиксом —
// проверено живьём: С индексом тот же запрос падает уже на 9 строках
// (WHERE id<10, лимит 45000), а БЕЗ индекса те же 9 строк проходят.
// Индекс здесь не снижает стоимость GROUP BY, а увеличивает её.
//
// Бисекция итогового лимита (полный датасет): 45000 и 500000 — падают,
// 1000000 — падает, 2000000 — проходит. Здесь выставлен 5000000 — запас
// 2.5x над найденным порогом, не впритык. "ALTER SYSTEM SET" работает и
// через PG-протокол (не только через admin-консоль), поэтому сценарий
// bootstrap-ит это сам, без отдельного docker exec шага.
func raiseVDBEOpcodeLimit(ctx context.Context, pico *pgxpool.Pool) {
	if _, err := pico.Exec(ctx, `ALTER SYSTEM SET sql_vdbe_opcode_max = 5000000`); err != nil {
		log.Fatalf("ALTER SYSTEM SET sql_vdbe_opcode_max: %v", err)
	}
}

func createShardedTable(ctx context.Context, pico *pgxpool.Pool) {
	raiseVDBEOpcodeLimit(ctx, pico)
	if _, err := pico.Exec(ctx, `DROP TABLE IF EXISTS products`); err != nil {
		log.Fatalf("DROP TABLE products: %v", err)
	}
	const ddl = `CREATE TABLE products (
  id INTEGER NOT NULL,
  sku TEXT NOT NULL,
  title TEXT NOT NULL,
  price_cents INTEGER NOT NULL,
  category TEXT NOT NULL,
  PRIMARY KEY (id)
) USING memtx DISTRIBUTED BY (id)`
	if _, err := pico.Exec(ctx, ddl); err != nil {
		log.Fatalf("CREATE TABLE products (sharded): %v", err)
	}
}

// loadIntoPicodata — многострочный INSERT пачками через распределённый SQL
// router (picodata-1); каждая строка маршрутизируется на свой bucket/
// репликасет по hash(id) прозрачно для клиента.
func loadIntoPicodata(ctx context.Context, pico *pgxpool.Pool, rows []pgProduct) {
	const batch = 500
	for i := 0; i < len(rows); i += batch {
		end := i + batch
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[i:end]
		var sb strings.Builder
		sb.WriteString("INSERT INTO products (id, sku, title, price_cents, category) VALUES ")
		args := make([]any, 0, len(chunk)*5)
		for j, p := range chunk {
			if j > 0 {
				sb.WriteString(",")
			}
			base := j * 5
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5)
			args = append(args, p.ID, p.SKU, p.Title, p.PriceCents, p.Category)
		}
		if _, err := pico.Exec(ctx, sb.String(), args...); err != nil {
			log.Fatalf("вставка строк [%d:%d]: %v", i, end, err)
		}
		if (i/batch)%40 == 0 {
			fmt.Printf("sharding: залито %d/%d строк\n", end, len(rows))
		}
	}
	fmt.Printf("sharding: залито %d/%d строк\n", len(rows), len(rows))
}

func scenarioSharding(ctx context.Context, origin, pico *pgxpool.Pool, containers []string, adminSock string) {
	rows := loadProductsFromPG(ctx, origin)
	fmt.Printf("sharding: прочитано из PG products=%d\n", len(rows))

	createShardedTable(ctx, pico)
	loadIntoPicodata(ctx, pico, rows)

	var picoCount int64
	if err := pico.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&picoCount); err != nil {
		log.Fatalf("count(*) products (picodata, через router): %v", err)
	}
	fmt.Printf("sharding: SELECT count(*) через router picodata=%d, ожидалось %d\n", picoCount, len(rows))
	if picoCount != int64(len(rows)) {
		log.Fatalf("АССЕРТ: через router видно %d строк, залито было %d — часть данных потерялась или не доехала", picoCount, len(rows))
	}

	// Локальное число кортежей на КАЖДОМ инстансе (box.space.products:len(),
	// снято тем же зондом admin console, что и raft-статус в topology) —
	// именно оно показывает физическое распределение по репликасетам,
	// SQL-агрегация через router его не покажет (router суммирует прозрачно).
	probes := probeAll(containers, adminSock)
	fmt.Println("sharding: локальное число кортежей на каждом инстансе (box.space.products:len()):")
	perReplicaset := map[string][]int64{}
	for _, p := range probes {
		fmt.Printf("  container=%s instance=%s replicaset=%s products_len(local)=%d\n",
			p.Container, p.InstanceName, p.ReplicasetName, p.ProductsLen)
		perReplicaset[p.ReplicasetName] = append(perReplicaset[p.ReplicasetName], p.ProductsLen)
	}

	replicasetNames := make([]string, 0, len(perReplicaset))
	for name := range perReplicaset {
		replicasetNames = append(replicasetNames, name)
	}
	sort.Strings(replicasetNames)

	fmt.Println("sharding: распределение по репликасетам (перекос — честное наблюдение, не подгонка):")
	var totalAcrossReplicasets int64
	usedReplicasets := 0
	for _, name := range replicasetNames {
		counts := perReplicaset[name]
		// Внутри одного репликасета все члены — зеркала (одна и та же копия
		// данных, репликация фактора 2 или 1) — печатаем ВСЕ, чтобы расхождение
		// (репликационный лаг) было видно, но для распределения по
		// репликасетам берём МАКСИМУМ (самая свежая копия на момент опроса).
		var maxCount int64
		allEqual := true
		for _, c := range counts {
			if c > maxCount {
				maxCount = c
			}
			if c != counts[0] {
				allEqual = false
			}
		}
		if !allEqual {
			fmt.Printf("  ПРЕДУПРЕЖДЕНИЕ: репликасет %s — зеркала разошлись (репликационный лаг на момент опроса): %v, беру максимум=%d\n", name, counts, maxCount)
		}
		fmt.Printf("  репликасет=%s (инстансов=%d): products_len=%d\n", name, len(counts), maxCount)
		if maxCount > 0 {
			usedReplicasets++
		}
		totalAcrossReplicasets += maxCount
	}
	fmt.Printf("sharding: сумма по репликасетам=%d, через router=%d, из PG=%d\n", totalAcrossReplicasets, picoCount, len(rows))

	// Данные обязаны лечь больше чем на один репликасет — иначе шардирования
	// фактически нет и число в статье было бы враньём.
	if usedReplicasets < 2 {
		log.Fatalf("АССЕРТ: данные легли на %d репликасет — шардирование не наблюдается", usedReplicasets)
	}
	if totalAcrossReplicasets != int64(len(rows)) {
		log.Fatalf("АССЕРТ: сумма по репликасетам %d != загруженных строк %d", totalAcrossReplicasets, len(rows))
	}
	fmt.Printf("sharding: данные легли на %d репликасета — шардирование наблюдается\n", usedReplicasets)
}

// ---------- сценарий sql ----------

func scenarioSQL(ctx context.Context, origin, pico *pgxpool.Pool) {
	// Идемпотентно на случай, если sql запущен в процессе, отдельном от
	// sharding (см. raiseVDBEOpcodeLimit) — настройка кластерная и
	// персистентная, но полагаться на порядок запуска процессов не стоит.
	raiseVDBEOpcodeLimit(ctx, pico)

	var picoCount int64
	if err := pico.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&picoCount); err != nil {
		log.Fatalf(`count(*) products (picodata) — вероятно таблица не создана: запустите сначала -scenario sharding: %v`, err)
	}
	if picoCount == 0 {
		log.Fatalf("АССЕРТ: в products (picodata) 0 строк — запустите сначала -scenario sharding")
	}

	picoAgg := map[string]int64{}
	rows, err := pico.Query(ctx, `SELECT category, count(*) AS n FROM products GROUP BY category ORDER BY category`)
	if err != nil {
		log.Fatalf("распределённая агрегация (picodata): %v", err)
	}
	for rows.Next() {
		var cat string
		var n int64
		if err := rows.Scan(&cat, &n); err != nil {
			log.Fatalf("scan агрегации picodata: %v", err)
		}
		picoAgg[cat] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("агрегация picodata rows: %v", err)
	}

	pgAgg := map[string]int64{}
	rows2, err := origin.Query(ctx, `SELECT attrs->>'category' AS category, count(*) AS n FROM products GROUP BY attrs->>'category' ORDER BY category`)
	if err != nil {
		log.Fatalf("агрегация (PG): %v", err)
	}
	for rows2.Next() {
		var cat string
		var n int64
		if err := rows2.Scan(&cat, &n); err != nil {
			log.Fatalf("scan агрегации PG: %v", err)
		}
		pgAgg[cat] = n
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		log.Fatalf("агрегация PG rows: %v", err)
	}

	fmt.Println("sql: распределённая агрегация picodata (SELECT category, count(*) FROM products GROUP BY category):")
	catNames := make([]string, 0, len(picoAgg))
	for c := range picoAgg {
		catNames = append(catNames, c)
	}
	sort.Strings(catNames)
	for _, c := range catNames {
		fmt.Printf("  %s: picodata=%d pg=%d\n", c, picoAgg[c], pgAgg[c])
	}

	// Распределённая агрегация обязана дать тот же результат, что и агрегация
	// в источнике. Иначе распределённый SQL сломан либо датасет залит не весь.
	if !reflect.DeepEqual(picoAgg, pgAgg) {
		log.Fatalf("АССЕРТ: агрегация Picodata != PostgreSQL:\n pico=%v\n pg=%v", picoAgg, pgAgg)
	}
	fmt.Println("sql: распределённая агрегация Picodata совпала с агрегацией в источнике (PostgreSQL) по всем категориям")
}

func main() {
	// Пароль ниже — НЕ секрет прода: это дефолт для одноразового локального
	// dev-кластера (сеть docker compose, не наружу), совпадающий с
	// ALTER USER-шагом bootstrap в README. Он уже переопределяем через
	// PICODATA_PASSWORD — дефолт задан только для быстрого локального
	// запуска сценариев без лишней переменной окружения.
	var (
		scenario   = flag.String("scenario", "", "topology|sharding|sql")
		originDSN  = flag.String("origin-dsn", os.Getenv("ORIGIN_DSN"), "DSN источника истины PostgreSQL")
		addr       = flag.String("picodata-addr", envOr("PICODATA_ADDR", "127.0.0.1:5441"), "адрес PG-протокола Picodata (НЕ iproto — см. README)")
		user       = flag.String("picodata-user", envOr("PICODATA_USER", "admin"), "пользователь Picodata (PG-протокол)")
		password   = flag.String("picodata-password", envOr("PICODATA_PASSWORD", "CookbookPass123!"), "пароль Picodata (PG-протокол, env PICODATA_PASSWORD, см. README bootstrap)")
		db         = flag.String("picodata-db", envOr("PICODATA_DB", "picodata"), "имя БД Picodata (PG-протокол)")
		containers = flag.String("containers", envOr("PICODATA_CONTAINERS", "picodata-1,picodata-2,picodata-3,picodata-4"), "имена контейнеров кластера для raft-пробы (docker exec)")
		adminSock  = flag.String("admin-sock", envOr("PICODATA_ADMIN_SOCK", "/var/lib/picodata/admin.sock"), "путь unix-сокета admin-консоли ВНУТРИ контейнера")
	)
	flag.Parse()

	if *originDSN == "" {
		log.Fatal("нужен -origin-dsn или ORIGIN_DSN")
	}

	ctx := context.Background()
	origin := connectOrigin(ctx, *originDSN)
	defer origin.Close()
	pico := connectPicodata(ctx, *addr, *user, *password, *db)
	defer pico.Close()

	containerList := strings.Split(*containers, ",")
	for i := range containerList {
		containerList[i] = strings.TrimSpace(containerList[i])
	}

	start := time.Now()
	switch *scenario {
	case "topology":
		scenarioTopology(ctx, pico, containerList, *adminSock)
	case "sharding":
		scenarioSharding(ctx, origin, pico, containerList, *adminSock)
	case "sql":
		scenarioSQL(ctx, origin, pico)
	default:
		log.Fatalf("неизвестный -scenario=%q (ожидается topology|sharding|sql)", *scenario)
	}
	fmt.Printf("OK (%s, %s)\n", *scenario, time.Since(start).Round(time.Millisecond))
}
