package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"shortlinks/internal/links"
	"shortlinks/internal/store"
)

// Relay — события создания ссылок. Outbox: строка пишется в транзакции ссылки,
// ретранслятор публикует её и отмечает. Direct: публикация после коммита
// отдельным шагом — между коммитом и публикацией событие можно потерять.
type Relay struct {
	Store     *store.Postgres
	JS        jetstream.JetStream
	Published atomic.Int64
	Failed    atomic.Int64
}

func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		_, err := r.Store.OutboxBatch(ctx, 100, func(id int64, subject string, payload []byte) error {
			// Msg-Id по id строки outbox: повтор после сбоя отметки брокер склеит в окне дедупликации
			_, err := r.JS.Publish(ctx, subject, payload, jetstream.WithMsgID(fmt.Sprintf("outbox-%d", id)))
			if err != nil {
				r.Failed.Add(1)
				return err
			}
			r.Published.Add(1)
			return nil
		})
		if err != nil && ctx.Err() == nil {
			slog.Warn("outbox: публикация отложена", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Relay) PublishDirect(l links.Link) {
	payload, _ := json.Marshal(map[string]any{"code": l.Code, "url": l.URL, "created_at": l.CreatedAt})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := r.JS.Publish(ctx, "links.created", payload, jetstream.WithMsgID("direct-"+l.Code)); err != nil {
		r.Failed.Add(1)
		slog.Warn("событие создания не опубликовано", "code", l.Code, "err", err)
		return
	}
	r.Published.Add(1)
}
