package beforeafter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

// Контрактные тесты боевого адаптера HTTPSender.
//
// Зачем они, если «за швом лежит тонкий адаптер без логики»? Затем, что логика
// там всё-таки есть: кодирование формы, метод, заголовок, проброс context и —
// главное — трактовка кода ответа. Первая версия Send возвращала nil на HTTP
// 500, то есть молча считала недоставленное доставленным. Поймал это вот этот
// тест. Адаптеру нужно меньше тестов, чем доменной логике, но не ноль.

// Форма, метод и заголовок — часть контракта с чужим сервисом.
func TestHTTPSender_ФормаМетодЗаголовок(t *testing.T) {
	var gotMethod, gotCT, gotTo, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotTo, gotText = r.PostFormValue("to"), r.PostFormValue("text")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := HTTPSender{URL: srv.URL, Client: srv.Client()}
	if err := s.Send(context.Background(), "user-1", "Ваш купон CPN-000042"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("метод = %s, want POST", gotMethod)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotTo != "user-1" || gotText != "Ваш купон CPN-000042" {
		t.Errorf("форма: to=%q text=%q", gotTo, gotText)
	}
}

// РЕГРЕССИОННЫЙ тест на реальный дефект: 500 — это НЕ доставка.
func TestHTTPSender_НеДваИксаЭтоОшибка(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadRequest, http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		s := HTTPSender{URL: srv.URL, Client: srv.Client()}
		err := s.Send(context.Background(), "user-1", "текст")
		srv.Close()

		if err == nil {
			t.Errorf("HTTP %d принят за успешную доставку — недоставленное считается доставленным", code)
		}
	}
}

// failingTransport детерминированно возвращает ошибку. Раньше здесь стоял
// закрытый httptest-сервер («порт освободили — соединение не установится»),
// но это ставка на состояние ОС: порт может быть занят кем-то другим, а отказ
// зависит от сетевого стека. В статье про детерминированные тесты такому
// приёму не место — подменяем транспорт и получаем ровно ту ошибку, которую
// хотим, без единого сетевого допущения.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

// Ошибка транспорта должна доехать наружу обёрнутой, а не потеряться.
func TestHTTPSender_ОшибкаТранспорта(t *testing.T) {
	transportErr := errors.New("соединение отвергнуто")
	s := HTTPSender{
		URL:    "https://notify.example.com/send",
		Client: &http.Client{Transport: failingTransport{err: transportErr}},
	}

	err := s.Send(context.Background(), "user-1", "текст")
	if !errors.Is(err, transportErr) {
		t.Errorf("ждали обёрнутую ошибку транспорта, получили %v", err)
	}
}

// Формат ID — НЕ «однострочник, где нечего тестировать». Ветвлений в нём нет
// ни одного, но есть наш собственный контракт: префикс CPN-, шесть цифр с
// ведущими нулями. Сменили префикс, забыли нолик в %6d — и потребители,
// разбирающие ID, молча поедут. Никакой if для этого не нужен.
//
// Формат вынесен в ЧИСТУЮ функцию formatCouponID, поэтому проверяется
// детерминированно — на границах диапазона, а не выборкой случайных значений.
// Это принципиально: регрессия %06d → %6d видна только на малых номерах, и
// случайная выборка ловила бы её вероятностно.
func TestFormatCouponID_Границы(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "CPN-000000"}, // нижняя граница: ведущие нули на месте
		{1, "CPN-000001"}, // %6d вместо %06d сломался бы именно здесь
		{42, "CPN-000042"},
		{99_999, "CPN-099999"},      // на разряд короче — всё ещё шесть цифр
		{idSpace - 1, "CPN-999999"}, // верхняя граница диапазона
	}
	for _, c := range cases {
		if got := formatCouponID(c.n); got != c.want {
			t.Errorf("formatCouponID(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// А генератору остаётся проверить только то, что он и правда отдаёт номер из
// договорённого диапазона через тот самый форматтер. Формат здесь уже не
// проверяем — он доказан выше, детерминированно.
func TestRandomIDGen_ОтдаётIDИзДиапазона(t *testing.T) {
	idFormat := regexp.MustCompile(`^CPN-[0-9]{6}$`)
	g := RandomIDGen{}

	for i := 0; i < 500; i++ {
		if id := g.NewID(); !idFormat.MatchString(id) {
			t.Fatalf("NewID() = %q, ждали формат ^CPN-[0-9]{6}$ (вызов #%d)", id, i)
		}
	}
	// Проверки «а вдруг это константа?» (len(уникальных) >= 2) здесь намеренно
	// НЕТ: она вероятностная. Формально ничто не мешает генератору 500 раз
	// выдать одно значение — вероятность исчезающе мала, но опираться на неё
	// тест не должен. Разнообразие — свойство rand, а не наш контракт.
}

// Отмена контекста прерывает запрос — иначе проброс ctx был бы декоративным.
//
// Синхронизация здесь по СОБЫТИЮ, а не по таймеру: handler сигналит «запрос
// дошёл до меня» через канал entered, и только тогда тест отменяет контекст.
// Раньше тут стоял time.Sleep(20ms) — ставка на то, что за 20 мс запрос
// успеет долететь. На загруженной машине это ровно тот flaky, про который
// написана соседняя статья.
func TestHTTPSender_ОтменаКонтекста(t *testing.T) {
	entered := make(chan struct{}) // handler начал обрабатывать запрос
	release := make(chan struct{}) // тест разрешает handler'у завершиться

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release // держим запрос, пока тест не отменит контекст
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered // отменяем ровно тогда, когда запрос точно в обработке
		cancel()
	}()

	s := HTTPSender{URL: srv.URL, Client: srv.Client()}
	if err := s.Send(ctx, "user-1", "текст"); !errors.Is(err, context.Canceled) {
		t.Errorf("ждали context.Canceled, получили %v", err)
	}
}
