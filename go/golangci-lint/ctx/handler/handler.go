// Package handler — обработчики, теряющие срок вызывающего. Метка `// want ctxloss`
// размечает строку, где создаётся новый корневой контекст в функции, которая уже
// получила ctx; готовые линтеры сверяются с той же разметкой.
package handler

import (
	"context"
	"database/sql"
	"time"
)

// Store — доступ к данным.
type Store struct{ db *sql.DB }

func (s *Store) load(ctx context.Context, id int) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM users WHERE id = $1", id).Scan(&name)
	return name, err
}

// Get получил ctx запроса, но идёт в базу с новым корневым контекстом:
// клиент ушёл, а запрос в базе продолжается.
func (s *Store) Get(ctx context.Context, id int) (string, error) {
	return s.load(context.Background(), id) // want ctxloss
}

// GetTimeout задаёт свой срок, но от корня, а не от ctx: срок клиента теряется.
func (s *Store) GetTimeout(ctx context.Context, id int) (string, error) {
	c, cancel := context.WithTimeout(context.TODO(), time.Second) // want ctxloss
	defer cancel()
	return s.load(c, id)
}

// Audit пишет журнал в горутине после ответа — разрыв со сроком намеренный,
// но сделан корневым контекстом, а не context.WithoutCancel.
func (s *Store) Audit(ctx context.Context, id int) {
	go func() {
		_, _ = s.load(context.Background(), id) // want ctxloss
	}()
}

// Poll ждёт сигнала корневого контекста, а не ctx запроса: отмена клиента
// не прерывает ожидание. Контекст никуда не передаётся.
func (s *Store) Poll(ctx context.Context, tick <-chan struct{}) bool {
	bg := context.Background() // want ctxloss
	select {
	case <-bg.Done():
		return false
	case <-tick:
		return true
	}
}

// Job — отложенная задача; контекст хранится до запуска.
type Job struct {
	ctx context.Context
	id  int
}

// Run выполняет задачу с сохранённым контекстом.
func (j Job) Run(s *Store) (string, error) { return s.load(j.ctx, j.id) }

// Enqueue сохраняет в задаче корневой контекст вместо ctx запроса.
func (s *Store) Enqueue(ctx context.Context, id int) Job {
	return Job{ctx: context.Background(), id: id} // want ctxloss
}

// Отрицательные случаи — правило обязано молчать.

// GetPassed передаёт ctx дальше.
func (s *Store) GetPassed(ctx context.Context, id int) (string, error) {
	return s.load(ctx, id)
}

// AuditDetached отрывает работу от срока и отмены вызывающего явно и сохраняет
// значения ctx. Срока у неё теперь нет вовсе — в рабочем коде такой работе
// задают собственный: context.WithTimeout(context.WithoutCancel(ctx), …).
func (s *Store) AuditDetached(ctx context.Context, id int) {
	go func() {
		_, _ = s.load(context.WithoutCancel(ctx), id)
	}()
}

// Warmup не получает ctx — корневой контекст здесь законен.
func (s *Store) Warmup() {
	_, _ = s.load(context.Background(), 0)
}
