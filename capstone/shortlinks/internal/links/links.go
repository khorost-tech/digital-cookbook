// Package links — домен: короткая ссылка, её создание и разрешение кода в URL.
// Инфраструктура (база, кэш, брокер) — за интерфейсами; здесь только правила.
package links

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"time"
)

var (
	ErrInvalidURL = errors.New("недопустимый URL")
	ErrNotFound   = errors.New("ссылка не найдена")
	ErrCodeTaken  = errors.New("код уже занят")
)

type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// Store — постоянное хранилище ссылок. Create записывает ссылку и, если событие
// создания идёт через outbox, строку outbox — в одной транзакции.
type Store interface {
	Create(ctx context.Context, l Link) error
	Get(ctx context.Context, code string) (Link, error)
	Clicks(ctx context.Context, code string) (int64, error)
}

// Resolver — чтение по коду с кэшем перед хранилищем.
type Resolver interface {
	Resolve(ctx context.Context, code string) (string, error)
}

// Clicks — публикация события клика; не должна задерживать редирект.
type Clicks interface {
	Click(ctx context.Context, code string, at time.Time)
}

type Service struct {
	Store    Store
	Resolver Resolver
	Clicks   Clicks
	Now      func() time.Time
}

const codeLen = 7
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func NewCode() string {
	b := make([]byte, codeLen)
	if _, err := rand.Read(b); err != nil {
		panic(err) // источник случайности ОС недоступен — продолжать нельзя
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func ValidateURL(raw string) error {
	if len(raw) == 0 || len(raw) > 2048 {
		return ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrInvalidURL
	}
	return nil
}

// Create — новая ссылка со случайным кодом; при редкой коллизии кода — повтор.
func (s *Service) Create(ctx context.Context, raw string) (Link, error) {
	if err := ValidateURL(raw); err != nil {
		return Link{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		l := Link{Code: NewCode(), URL: raw, CreatedAt: s.Now().UTC()}
		err := s.Store.Create(ctx, l)
		if errors.Is(err, ErrCodeTaken) {
			continue
		}
		return l, err
	}
	return Link{}, ErrCodeTaken
}

// Redirect — URL по коду и событие клика; событие не влияет на ответ.
func (s *Service) Redirect(ctx context.Context, code string) (string, error) {
	target, err := s.Resolver.Resolve(ctx, code)
	if err != nil {
		return "", err
	}
	s.Clicks.Click(ctx, code, s.Now())
	return target, nil
}

func (s *Service) Stats(ctx context.Context, code string) (int64, error) {
	if _, err := s.Store.Get(ctx, code); err != nil {
		return 0, err
	}
	return s.Store.Clicks(ctx, code)
}
