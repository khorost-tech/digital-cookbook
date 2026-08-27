// Package beforeafter показывает одну и ту же фичу (выпуск купона + уведомление)
// в двух вариантах: «как обычно» — с зашитыми внутрь часами, случайностью и
// отправкой, и после введения швов — с инъецированными зависимостями. Разница
// не в тестах, а в ДИЗАЙНЕ: во втором варианте точный тест вообще становится
// возможным.
package beforeafter

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Coupon — купон со сроком годности.
type Coupon struct {
	ID        string
	UserID    string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

const (
	couponTTL = 24 * time.Hour
	// notifyURL зашит в код — подменить его в тесте нечем.
	notifyURL = "https://notify.example.com/send"
)

// IssueBefore — «как обычно»: часы и случайность зашиты внутрь. Подменить их,
// не правя саму функцию, нечем, поэтому тест может утверждать только СВОЙСТВА,
// а не значения:
//   - ID случайный → доступен лишь формат (^CPN-[0-9]{6}$), не само значение;
//   - IssuedAt/ExpiresAt из настоящих часов → только границы вокруг вызова;
//   - равенство ExpiresAt == IssuedAt+TTL требовать нельзя: это два разных
//     вызова time.Now(), и их совпадение ничем не гарантировано.
func IssueBefore(userID string) Coupon {
	return Coupon{
		ID:        fmt.Sprintf("CPN-%06d", rand.IntN(1_000_000)), // случайность внутри
		UserID:    userID,
		IssuedAt:  time.Now(),                // часы внутри
		ExpiresAt: time.Now().Add(couponTTL), // и снова часы
	}
}

// NotifyBefore — отправка зашита в функцию: адрес константа, транспорт
// net/http напрямую.
//
// Осторожно с выводом «герметично не протестировать» — это НЕПРАВДА.
// http.Post ходит через глобальный http.DefaultClient, а его Transport
// подменяем: тест TestNotifyBefore_ТолькоЧерезГлобальнуюПодмену перехватывает
// запрос без сети и проходит. Но шов получается ПРОЦЕССНЫЙ — меняет поведение
// всего процесса, запрещает t.Parallel(), опирается на деталь реализации
// (что внутри именно DefaultClient), а URL-константу всё равно не подставить.
// Точная формулировка: тест возможен, но только через хрупкую глобальную
// подмену; явный локальный шов (Sender в after.go) даёт то же без этих минусов.
func NotifyBefore(c Coupon) error {
	body := strings.NewReader(url.Values{
		"to":   {c.UserID},
		"text": {"Ваш купон " + c.ID},
	}.Encode())
	resp, err := http.Post(notifyURL, "application/x-www-form-urlencoded", body)
	if err != nil {
		return fmt.Errorf("отправка уведомления: %w", err)
	}
	defer resp.Body.Close()
	return nil
}
