// Демо к мини-серии «Основы Go вглубь» (khorost.tech), раздел «Обработка ошибок».
//
// panic / recover: как перехватить панику в defer и превратить её в обычную
// ошибку (error), не роняя процесс. Это стандартный приём на границе пакета —
// внутри может паниковать сторонний код, наружу отдаём аккуратный error.
//
// Что печатает запуск (go run ./errors/cmd/panic-recover):
//
//	safeDivide(10, 2) = 5, err = <nil>
//	safeDivide(10, 0) = 0, err = восстановлено из паники: деление на ноль
//	дошли до конца main — процесс не упал
//
// Важно: recover() что-то возвращает ТОЛЬКО при вызове напрямую из
// отложенной (defer) функции во время разворачивания паники. Вне паники
// recover() возвращает nil.
package main

import (
	"errors"
	"fmt"
)

// safeDivide делит a на b. При делении на ноль встроенная операция паникует;
// defer с recover перехватывает панику и превращает её в error через
// именованный возвращаемый параметр err.
func safeDivide(a, b int) (result int, err error) {
	defer func() {
		if r := recover(); r != nil {
			// r — значение, переданное в panic (тут — runtime error).
			err = fmt.Errorf("восстановлено из паники: %v", r)
		}
	}()

	if b == 0 {
		// Явная паника, чтобы сообщение было читаемым; результат тот же.
		panic(errors.New("деление на ноль"))
	}
	return a / b, nil
}

func main() {
	if r, err := safeDivide(10, 2); err == nil {
		fmt.Printf("safeDivide(10, 2) = %d, err = %v\n", r, err)
	}

	r, err := safeDivide(10, 0)
	fmt.Printf("safeDivide(10, 0) = %d, err = %v\n", r, err)

	fmt.Println("дошли до конца main — процесс не упал")
}
