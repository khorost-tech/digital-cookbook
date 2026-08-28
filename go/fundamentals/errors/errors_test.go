// errors_test.go — тесты доказывают, что errors.Is / errors.As обходят всю
// обёрнутую цепочку, а errors.Join находит любую из объединённых ошибок.
package errorscookbook

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsFindsSentinelThroughChain: errors.Is находит sentinel сквозь %w-обёртку.
func TestIsFindsSentinelThroughChain(t *testing.T) {
	err := LookupUser("") // вернёт обёрнутый ErrNotFound
	if err == nil {
		t.Fatal("ожидали ошибку, получили nil")
	}
	// Прямое сравнение == не сработало бы: err — это обёртка, не сам sentinel.
	if err == ErrNotFound { //nolint:errorlint // намеренно показываем, что == тут ложно
		t.Fatal("обёртка не должна быть равна sentinel по ==")
	}
	// errors.Is разворачивает цепочку и находит ErrNotFound.
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("errors.Is не нашёл ErrNotFound в %v", err)
	}
}

// TestIsThroughDoubleWrap: Is работает и через несколько уровней обёртки.
func TestIsThroughDoubleWrap(t *testing.T) {
	base := LookupUser("")
	wrapped := fmt.Errorf("слой обработки: %w", base)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Errorf("errors.Is не нашёл ErrNotFound сквозь двойную обёртку: %v", wrapped)
	}
}

// TestAsExtractsTypedError: errors.As достаёт *ValidationError из цепочки
// и даёт доступ к его полям.
func TestAsExtractsTypedError(t *testing.T) {
	err := ValidateUser("") // вернёт обёрнутый *ValidationError
	if err == nil {
		t.Fatal("ожидали ошибку, получили nil")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("errors.As не извлёк *ValidationError из %v", err)
	}
	if verr.Field != "name" {
		t.Errorf("Field = %q, ожидали \"name\"", verr.Field)
	}
}

// TestJoinFindsAnyError: errors.Join объединяет ошибки, а Is/As находят любую.
func TestJoinFindsAnyError(t *testing.T) {
	err := RegisterUser("", "") // обе проверки провалятся
	if err == nil {
		t.Fatal("ожидали объединённую ошибку, получили nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("errors.Is не нашёл ErrNotFound в объединённой ошибке")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("errors.As не нашёл *ValidationError в объединённой ошибке")
	}
}

// TestJoinNilIsNil: если все проверки прошли, Join даёт чистый nil.
func TestJoinNilIsNil(t *testing.T) {
	if err := RegisterUser("u-1", "Alice"); err != nil {
		t.Errorf("ожидали nil при успехе, получили %v", err)
	}
}
