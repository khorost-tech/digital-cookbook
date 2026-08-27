package beforeafter

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Тест «до»: выжимаем из зашитых зависимостей МАКСИМУМ. Вопреки соблазну
// сказать «тут ничего не проверить», проверить можно немало — и этот тест
// специально написан настолько строго, насколько дизайн вообще позволяет:
// точный ФОРМАТ ID (а не только префикс) и границы времени, снятые вокруг
// вызова.
//
// Чего он всё равно не может: сверить ЗНАЧЕНИЕ ID (оно случайно) и точное
// время (часы внутри). Остаются форма и диапазон — свойства, а не значения.
// Регрессия внутри этих рамок (другой купон, сдвиг TTL на секунду) пройдёт
// незамеченной.
func TestIssueBefore_МаксимумВозможного(t *testing.T) {
	idFormat := regexp.MustCompile(`^CPN-[0-9]{6}$`)

	before := time.Now()
	c := IssueBefore("user-1")
	after := time.Now()

	// 1) ID — точный ФОРМАТ, но не значение: rand внутри функции
	if !idFormat.MatchString(c.ID) {
		t.Errorf("ID = %q, ждали формат ^CPN-[0-9]{6}$", c.ID)
	}
	// 2) UserID проброшен — единственное, что тест задаёт сам
	if c.UserID != "user-1" {
		t.Errorf("UserID = %q", c.UserID)
	}
	// 3) время — только ГРАНИЦЫ, снятые вокруг вызова
	if c.IssuedAt.Before(before) || c.IssuedAt.After(after) {
		t.Errorf("IssuedAt=%v вне интервала [%v, %v]", c.IssuedAt, before, after)
	}
	if c.ExpiresAt.Before(before.Add(couponTTL)) || c.ExpiresAt.After(after.Add(couponTTL)) {
		t.Error("ExpiresAt вне ожидаемого интервала")
	}
	// А вот РАВЕНСТВО ExpiresAt == IssuedAt+TTL требовать нельзя: это два
	// РАЗНЫХ вызова time.Now() внутри функции, и их совпадение ничем не
	// гарантировано (случайно совпасть они могут, но полагаться на это тест
	// не вправе). Инвариант недоказуем не потому, что тест ленив, а потому,
	// что дизайн его не гарантирует.
}

// stubTransport перехватывает исходящий запрос, не выходя в сеть.
type stubTransport struct {
	gotURL  string
	gotBody string
}

func (s *stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	s.gotURL, s.gotBody = r.URL.String(), string(b)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// Главный нюанс, который легко превратить в неправду.
//
// Соблазнительно заявить «NotifyBefore герметично не протестировать». Это НЕ
// так: http.Post ходит через глобальный http.DefaultClient, а его Transport
// подменяем — тест ниже реально перехватывает запрос без сети и проходит.
//
// Но посмотрите, ЦЕНОЙ чего:
//   - шов ПРОЦЕССНЫЙ: подмена меняет поведение всего процесса, а не одного
//     объекта; забыли восстановить — поедут чужие тесты;
//   - нельзя t.Parallel(): параллельные тесты подрались бы за глобал;
//   - тест опирается на ДЕТАЛЬ РЕАЛИЗАЦИИ (что http.Post использует именно
//     DefaultClient) — переход на свой http.Client внутри функции молча
//     сломает подмену, хотя поведение не изменится;
//   - URL остаётся константой: адрес можно только сверить, но не подставить.
//
// Верная формулировка — не «невозможно», а «возможно только через хрупкую
// глобальную подмену». Явный локальный шов (Sender в after.go) даёт то же
// самое без единого из этих минусов.
func TestNotifyBefore_ТолькоЧерезГлобальнуюПодмену(t *testing.T) {
	// НЕ t.Parallel() — подмена глобальная.
	orig := http.DefaultClient.Transport
	stub := &stubTransport{}
	http.DefaultClient.Transport = stub
	t.Cleanup(func() { http.DefaultClient.Transport = orig })

	if err := NotifyBefore(Coupon{ID: "CPN-000042", UserID: "user-1"}); err != nil {
		t.Fatalf("NotifyBefore: %v", err)
	}

	if !strings.Contains(stub.gotBody, "to=user-1") || !strings.Contains(stub.gotBody, "CPN-000042") {
		t.Errorf("перехваченное тело: %q", stub.gotBody)
	}
	// URL зашит константой — тест может лишь убедиться, что он тот самый,
	// но не подставить свой.
	if stub.gotURL != notifyURL {
		t.Errorf("URL = %q, want %q", stub.gotURL, notifyURL)
	}
}
