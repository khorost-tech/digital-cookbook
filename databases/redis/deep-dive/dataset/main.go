package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type Event struct {
	UserID    string            `json:"user_id"`
	SessionID string            `json:"session_id"`
	Ts        time.Time         `json:"ts"`
	Kind      string            `json:"kind"`
	Payload   map[string]string `json:"payload"`
}

var eventKinds = []string{"page_view", "click", "purchase", "logout"}

func Generate(seed int64, users, eventsPerUser int) []Event {
	r := rand.New(rand.NewSource(seed))
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	events := make([]Event, 0, users*eventsPerUser)
	for u := 0; u < users; u++ {
		userID := fmt.Sprintf("user-%05d", u)
		sessionID := fmt.Sprintf("sess-%05d-%d", u, r.Intn(3))
		for e := 0; e < eventsPerUser; e++ {
			events = append(events, Event{
				UserID:    userID,
				SessionID: sessionID,
				Ts:        base.Add(time.Duration(r.Intn(30*24*60)) * time.Minute),
				Kind:      eventKinds[r.Intn(len(eventKinds))],
				Payload:   map[string]string{"ip": fmt.Sprintf("10.0.%d.%d", r.Intn(255), r.Intn(255))},
			})
		}
	}
	return events
}

func main() {
	users := flag.Int("users", 2000, "число пользователей")
	eventsPerUser := flag.Int("events-per-user", 50, "событий на пользователя")
	load := flag.Bool("load", false, "загрузить напрямую в Redis вместо stdout")
	addr := flag.String("addr", "127.0.0.1:6379", "адрес Redis (для -load)")
	flag.Parse()

	events := Generate(42, *users, *eventsPerUser)

	if !*load {
		enc := json.NewEncoder(os.Stdout)
		for _, ev := range events {
			_ = enc.Encode(ev)
		}
		return
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: *addr})
	defer rdb.Close()
	pipe := rdb.Pipeline()
	for i, ev := range events {
		key := fmt.Sprintf("event:%s:%d", ev.UserID, i)
		pipe.HSet(ctx, key, "session_id", ev.SessionID, "kind", ev.Kind, "ip", ev.Payload["ip"])
		pipe.ZAdd(ctx, "events:by-ts", redis.Z{Score: float64(ev.Ts.Unix()), Member: key})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "load failed:", err)
		os.Exit(1)
	}
	fmt.Printf("loaded %d events for %d users\n", len(events), *users)
}
