package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Write skew — классика «дежурных врачей». Двое на дежурстве, правило: хотя бы
// один должен остаться. Каждая транзакция читает, сколько дежурят, видит двоих
// и снимает с дежурства «себя». Строки разные, конфликта записи нет — поймать
// это может только сериализуемость (или явная блокировка читаемого).
//
// Шаги идут в фиксированном порядке. Если шаг ждёт блокировку дольше
// stepWait, координатор отмечает это и идёт дальше — шаг доделается сам.

const stepWait = 1500 * time.Millisecond

type txnRunner struct {
	name  string
	conn  *sql.Conn
	steps chan func() string
	done  chan string
	dead  bool
	seen  int // сколько дежурных увидела транзакция при чтении
}

func newRunner(ctx context.Context, db *sql.DB, name string) *txnRunner {
	c, err := db.Conn(ctx)
	must(err, "conn")
	r := &txnRunner{name: name, conn: c, steps: make(chan func() string, 8), done: make(chan string, 8)}
	go func() {
		for f := range r.steps {
			r.done <- f()
		}
	}()
	return r
}

func runSkew(db *sql.DB, engine, iso string) {
	ctx := context.Background()
	_, _ = db.Exec("DROP TABLE IF EXISTS oncall")
	_, err := db.Exec("CREATE TABLE oncall (id INT PRIMARY KEY, on_call INT NOT NULL)")
	must(err, "create oncall")
	_, err = db.Exec("INSERT INTO oncall VALUES (1, 1), (2, 1)")
	must(err, "insert oncall")

	begin := "BEGIN"
	isoQ := "SHOW transaction_isolation"
	if !pgWire(engine) {
		begin = "START TRANSACTION"
		isoQ = "SELECT @@transaction_isolation"
	}
	read := "SELECT count(*) FROM oncall WHERE on_call = 1"
	switch iso {
	case "serializable":
		if pgWire(engine) {
			begin = "BEGIN ISOLATION LEVEL SERIALIZABLE"
		}
	case "for-update":
		read = "SELECT id FROM oncall WHERE on_call = 1 FOR UPDATE"
	case "skipcheck":
		// Только TiDB: SERIALIZABLE с флагом, который предлагает текст ERROR 8048.
		if engine != "tidb" {
			die("skipcheck имеет смысл только для tidb")
		}
	case "default":
	default:
		die("iso: default | serializable | for-update | skipcheck")
	}

	t1, t2 := newRunner(ctx, db, "T1"), newRunner(ctx, db, "T2")
	if (iso == "serializable" || iso == "skipcheck") && !pgWire(engine) {
		for _, t := range []*txnRunner{t1, t2} {
			if iso == "skipcheck" {
				if _, err := t.conn.ExecContext(ctx, "SET SESSION tidb_skip_isolation_level_check = 1"); err != nil {
					die("skipcheck: %s", errLine(err))
				}
			}
			if _, err := t.conn.ExecContext(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL SERIALIZABLE"); err != nil {
				fmt.Printf("%s SET SERIALIZABLE -> %s\n", t.name, errLine(err))
				fmt.Println("итог: уровень SERIALIZABLE движком не принят, прогон не имеет смысла")
				return
			}
		}
	}

	exec := func(t *txnRunner, label, q string) func() string {
		return func() string {
			if t.dead {
				return "пропущен (транзакция уже откатана)"
			}
			// Логика приложения: уходить можно, только если дежурят двое.
			if label == "leave" && t.seen < 2 {
				return fmt.Sprintf("не выполняется: транзакция видит дежурных %d, уходить нельзя", t.seen)
			}
			if _, err := t.conn.ExecContext(ctx, q); err != nil {
				t.dead = true
				_, _ = t.conn.ExecContext(ctx, "ROLLBACK")
				return errLine(err)
			}
			return "ok"
		}
	}
	query := func(t *txnRunner, q string, count bool) func() string {
		return func() string {
			if t.dead {
				return "пропущен (транзакция уже откатана)"
			}
			rows, err := t.conn.QueryContext(ctx, q)
			if err != nil {
				t.dead = true
				_, _ = t.conn.ExecContext(ctx, "ROLLBACK")
				return errLine(err)
			}
			defer rows.Close()
			n := 0
			var s string
			for rows.Next() {
				var v any
				_ = rows.Scan(&v)
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				s = fmt.Sprint(v)
				n++
			}
			if err := rows.Err(); err != nil {
				t.dead = true
				_, _ = t.conn.ExecContext(ctx, "ROLLBACK")
				return errLine(err)
			}
			if count {
				fmt.Sscan(s, &t.seen)
				return s
			}
			t.seen = n
			return fmt.Sprintf("%d строк(и) под блокировкой", n)
		}
	}

	plan := []struct {
		t     *txnRunner
		label string
		f     func() string
	}{
		{t1, begin, exec(t1, "begin", begin)},
		{t2, begin, exec(t2, "begin", begin)},
		{t1, isoQ, query(t1, isoQ, true)},
		{t1, read, query(t1, read, iso != "for-update")},
		{t2, read, query(t2, read, iso != "for-update")},
		{t1, "UPDATE oncall SET on_call = 0 WHERE id = 1", exec(t1, "leave", "UPDATE oncall SET on_call = 0 WHERE id = 1")},
		{t2, "UPDATE oncall SET on_call = 0 WHERE id = 2", exec(t2, "leave", "UPDATE oncall SET on_call = 0 WHERE id = 2")},
		{t1, "COMMIT", exec(t1, "", "COMMIT")},
		{t2, "COMMIT", exec(t2, "", "COMMIT")},
	}
	pending := map[*txnRunner]int{}
	for _, p := range plan {
		p.t.steps <- p.f
		pending[p.t]++
		// Ждём, пока у этой транзакции не останется невыполненных шагов.
		for pending[p.t] > 0 {
			select {
			case res := <-p.t.done:
				pending[p.t]--
				if pending[p.t] == 0 {
					fmt.Printf("%s  %-52s -> %s\n", p.t.name, p.label, res)
				} else {
					fmt.Printf("%s  (ранее ждавший шаг)%33s -> %s\n", p.t.name, "", res)
				}
			case <-time.After(stepWait):
				why := "ждёт блокировку, идём дальше"
				if pending[p.t] > 1 {
					why = "в очереди: предыдущий шаг этой транзакции ещё ждёт"
				}
				fmt.Printf("%s  %-52s -> %s\n", p.t.name, p.label, why)
				goto next
			}
		}
	next:
		// Попутно забираем результаты шагов другой транзакции, если они дозрели.
		for _, o := range []*txnRunner{t1, t2} {
			if o == p.t {
				continue
			}
		drain:
			for pending[o] > 0 {
				select {
				case res := <-o.done:
					pending[o]--
					fmt.Printf("%s  (ранее ждавший шаг)%33s -> %s\n", o.name, "", res)
				default:
					break drain
				}
			}
		}
	}
	for _, o := range []*txnRunner{t1, t2} {
		for pending[o] > 0 {
			res := <-o.done
			pending[o]--
			fmt.Printf("%s  (ранее ждавший шаг)%33s -> %s\n", o.name, "", res)
		}
	}

	var left int
	must(db.QueryRow("SELECT count(*) FROM oncall WHERE on_call = 1").Scan(&left), "final count")
	verdict := "инвариант цел"
	if left == 0 {
		verdict = "АНОМАЛИЯ write skew: на дежурстве никого"
	}
	fmt.Printf("итог: на дежурстве %d — %s\n", left, verdict)
}
