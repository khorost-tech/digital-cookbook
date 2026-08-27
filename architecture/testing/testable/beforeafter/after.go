package beforeafter

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- ШВЫ: три маленьких интерфейса на границах ---
// Каждый описан у ПОТРЕБИТЕЛЯ и содержит ровно то, что нужно Issuer.

// Clock — источник времени. Один метод: больше Issuer от часов ничего не надо.
type Clock interface {
	Now() time.Time
}

// IDGen — генератор идентификаторов купона.
type IDGen interface {
	NewID() string
}

// Sender — отправка уведомления. Наш собственный интерфейс на границе:
// чужой SDK/транспорт прячется за ним (don't mock what you don't own).
type Sender interface {
	Send(ctx context.Context, to, text string) error
}

// Issuer — та же фича, но зависимости пришли снаружи. Ни часов, ни rand,
// ни http внутри логики: только вызовы швов.
type Issuer struct {
	Clock  Clock
	IDs    IDGen
	Sender Sender
	TTL    time.Duration
}

// Issue выпускает купон и уведомляет пользователя. Функция стала
// детерминированной относительно своих зависимостей: подставь фиксированные
// часы и генератор — получишь предсказуемый до поля результат.
func (i Issuer) Issue(ctx context.Context, userID string) (Coupon, error) {
	now := i.Clock.Now()
	c := Coupon{
		ID:        i.IDs.NewID(),
		UserID:    userID,
		IssuedAt:  now,
		ExpiresAt: now.Add(i.TTL), // ОДИН вызов часов — срок точно = IssuedAt+TTL
	}
	if err := i.Sender.Send(ctx, userID, "Ваш купон "+c.ID); err != nil {
		return Coupon{}, fmt.Errorf("отправка уведомления: %w", err)
	}
	return c, nil
}

// --- Боевые реализации швов: тонкие, без логики ---

// SystemClock — настоящие часы.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// idSpace — размер пространства номеров купона: [0, idSpace).
const idSpace = 1_000_000

// formatCouponID — ЧИСТАЯ функция: номер → идентификатор. Весь наш контракт
// формата (префикс, шесть цифр с ведущими нулями) живёт здесь.
//
// Вынесен он не ради красоты: пока формат был вкомпилирован в NewID рядом с
// rand, проверить его можно было только выборкой случайных значений — то есть
// вероятностно. Регрессия %06d → %6d видна лишь на малых номерах, и выборка
// ловила бы её с некоторой вероятностью, а не гарантированно. Чистая функция
// проверяется детерминированно, на границах (0 и idSpace-1).
//
// Это ровно тот же приём, что и в пакете functionalcore: вынести решение из
// окружения недетерминизма — и оно становится тривиально проверяемым.
func formatCouponID(n int64) string { return fmt.Sprintf("CPN-%06d", n) }

// RandomIDGen — случайные ID, как было раньше. Своего формата больше не знает:
// только выбирает номер и отдаёт его чистому форматтеру.
type RandomIDGen struct{}

func (RandomIDGen) NewID() string { return formatCouponID(int64(rand.IntN(idSpace))) }

// HTTPSender — реальная отправка. Вся возня с сетью заперта здесь, за швом,
// и в доменную логику не протекает. URL стал полем, а не константой.
//
// ВАЖНО: это НЕ «тонкий адаптер, в котором нечему ломаться». Здесь есть свой
// контракт — кодирование формы, метод, заголовок, проброс context, выбор
// клиента и трактовка кода ответа, — и он ломается. Первая версия этого кода
// возвращала nil на HTTP 500, то есть считала недоставленное уведомление
// доставленным; поймал это контрактный тест (adapter_test.go), а не ревью.
// Адаптерам нужно меньше тестов, чем логике, но не ноль.
type HTTPSender struct {
	URL    string
	Client *http.Client
}

func (s HTTPSender) Send(ctx context.Context, to, text string) error {
	body := strings.NewReader(url.Values{"to": {to}, "text": {text}}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cl := s.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("запрос уведомления: %w", err)
	}
	defer resp.Body.Close()
	// Успех — только 2xx. Без этой проверки 500 выглядел бы как доставка.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("уведомление не принято: %s", resp.Status)
	}
	return nil
}
