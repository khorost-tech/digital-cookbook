// Package semantics проверяет смысл разметки, а не правила линтера: что
// дефект, на который указывает находка, действительно есть в поведении
// программы, а исправленная пара его не имеет.
package semantics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
)

// Фиктивный драйвер: любой запрос возвращает rows строк с одной колонкой name.
type fakeDriver struct{ rows int }

type fakeConn struct{ rows int }

type fakeRows struct{ left int }

// Open — база из одного соединения, чтобы занятое соединение было видно в Stats().InUse.
func Open(rows int) *sql.DB {
	db := sql.OpenDB(connector{rows: rows})
	db.SetMaxOpenConns(1)
	return db
}

type connector struct{ rows int }

func (c connector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{rows: c.rows}, nil }
func (c connector) Driver() driver.Driver                        { return &fakeDriver{rows: c.rows} }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{rows: d.rows}, nil }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{left: c.rows}, nil
}

func (r *fakeRows) Columns() []string { return []string{"name"} }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.left == 0 {
		return io.EOF
	}
	r.left--
	dest[0] = "user"
	return nil
}
