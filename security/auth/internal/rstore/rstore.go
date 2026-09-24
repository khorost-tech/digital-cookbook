package rstore

import "github.com/redis/go-redis/v9"

func New(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}
