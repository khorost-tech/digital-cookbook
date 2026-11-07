package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// stubJS — брокер, который подтверждает публикацию после задержки или отказывает по контексту.
type stubJS struct {
	jetstream.JetStream
	delay time.Duration
}

func (s stubJS) PublishMsg(ctx context.Context, _ *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	select {
	case <-time.After(s.delay):
		return &jetstream.PubAck{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Клики идут конкурентно с Close: ни одной паники, и каждый клик учтён ровно в одном исходе.
func TestConcurrentClickAndClose(t *testing.T) {
	for round := 0; round < 200; round++ {
		p := NewPublisher(stubJS{delay: time.Millisecond}, "async", 8)
		const workers, per = 16, 50
		var wg sync.WaitGroup
		start := make(chan struct{})
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < per; i++ {
					p.Click(context.Background(), "abc", time.Now())
				}
			}()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		close(start)
		time.Sleep(time.Duration(round%5) * time.Millisecond)
		p.Close(ctx)
		cancel()
		wg.Wait()
		total := p.Published.Load() + p.Dropped.Load() + p.Unsent.Load() + p.Failed.Load() + p.Late.Load()
		if total != workers*per {
			t.Fatalf("раунд %d: учтено %d кликов из %d", round, total, workers*per)
		}
	}
}

// Срок Close истёк при недоступном брокере: остаток не публикуется, а считается.
func TestCloseDeadlineCountsRest(t *testing.T) {
	p := NewPublisher(stubJS{delay: time.Hour}, "async", 100)
	for i := 0; i < 50; i++ {
		p.Click(context.Background(), "abc", time.Now())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	unsent := p.Close(ctx)
	if time.Since(start) > time.Second || unsent == 0 || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("Close не уложился в срок или не посчитал остаток: unsent=%d за %s", unsent, time.Since(start))
	}
}
