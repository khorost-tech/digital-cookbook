// Package cache — cache-aside перед хранилищем: чтение из Redis, при промахе — из
// базы и запись в Redis с TTL. Одновременные промахи по одному коду склеиваются
// singleflight: в базу идёт один запрос, остальные ждут его результат.
package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"shortlinks/internal/links"
)

type Resolver struct {
	Redis        *redis.Client // nil — без кэша
	Store        links.Store
	TTL          time.Duration
	Singleflight bool
	SlowLoad     time.Duration // задержка чтения из базы при промахе (опыт)
	LoadTimeout  time.Duration // срок общей загрузки singleflight: свой, не первого клиента
	group        singleflight.Group
	Hits, Misses atomic.Int64
}

func key(code string) string { return "link:" + code }

func (r *Resolver) Resolve(ctx context.Context, code string) (string, error) {
	if r.Redis != nil {
		v, err := r.Redis.Get(ctx, key(code)).Result()
		if err == nil {
			r.Hits.Add(1)
			return v, nil
		}
		if !errors.Is(err, redis.Nil) {
			// кэш недоступен — идём в базу, а не отказываем: кэш ускоряет, но не держит сервис
			r.Misses.Add(1)
			return r.load(ctx, code, false)
		}
		r.Misses.Add(1)
	}
	if r.Singleflight {
		ch := r.group.DoChan(code, func() (any, error) {
			// общая загрузка не наследует ни отмену, ни срок первого клиента — у неё свой срок
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.LoadTimeout)
			defer cancel()
			return r.load(lctx, code, true)
		})
		select {
		case res := <-ch:
			if res.Err != nil {
				return "", res.Err
			}
			return res.Val.(string), nil
		case <-ctx.Done():
			// клиент ушёл или истёк его срок: перестаём ждать, загрузка для остальных продолжается
			return "", ctx.Err()
		}
	}
	return r.load(ctx, code, true)
}

func (r *Resolver) load(ctx context.Context, code string, fill bool) (string, error) {
	if r.SlowLoad > 0 {
		select {
		case <-time.After(r.SlowLoad):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	l, err := r.Store.Get(ctx, code)
	if err != nil {
		return "", err
	}
	if fill && r.Redis != nil {
		r.Redis.Set(ctx, key(code), l.URL, r.TTL)
	}
	return l.URL, nil
}
