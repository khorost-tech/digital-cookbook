package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// compat — один и тот же список конструкций, выполняемый штатным драйвером.
// Результат — дословный ответ движка: значение, «ok» или текст ошибки.
// Списков два: для PG-wire (сравнивать с колонкой pg) и для MySQL-wire
// (сравнивать с колонкой mysql).

type check struct {
	name string
	sql  []string // выполняются по очереди на одном соединении
	// final — запрос, значение которого и есть результат. Пусто — результат «ok».
	final string
	// wantErr — проверка ограничения: ошибка означает, что движок его соблюдает.
	wantErr bool
}

var pgChecks = []check{
	{name: "serial: какие id выдаёт",
		sql:   []string{"CREATE TABLE c_serial (id SERIAL PRIMARY KEY, x INT)", "INSERT INTO c_serial (x) VALUES (1), (2), (3)"},
		final: "SELECT string_agg(id::text, ',' ORDER BY id) FROM c_serial"},
	{name: "sequence: nextval",
		sql:   []string{"CREATE SEQUENCE c_seq"},
		final: "SELECT nextval('c_seq')::text || ',' || nextval('c_seq')::text"},
	{name: "gen_random_uuid()", final: "SELECT length(gen_random_uuid()::text)::text"},
	{name: "JSONB + GIN-индекс",
		sql: []string{"CREATE TABLE c_json (id INT PRIMARY KEY, doc JSONB)", "CREATE INDEX c_json_gin ON c_json USING GIN (doc)"}},
	{name: "частичный индекс",
		sql: []string{"CREATE TABLE c_pi (id INT PRIMARY KEY, active BOOL)", "CREATE INDEX c_pi_a ON c_pi (id) WHERE active"}},
	{name: "генерируемая колонка STORED",
		sql: []string{"CREATE TABLE c_gen (a INT PRIMARY KEY, b INT GENERATED ALWAYS AS (a * 2) STORED)"}},
	{name: "FK: строка без родителя",
		sql: []string{"CREATE TABLE c_parent (id INT PRIMARY KEY)",
			"CREATE TABLE c_child (id INT PRIMARY KEY, p INT REFERENCES c_parent (id) ON DELETE CASCADE)",
			"INSERT INTO c_child VALUES (1, 999)"},
		wantErr: true},
	{name: "CHECK: нарушающая строка",
		sql:     []string{"CREATE TABLE c_chk (x INT CHECK (x > 0))", "INSERT INTO c_chk VALUES (-1)"},
		wantErr: true},
	{name: "функция PL/pgSQL",
		sql:   []string{"CREATE FUNCTION c_f(x INT) RETURNS INT LANGUAGE plpgsql AS $$ BEGIN RETURN x + 1; END $$"},
		final: "SELECT c_f(1)::text"},
	{name: "триггер BEFORE INSERT",
		sql: []string{"CREATE TABLE c_trg (id INT PRIMARY KEY, upd TIMESTAMPTZ)",
			"CREATE FUNCTION c_trg_f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.upd := now(); RETURN NEW; END $$",
			"CREATE TRIGGER c_trg_t BEFORE INSERT ON c_trg FOR EACH ROW EXECUTE FUNCTION c_trg_f()",
			"INSERT INTO c_trg (id) VALUES (1)"},
		final: "SELECT (upd IS NOT NULL)::text FROM c_trg"},
	{name: "процедура + CALL",
		sql: []string{"CREATE PROCEDURE c_p() LANGUAGE plpgsql AS $$ BEGIN NULL; END $$", "CALL c_p()"}},
	{name: "CREATE EXTENSION pg_trgm", sql: []string{"CREATE EXTENSION IF NOT EXISTS pg_trgm"}},
	{name: "LISTEN / NOTIFY", sql: []string{"LISTEN c_chan", "NOTIFY c_chan"}},
	{name: "pg_advisory_lock", final: "SELECT 'взята' FROM (SELECT pg_advisory_lock(42)) AS l"},
	{name: "FOR UPDATE SKIP LOCKED", sql: []string{"SELECT id FROM c_pi FOR UPDATE SKIP LOCKED"}},
	{name: "EXCLUDE USING gist",
		sql: []string{"CREATE TABLE c_ex (r tstzrange, EXCLUDE USING gist (r WITH &&))"}},
	{name: "CREATE INDEX CONCURRENTLY", sql: []string{"CREATE INDEX CONCURRENTLY c_pi_c ON c_pi (active)"}},
	{name: "DDL откатывается вместе с транзакцией",
		sql:   []string{"BEGIN", "CREATE TABLE c_txddl (id INT PRIMARY KEY)", "INSERT INTO c_txddl VALUES (1)", "ROLLBACK"},
		final: "SELECT CASE WHEN count(*) = 0 THEN 'да, таблицы нет' ELSE 'нет, таблица осталась' END FROM information_schema.tables WHERE table_name = 'c_txddl'"},
	{name: "INSERT перед DDL откатывается",
		sql:   []string{"BEGIN", "INSERT INTO c_pi VALUES (100, true)", "CREATE TABLE c_txddl2 (id INT PRIMARY KEY)", "ROLLBACK"},
		final: "SELECT CASE WHEN count(*) = 0 THEN 'да, строки нет' ELSE 'нет, строка закоммичена' END FROM c_pi WHERE id = 100"},
	{name: "BEGIN READ COMMITTED → фактический уровень",
		sql:   []string{"BEGIN ISOLATION LEVEL READ COMMITTED"},
		final: "SHOW transaction_isolation"},
	{name: "BEGIN REPEATABLE READ → фактический уровень",
		sql:   []string{"BEGIN ISOLATION LEVEL REPEATABLE READ"},
		final: "SHOW transaction_isolation"},
}

var myChecks = []check{
	{name: "AUTO_INCREMENT: какие id выдаёт",
		sql:   []string{"CREATE TABLE c_ai (id BIGINT AUTO_INCREMENT PRIMARY KEY, x INT)", "INSERT INTO c_ai (x) VALUES (1), (2), (3)"},
		final: "SELECT GROUP_CONCAT(id ORDER BY id) FROM c_ai"},
	{name: "FK: строка без родителя",
		sql: []string{"CREATE TABLE c_parent (id INT PRIMARY KEY)",
			"CREATE TABLE c_child (id INT PRIMARY KEY, p INT, FOREIGN KEY (p) REFERENCES c_parent (id) ON DELETE CASCADE)",
			"INSERT INTO c_child VALUES (1, 999)"},
		wantErr: true},
	{name: "CHECK: нарушающая строка",
		sql:     []string{"CREATE TABLE c_chk (x INT, CHECK (x > 0))", "INSERT INTO c_chk VALUES (-1)"},
		wantErr: true},
	{name: "JSON + функциональный индекс",
		sql: []string{"CREATE TABLE c_j (id INT PRIMARY KEY, doc JSON)",
			"CREATE INDEX c_j_i ON c_j ((CAST(doc->>'$.a' AS CHAR(32))))"}},
	{name: "генерируемая колонка STORED",
		sql: []string{"CREATE TABLE c_gen (a INT PRIMARY KEY, b INT AS (a * 2) STORED)"}},
	{name: "триггер BEFORE INSERT",
		sql: []string{"CREATE TABLE c_trg (id INT PRIMARY KEY, x INT)",
			"CREATE TRIGGER c_trg_t BEFORE INSERT ON c_trg FOR EACH ROW SET NEW.x = 7",
			"INSERT INTO c_trg (id) VALUES (1)"},
		final: "SELECT CAST(x AS CHAR) FROM c_trg"},
	{name: "процедура + CALL",
		sql: []string{"CREATE PROCEDURE c_p() BEGIN SELECT 1; END", "CALL c_p()"}},
	{name: "FULLTEXT-индекс",
		sql: []string{"CREATE TABLE c_ft (id INT PRIMARY KEY, t TEXT, FULLTEXT KEY c_ft_t (t))"}},
	{name: "SPATIAL-индекс",
		sql: []string{"CREATE TABLE c_sp (id INT PRIMARY KEY, g POINT NOT NULL SRID 0, SPATIAL INDEX c_sp_g (g))"}},
	{name: "CREATE EVENT", sql: []string{"CREATE EVENT c_ev ON SCHEDULE EVERY 1 HOUR DO SELECT 1"}},
	{name: "GET_LOCK", final: "SELECT CAST(GET_LOCK('c', 1) AS CHAR)"},
	{name: "FOR UPDATE SKIP LOCKED",
		sql: []string{"START TRANSACTION", "SELECT id FROM c_ai FOR UPDATE SKIP LOCKED", "COMMIT"}},
	{name: "SAVEPOINT / ROLLBACK TO",
		sql: []string{"START TRANSACTION", "SAVEPOINT s1", "INSERT INTO c_ai (x) VALUES (50)", "ROLLBACK TO SAVEPOINT s1", "COMMIT"},
		final: "SELECT CAST(count(*) AS CHAR) FROM c_ai WHERE x = 50"},
	{name: "XA-транзакция",
		sql: []string{"XA START 'c1'", "INSERT INTO c_ai (x) VALUES (60)", "XA END 'c1'", "XA PREPARE 'c1'", "XA COMMIT 'c1'"}},
	{name: "INSERT перед DDL откатывается",
		sql:   []string{"START TRANSACTION", "INSERT INTO c_ai (x) VALUES (100)", "CREATE TABLE c_txddl2 (id INT PRIMARY KEY)", "ROLLBACK"},
		final: "SELECT CASE WHEN count(*) = 0 THEN 'да, строки нет' ELSE 'нет, строка закоммичена' END FROM c_ai WHERE x = 100"},
	{name: "SERIALIZABLE → фактический уровень",
		sql:   []string{"SET SESSION TRANSACTION ISOLATION LEVEL SERIALIZABLE"},
		final: "SELECT @@transaction_isolation"},
}

func runCompat(db *sql.DB, engine string) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	must(err, "conn")
	defer conn.Close()

	list := myChecks
	if pgWire(engine) {
		list = pgChecks
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS compat CASCADE")
		_, err = conn.ExecContext(ctx, "CREATE SCHEMA compat")
		must(err, "create schema")
		_, err = conn.ExecContext(ctx, "SET search_path = compat")
		must(err, "search_path")
	} else {
		_, _ = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS compat")
		_, err = conn.ExecContext(ctx, "CREATE DATABASE compat")
		must(err, "create database")
		_, err = conn.ExecContext(ctx, "USE compat")
		must(err, "use")
	}

	fmt.Printf("engine=%s  проверок: %d\n", engine, len(list))
	for _, c := range list {
		res := runCheck(ctx, conn, c, !pgWire(engine))
		fmt.Printf("%-44s | %s\n", c.name, res)
		// Чтобы упавшая проверка не оставила открытую или сломанную транзакцию
		// следующей.
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		if !pgWire(engine) {
			_, _ = conn.ExecContext(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL "+defaultIso(engine))
		}
	}
}

func defaultIso(engine string) string {
	if engine == "ob" {
		return "READ COMMITTED"
	}
	return "REPEATABLE READ"
}

// mysqlWarnings — предупреждения последнего оператора. TiDB и OceanBase
// часть неподдержанного синтаксиса принимают молча, с одним предупреждением:
// без SHOW WARNINGS такая проверка выглядела бы как «ok».
func mysqlWarnings(ctx context.Context, conn *sql.Conn) []string {
	rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var level, msg string
		var code int
		if rows.Scan(&level, &code, &msg) == nil {
			out = append(out, fmt.Sprintf("%s %d: %s", level, code, firstLine(msg)))
		}
	}
	return out
}

func runCheck(ctx context.Context, conn *sql.Conn, c check, mysqlWire bool) string {
	var warns []string
	res := runCheckInner(ctx, conn, c, mysqlWire, &warns)
	if len(warns) > 0 {
		res += " [предупреждение — " + strings.Join(warns, "; ") + "]"
	}
	return res
}

func runCheckInner(ctx context.Context, conn *sql.Conn, c check, mysqlWire bool, warns *[]string) string {
	for _, q := range c.sql {
		_, err := conn.ExecContext(ctx, q)
		if err == nil && mysqlWire {
			*warns = append(*warns, mysqlWarnings(ctx, conn)...)
		}
		if err != nil {
			if c.wantErr && strings.HasPrefix(strings.ToUpper(q), "INSERT") {
				return "соблюдается — " + errLine(err)
			}
			return "ОШИБКА — " + errLine(err)
		}
	}
	if c.wantErr {
		return "НЕ соблюдается: нарушающая строка вставлена молча"
	}
	if c.final == "" {
		return "ok"
	}
	var v sql.NullString
	if err := conn.QueryRowContext(ctx, c.final).Scan(&v); err != nil {
		return "ОШИБКА — " + errLine(err)
	}
	return v.String
}
