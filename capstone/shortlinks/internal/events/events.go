// Package events — события кликов и создания ссылок в NATS JetStream.
//
// Клик публикуется в двух режимах:
//   - sync  — в обработчике редиректа: брокер медленный или недоступен — редирект ждёт или падает;
//   - async — в ограниченный буфер, публикует фоновая горутина; буфер полон — событие
//     отбрасывается и считается: редирект важнее аналитики, но потерю видно в метрике.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"shortlinks/internal/telemetry"
)

const Stream = "SHORTLINKS"

type Click struct {
	ID   string    `json:"id"`
	Code string    `json:"code"`
	At   time.Time `json:"at"`
}

// Connect — соединение и поток SHORTLINKS (clicks.>, links.>), идемпотентно.
func Connect(ctx context.Context, url string) (*nats.Conn, jetstream.JetStream, error) {
	nc, err := nats.Connect(url, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.ReconnectWait(500*time.Millisecond))
	if err != nil {
		return nil, nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = js.CreateOrUpdateStream(cctx, jetstream.StreamConfig{Name: Stream, Subjects: []string{"clicks.>", "links.>"},
		Duplicates: 2 * time.Minute})
	return nc, js, err
}

// Publisher — клики в брокер.
type Publisher struct {
	JS      jetstream.JetStream
	Mode    string // async | sync
	Timeout time.Duration
	Inject  func(ctx context.Context, h nats.Header) // трейс-контекст в заголовки (статья 4)

	queue     chan queued
	mu        sync.RWMutex // Click держит RLock на время постановки в очередь, Close — Lock на её закрытие
	closed    bool
	wg        sync.WaitGroup
	base      context.Context // отменяется, когда срок остановки истёк: остаток буфера не публикуется
	abort     context.CancelFunc
	Published atomic.Int64
	Dropped   atomic.Int64
	Failed    atomic.Int64
	Unsent    atomic.Int64 // не опубликованы, потому что кончился срок остановки
	Late      atomic.Int64 // пришли после Close: очередь уже закрыта, клик отклонён
	seq       atomic.Int64
	node      string
}

// queued — клик в буфере вместе с заголовками: трейс-контекст снимается при постановке,
// пока запрос ещё жив, а не при публикации из фоновой горутины.
type queued struct {
	c Click
	h nats.Header
}

func NewPublisher(js jetstream.JetStream, mode string, buffer int) *Publisher {
	p := &Publisher{JS: js, Mode: mode, Timeout: 2 * time.Second, queue: make(chan queued, buffer),
		node: fmt.Sprintf("%x", time.Now().UnixNano())}
	p.base, p.abort = context.WithCancel(context.Background())
	if mode == "async" {
		p.wg.Add(1)
		go p.loop()
	}
	return p
}

func (p *Publisher) nextID() string { return fmt.Sprintf("%s-%d", p.node, p.seq.Add(1)) }

// Click — событие клика. В sync-режиме ошибка публикации возвращается вызывающему.
func (p *Publisher) Click(ctx context.Context, code string, at time.Time) {
	_ = p.ClickErr(ctx, code, at)
}

func (p *Publisher) ClickErr(ctx context.Context, code string, at time.Time) error {
	c := Click{ID: p.nextID(), Code: code, At: at}
	h := nats.Header{}
	if p.Inject != nil {
		p.Inject(ctx, h)
	}
	if p.Mode == "sync" {
		return p.publish(ctx, c, h)
	}
	// Обработчик, начавший клик до остановки, может дойти сюда уже после Close: отмена контекста не
	// останавливает горутину. Проверка closed и отправка — под одним RLock, Close закрывает очередь под
	// Lock, поэтому отправки в закрытый канал (и паники) не бывает: поздний клик отклоняется и считается.
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		p.Late.Add(1)
		return nil
	}
	select {
	case p.queue <- queued{c, h}:
	default:
		p.Dropped.Add(1)
	}
	return nil
}

func (p *Publisher) publish(ctx context.Context, c Click, h nats.Header) error {
	ctx, span := telemetry.Tracer().Start(telemetry.ExtractNATS(ctx, h), "nats publish click")
	defer span.End()
	telemetry.InjectNATS(ctx, h) // consumer — потомок спана публикации, а не всего запроса
	data, _ := json.Marshal(c)
	msg := &nats.Msg{Subject: "clicks." + c.Code, Data: data, Header: h}
	cctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	// Msg-Id: повторная публикация того же клика (ретрай продюсера) склеивается брокером
	_, err := p.JS.PublishMsg(cctx, msg, jetstream.WithMsgID(c.ID))
	if err != nil {
		p.Failed.Add(1)
		return err
	}
	p.Published.Add(1)
	return nil
}

func (p *Publisher) loop() {
	defer p.wg.Done()
	for q := range p.queue {
		if p.base.Err() != nil {
			p.Unsent.Add(1) // срок остановки истёк — не публикуем, но считаем
			continue
		}
		if err := p.publish(p.base, q.c, q.h); err != nil {
			slog.Warn("клик не опубликован", "code", q.c.Code, "err", err)
		}
	}
}

// Close — опубликовать то, что уже в буфере, но не дольше срока ctx (часть graceful shutdown).
// Каждая публикация ждёт брокер до Timeout, поэтому без общего срока полный буфер при недоступном
// брокере пережил бы и SHUTDOWN_TIMEOUT, и terminationGracePeriodSeconds. По истечении срока
// остаток не публикуется и возвращается числом — потерю видно в логе и метрике.
func (p *Publisher) Close(ctx context.Context) int64 {
	if p.Mode != "async" {
		return 0
	}
	p.mu.Lock()
	p.closed = true
	close(p.queue)
	p.mu.Unlock()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		p.abort() // текущая публикация прерывается, остаток буфера только считается
		<-done
	}
	return p.Unsent.Load()
}

// Pending — сколько кликов ждут публикации в буфере.
func (p *Publisher) Pending() int { return len(p.queue) }
