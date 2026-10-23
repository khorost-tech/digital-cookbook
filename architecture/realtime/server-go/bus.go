package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
)

// Bus — шина между инстансами. Публикация уходит в шину, а не прямо клиентам:
// событие доходит до подписчиков всех инстансов, включая этот.
type Bus interface {
	Publish(ctx context.Context, e Event) error
	Run(ctx context.Context, hub *Hub) // подписка; переподключается сама
	Up() bool                          // жива ли подписка этого инстанса
	History(ctx context.Context, topic string, after int64) ([]Event, error)
}

var errNoHistory = errors.New("у шины нет истории")

func newBus(kind, redisAddr, natsURL string, topics []string) (Bus, error) {
	switch kind {
	case "none":
		return &localBus{}, nil
	case "redis":
		return &redisPubSub{rdb: redisClient(redisAddr)}, nil
	case "redis-streams":
		return &redisStreams{rdb: redisClient(redisAddr), topics: topics}, nil
	case "nats":
		return newNats(natsURL, false)
	case "jetstream":
		return newNats(natsURL, true)
	}
	return nil, fmt.Errorf("RT_BUS=%q: неизвестная шина", kind)
}

// --- без шины: событие видят только клиенты этого инстанса -----------------

type localBus struct{ hub *Hub }

func (b *localBus) Publish(_ context.Context, e Event) error { b.hub.deliver(e); return nil }
func (b *localBus) Run(_ context.Context, hub *Hub)          { b.hub = hub }
func (b *localBus) Up() bool                                 { return true }
func (b *localBus) History(context.Context, string, int64) ([]Event, error) {
	return nil, errNoHistory
}

// --- Redis -------------------------------------------------------------------

func redisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 300 * time.Millisecond,
		ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond,
		MaxRetries: 0})
}

// Pub/sub — доставка «не более одного раза»: что опубликовано, пока подписка
// этого инстанса порвана, ему не достанется никогда.
type redisPubSub struct {
	rdb *redis.Client
	up  atomic.Bool
}

func (b *redisPubSub) Publish(ctx context.Context, e Event) error {
	data, _ := json.Marshal(e)
	return b.rdb.Publish(ctx, "rt."+e.Topic, data).Err()
}

func (b *redisPubSub) Run(ctx context.Context, hub *Hub) {
	go func() {
		for ctx.Err() == nil {
			ps := b.rdb.PSubscribe(ctx, "rt.*")
			if _, err := ps.Receive(ctx); err != nil { // подтверждение подписки
				b.up.Store(false)
				ps.Close()
				time.Sleep(100 * time.Millisecond)
				continue
			}
			b.up.Store(true)
			for {
				msg, err := ps.ReceiveTimeout(ctx, 300*time.Millisecond)
				if err != nil {
					var ne interface{ Timeout() bool }
					if errors.As(err, &ne) && ne.Timeout() {
						if ps.Ping(ctx) == nil { // тишина, но подписка жива
							continue
						}
					}
					break
				}
				if m, ok := msg.(*redis.Message); ok {
					var e Event
					if json.Unmarshal([]byte(m.Payload), &e) == nil {
						hub.deliver(e)
					}
				}
			}
			b.up.Store(false)
			ps.Close()
		}
	}()
}

func (b *redisPubSub) Up() bool { return b.up.Load() }
func (b *redisPubSub) History(context.Context, string, int64) ([]Event, error) {
	return nil, errNoHistory
}

// Streams — журнал: подписчик помнит последний прочитанный id и после разрыва
// дочитывает с него. Id записи — «0-<id события>», чтобы история читалась по
// номеру события без отдельной карты.
type redisStreams struct {
	rdb    *redis.Client
	topics []string
	up     atomic.Bool
}

func (b *redisStreams) Publish(ctx context.Context, e Event) error {
	return b.rdb.XAdd(ctx, &redis.XAddArgs{Stream: "rt." + e.Topic, ID: fmt.Sprintf("0-%d", e.ID),
		Values: map[string]any{"payload": e.Payload}}).Err()
}

func (b *redisStreams) Run(ctx context.Context, hub *Hub) {
	last := map[string]string{}
	for _, t := range b.topics {
		last[t] = "$"
	}
	go func() {
		for ctx.Err() == nil {
			args := &redis.XReadArgs{Block: 250 * time.Millisecond}
			for _, t := range b.topics {
				args.Streams = append(args.Streams, "rt."+t)
			}
			for _, t := range b.topics {
				args.Streams = append(args.Streams, last[t])
			}
			res, err := b.rdb.XRead(ctx, args).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				b.up.Store(false)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if errors.Is(err, redis.Nil) && b.rdb.Ping(ctx).Err() != nil {
				b.up.Store(false)
				continue
			}
			b.up.Store(true)
			for _, s := range res {
				topic := strings.TrimPrefix(s.Stream, "rt.")
				for _, m := range s.Messages {
					last[topic] = m.ID
					hub.deliver(Event{ID: streamSeq(m.ID), Topic: topic, Payload: fmt.Sprint(m.Values["payload"])})
				}
			}
			for _, t := range b.topics { // "$" — только при первом чтении
				if last[t] == "$" {
					if id, err := b.rdb.XInfoStream(ctx, "rt."+t).Result(); err == nil {
						last[t] = id.LastGeneratedID
					} else {
						last[t] = "0-0"
					}
				}
			}
		}
	}()
}

func streamSeq(id string) int64 {
	var a, b int64
	fmt.Sscanf(id, "%d-%d", &a, &b)
	return b
}

func (b *redisStreams) Up() bool { return b.up.Load() }

func (b *redisStreams) History(ctx context.Context, topic string, after int64) ([]Event, error) {
	msgs, err := b.rdb.XRange(ctx, "rt."+topic, fmt.Sprintf("0-%d", after+1), "+").Result()
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Event{ID: streamSeq(m.ID), Topic: topic, Payload: fmt.Sprint(m.Values["payload"])})
	}
	return out, nil
}

// --- NATS ----------------------------------------------------------------------

// Core — тоже «не более одного раза». JetStream — журнал: упорядоченный
// потребитель после разрыва продолжает с последнего номера в потоке.
type natsBus struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	jsm bool
	mu  sync.Mutex
}

func newNats(url string, js bool) (*natsBus, error) {
	var nc *nats.Conn
	var err error
	for i := 0; i < 50; i++ { // шина может подниматься дольше сервера
		nc, err = nats.Connect(url, nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond),
			nats.PingInterval(300*time.Millisecond), nats.MaxPingsOutstanding(1),
			nats.Timeout(300*time.Millisecond))
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	b := &natsBus{nc: nc, jsm: js}
	if js {
		b.js, err = jetstream.New(nc)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "RT", Subjects: []string{"rt.>"},
			Storage: jetstream.MemoryStorage})
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *natsBus) Publish(ctx context.Context, e Event) error {
	data, _ := json.Marshal(e)
	if b.jsm {
		_, err := b.js.Publish(ctx, "rt."+e.Topic, data)
		return err
	}
	return b.nc.Publish("rt."+e.Topic, data)
}

func (b *natsBus) Run(ctx context.Context, hub *Hub) {
	handle := func(data []byte) {
		var e Event
		if json.Unmarshal(data, &e) == nil {
			hub.deliver(e)
		}
	}
	if !b.jsm {
		if _, err := b.nc.Subscribe("rt.>", func(m *nats.Msg) { handle(m.Data) }); err != nil {
			log.Fatalf("nats subscribe: %v", err)
		}
		return
	}
	cons, err := b.js.OrderedConsumer(ctx, "RT", jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{"rt.>"}, DeliverPolicy: jetstream.DeliverNewPolicy})
	if err != nil {
		log.Fatalf("jetstream consumer: %v", err)
	}
	if _, err := cons.Consume(func(m jetstream.Msg) { handle(m.Data()) }); err != nil {
		log.Fatalf("jetstream consume: %v", err)
	}
}

func (b *natsBus) Up() bool { return b.nc.Status() == nats.CONNECTED }

func (b *natsBus) History(ctx context.Context, topic string, after int64) ([]Event, error) {
	if !b.jsm {
		return nil, errNoHistory
	}
	cons, err := b.js.OrderedConsumer(ctx, "RT", jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{"rt." + topic}, DeliverPolicy: jetstream.DeliverAllPolicy})
	if err != nil {
		return nil, err
	}
	info, err := b.js.Stream(ctx, "RT")
	if err != nil {
		return nil, err
	}
	state := info.CachedInfo().State
	out := []Event{}
	if state.Msgs == 0 {
		return out, nil
	}
	for {
		batch, err := cons.FetchNoWait(256)
		if err != nil {
			return nil, err
		}
		n := 0
		var lastSeq uint64
		for m := range batch.Messages() {
			n++
			var e Event
			if json.Unmarshal(m.Data(), &e) == nil && e.ID > after {
				out = append(out, e)
			}
			if md, err := m.Metadata(); err == nil {
				lastSeq = md.Sequence.Stream
			}
		}
		if n == 0 || lastSeq >= state.LastSeq {
			return out, nil
		}
	}
}
