package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// runRedis: конкурентный инкремент счётчика.
//   - naive GET + SET: чтение вынесено за транзакцию, значение к моменту SET уже устарело
//     → lost update. Виноват паттерн, а не отсутствие транзакции как таковой.
//   - WATCH + MULTI/EXEC (оптимистичный CAS) с retry: сам MULTI/EXEC исполняется атомарно и
//     без вклинивания чужих команд, но rollback'а у него нет и read-modify-write он не
//     закрывает. Это делает WATCH: условная проверка версии ключа — EXEC не применяется,
//     если ключ изменился после WATCH; повторяем — итог корректен. Альтернатива для
//     серверной логики — Lua/FUNCTION (а для голого инкремента хватило бы INCR).
func runRedis() {
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: envOr("REDIS_ADDR", "localhost:6390")})
	defer rdb.Close()

	const workers, iters = 16, 100
	expected := workers * iters

	// 1) naive GET + SET
	rdb.Set(ctx, "counter", 0, 0)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				v, _ := rdb.Get(ctx, "counter").Int()
				// обработка между чтением и записью — то самое окно, в котором другой клиент
				// успевает прочитать то же значение; на нём и теряются обновления
				time.Sleep(time.Millisecond)
				rdb.Set(ctx, "counter", v+1, 0)
			}
		}()
	}
	wg.Wait()
	naive, _ := rdb.Get(ctx, "counter").Int()

	// 2) WATCH-based optimistic CAS
	rdb.Set(ctx, "counter", 0, 0)
	var retries int64
	incr := func() error {
		return rdb.Watch(ctx, func(tx *redis.Tx) error {
			v, err := tx.Get(ctx, "counter").Int()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				p.Set(ctx, "counter", v+1, 0)
				return nil
			})
			return err
		}, "counter")
	}
	var wg2 sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for i := 0; i < iters; i++ {
				for {
					err := incr()
					if errors.Is(err, redis.TxFailedErr) { // ключ изменился между WATCH и EXEC
						atomic.AddInt64(&retries, 1)
						continue
					}
					break
				}
			}
		}()
	}
	wg2.Wait()
	watched, _ := rdb.Get(ctx, "counter").Int()

	fmt.Printf("redis: expected=%d  naive GET/SET=%d (lost %d)  WATCH+CAS=%d (retries %d)\n",
		expected, naive, expected-naive, watched, atomic.LoadInt64(&retries))
}
