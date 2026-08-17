package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"khorost.tech/go-testing/cache"
)

// fakeRepo — ручной мок ProfileRepository: маленький интерфейс, тривиальный
// фейк поверх map. calls считает число обращений к GetByID, чтобы тесты
// могли проверить, что промах кэша идёт в репозиторий ровно один раз, а
// попадание в кэш — ни разу. err, если задан, заставляет GetByID вернуть
// эту (произвольную, не обязательно ErrNotFound) ошибку вместо поиска в
// profiles — используется для проверки, что Service пробрасывает наружу
// любую ошибку репозитория как есть, а не только ErrNotFound.
type fakeRepo struct {
	profiles map[int64]cache.Profile
	calls    int
	err      error
}

func (f *fakeRepo) GetByID(_ context.Context, id int64) (cache.Profile, error) {
	f.calls++
	if f.err != nil {
		return cache.Profile{}, f.err
	}
	p, ok := f.profiles[id]
	if !ok {
		return cache.Profile{}, cache.ErrNotFound
	}
	return p, nil
}

// fakeCache — ручной мок Cache поверх map в памяти.
//
// getErr, если задан, заставляет Get всегда возвращать эту ошибку (Service
// должен трактовать это как промах кэша и идти в репозиторий, а не
// фейлить запрос). setErr, если задан, заставляет Set вернуть эту ошибку
// БЕЗ записи значения в data — так же, как повела бы себя настоящая
// реализация (запись не прошла — значения в кэше нет), — Service при этом
// не должен фейлить запрос (кэш best-effort). setCalls/gotTTL фиксируют
// последний вызов Set, чтобы проверить, что TTL из Service реально
// доходит до Cache.Set.
type fakeCache struct {
	data map[string]string

	getErr error
	setErr error

	setCalls int
	gotTTL   time.Duration
}

func (f *fakeCache) Get(_ context.Context, k string) (string, bool, error) {
	if f.getErr != nil {
		return "", false, f.getErr
	}
	v, ok := f.data[k]
	return v, ok, nil
}

func (f *fakeCache) Set(_ context.Context, k, v string, ttl time.Duration) error {
	f.setCalls++
	f.gotTTL = ttl
	if f.setErr != nil {
		return f.setErr
	}
	f.data[k] = v
	return nil
}

// mustMarshalProfile — вспомогательный ассерт для подготовки данных теста.
func mustMarshalProfile(t *testing.T, p cache.Profile) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	return string(raw)
}

func TestGetProfile(t *testing.T) {
	t.Parallel()

	alice := cache.Profile{ID: 1, Name: "Alice", Email: "alice@example.com"}
	errDBDown := errors.New("db: connection refused")
	errRedisDown := errors.New("redis: connection refused")
	errRedisReadonly := errors.New("redis: READONLY replica")

	cases := []struct {
		name string
		// setup строит fakeRepo/fakeCache для конкретного случая.
		setup func(t *testing.T) (*fakeRepo, *fakeCache)
		id    int64

		wantProfile   cache.Profile
		wantFromCache bool
		wantErr       error
		wantRepoCalls int
		wantCached    bool // должен ли ключ появиться в кэше после вызова
	}{
		{
			name: "cache miss reads through repo and backfills cache",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{1: alice}}
				c := &fakeCache{data: map[string]string{}}
				return repo, c
			},
			id:            1,
			wantProfile:   alice,
			wantFromCache: false,
			wantErr:       nil,
			wantRepoCalls: 1,
			wantCached:    true,
		},
		{
			name: "cache hit does not call repo",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{}} // repo пуст — если тронут, вернёт ErrNotFound
				c := &fakeCache{data: map[string]string{
					cache.CacheKey(1): mustMarshalProfile(t, alice),
				}}
				return repo, c
			},
			id:            1,
			wantProfile:   alice,
			wantFromCache: true,
			wantErr:       nil,
			wantRepoCalls: 0,
			wantCached:    true, // уже был в кэше до вызова
		},
		{
			name: "unknown user propagates ErrNotFound",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{}}
				c := &fakeCache{data: map[string]string{}}
				return repo, c
			},
			id:            42,
			wantProfile:   cache.Profile{},
			wantFromCache: false,
			wantErr:       cache.ErrNotFound,
			wantRepoCalls: 1,
			wantCached:    false,
		},
		{
			name: "cache get error is treated as miss, request still succeeds",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{1: alice}}
				c := &fakeCache{data: map[string]string{}, getErr: errRedisDown}
				return repo, c
			},
			id:            1,
			wantProfile:   alice,
			wantFromCache: false,
			wantErr:       nil,
			wantRepoCalls: 1,
			wantCached:    true, // Get упал, но Set после похода в репозиторий отработал штатно
		},
		{
			name: "corrupted json in cache is treated as miss, repo backfills valid value",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{1: alice}}
				c := &fakeCache{data: map[string]string{
					cache.CacheKey(1): "not-valid-json{{{",
				}}
				return repo, c
			},
			id:            1,
			wantProfile:   alice,
			wantFromCache: false,
			wantErr:       nil,
			wantRepoCalls: 1,
			wantCached:    true, // битое значение перезаписано валидным JSON
		},
		{
			name: "cache set error does not fail the request (best-effort)",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{profiles: map[int64]cache.Profile{1: alice}}
				c := &fakeCache{data: map[string]string{}, setErr: errRedisReadonly}
				return repo, c
			},
			id:            1,
			wantProfile:   alice,
			wantFromCache: false,
			wantErr:       nil,
			wantRepoCalls: 1,
			wantCached:    false, // Set упал — значения в кэше нет, но запрос не зафейлился
		},
		{
			name: "repo arbitrary error propagates as-is (not just ErrNotFound)",
			setup: func(t *testing.T) (*fakeRepo, *fakeCache) {
				t.Helper()
				repo := &fakeRepo{err: errDBDown}
				c := &fakeCache{data: map[string]string{}}
				return repo, c
			},
			id:            7,
			wantProfile:   cache.Profile{},
			wantFromCache: false,
			wantErr:       errDBDown,
			wantRepoCalls: 1,
			wantCached:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo, c := tc.setup(t)

			svc := &cache.Service{Repo: repo, Cache: c, TTL: time.Minute}

			got, fromCache, err := svc.GetProfile(context.Background(), tc.id)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("GetProfile(%d) error = %v, want %v", tc.id, err, tc.wantErr)
			}
			if tc.wantErr == nil && got != tc.wantProfile {
				t.Errorf("GetProfile(%d) profile = %+v, want %+v", tc.id, got, tc.wantProfile)
			}
			if fromCache != tc.wantFromCache {
				t.Errorf("GetProfile(%d) fromCache = %v, want %v", tc.id, fromCache, tc.wantFromCache)
			}
			if repo.calls != tc.wantRepoCalls {
				t.Errorf("repo.calls = %d, want %d", repo.calls, tc.wantRepoCalls)
			}
			// Проверяем содержимое кэша напрямую через data, а не через
			// c.Get: у части кейсов выше Get намеренно ломает (getErr), и
			// повторный вызов через Get дал бы ложное "не закешировано"
			// даже если Set успешно записал значение.
			raw, cached := c.data[cache.CacheKey(tc.id)]
			if cached != tc.wantCached {
				t.Errorf("cache has key %q = %v, want %v", cache.CacheKey(tc.id), cached, tc.wantCached)
			}
			// Мало проверить НАЛИЧИЕ ключа: у кейса с битым JSON ключ
			// присутствует и ДО вызова, поэтому проверка одного наличия
			// осталась бы зелёной, даже если сервис перестал перезаписывать
			// повреждённое значение. Поэтому для успешных кейсов с ключом
			// декодируем реально лежащее значение и сверяем с профилем —
			// это доказывает, что бэкфилл записал именно валидный JSON.
			if cached && tc.wantErr == nil {
				var stored cache.Profile
				if err := json.Unmarshal([]byte(raw), &stored); err != nil {
					t.Fatalf("значение в кэше не декодируется в Profile: %v (raw=%q)", err, raw)
				}
				if stored != tc.wantProfile {
					t.Errorf("кэшированный профиль = %+v, want %+v", stored, tc.wantProfile)
				}
			}
		})
	}
}

// TestGetProfile_PassesTTLToCache проверяет, что TTL, сконфигурированный в
// Service, реально доходит до Cache.Set при бэкфилле после промаха кэша —
// а не теряется/подменяется где-то по дороге.
func TestGetProfile_PassesTTLToCache(t *testing.T) {
	t.Parallel()

	alice := cache.Profile{ID: 1, Name: "Alice", Email: "alice@example.com"}
	repo := &fakeRepo{profiles: map[int64]cache.Profile{1: alice}}
	c := &fakeCache{data: map[string]string{}}
	wantTTL := 42 * time.Second

	svc := &cache.Service{Repo: repo, Cache: c, TTL: wantTTL}

	if _, _, err := svc.GetProfile(context.Background(), 1); err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if c.setCalls != 1 {
		t.Fatalf("Cache.Set calls = %d, want 1", c.setCalls)
	}
	if c.gotTTL != wantTTL {
		t.Errorf("Cache.Set ttl = %v, want %v", c.gotTTL, wantTTL)
	}
}
