// Демонстрационный клиент go-redis: read-through кэш профиля с бесконечным циклом.
// Один и тот же код работает и с Cluster, и с Sentinel — режим выбирается через REDIS_MODE.
// Клиент НИКОГДА не падает на ошибке: при обрыве/failover он логирует ошибку и продолжает,
// чтобы было видно окно недоступности и последующий reconnect.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const key = "user:42"

func main() {
	rdb := build()
	defer rdb.Close()

	fmt.Printf("[go] mode=%s — read-through цикл, Ctrl+C для выхода\n", getenv("REDIS_MODE", "cluster"))
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	for range tick.C {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		val, err := rdb.Get(ctx, key).Result()
		switch {
		case err == redis.Nil: // штатный промах кэша, НЕ ошибка соединения
			v := fmt.Sprintf("profile@%s", time.Now().Format("15:04:05"))
			if setErr := rdb.Set(ctx, key, v, 30*time.Second).Err(); setErr != nil {
				fmt.Printf("[go] MISS, SET error: %v\n", setErr)
			} else {
				fmt.Printf("[go] MISS -> set %q\n", v)
			}
		case err != nil: // обрыв, timeout, CLUSTERDOWN, failover-окно — продолжаем
			fmt.Printf("[go] ERROR: %v\n", err)
		default:
			fmt.Printf("[go] HIT %q\n", val)
		}
		cancel()
	}
}

// build создаёт cluster- или failover-клиент. Оба реализуют redis.UniversalClient.
func build() redis.UniversalClient {
	switch getenv("REDIS_MODE", "cluster") {
	case "sentinel":
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:            getenv("REDIS_MASTER", "mymaster"),
			SentinelAddrs:         split(getenv("REDIS_SENTINELS", "127.0.0.1:26379")),
			DialTimeout:           2 * time.Second,
			ReadTimeout:           500 * time.Millisecond,
			WriteTimeout:          500 * time.Millisecond,
			ContextTimeoutEnabled: true, // уважать deadline из context в цикле
		})
	default:
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:                 split(getenv("REDIS_ADDRS", "127.0.0.1:6379")),
			DialTimeout:           2 * time.Second,
			ReadTimeout:           500 * time.Millisecond,
			WriteTimeout:          500 * time.Millisecond,
			MaxRedirects:          3,    // лимит MOVED/ASK-редиректов
			ContextTimeoutEnabled: true, // уважать deadline из context в цикле
		})
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func split(csv string) []string { return strings.Split(csv, ",") }
