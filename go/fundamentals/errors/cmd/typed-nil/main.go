// Демо к мини-серии «Основы Go вглубь» (khorost.tech), раздел «Обработка ошибок».
//
// Знаменитый баг «typed nil»: функция возвращает КОНКРЕТНЫЙ тип *MyError,
// равный nil, но результат присваивается интерфейсу error. Проверка
// `if err != nil` даёт TRUE, хотя «ошибки как бы нет».
//
// Почему так: интерфейсное значение — это пара (тип, значение). Когда в
// интерфейс кладут nil-указатель *MyError, тип пары НЕ nil (он равен
// *MyError), поэтому интерфейс не равен nil. Сравнение с nil у интерфейса
// истинно ТОЛЬКО когда и тип, и значение пусты.
//
// Что печатает запуск (go run ./errors/cmd/typed-nil):
//
//	badErr != nil  -> true   (ловушка: указатель внутри nil, а интерфейс — нет)
//	goodErr != nil -> false  (правильно: вернули буквальный nil интерфейса)
//
// Как чинить: возвращать тип error напрямую и присваивать nil ему, а не
// «протаскивать» конкретный nil-указатель через промежуточную переменную.
package main

import "fmt"

// MyError — конкретный тип ошибки.
type MyError struct{ msg string }

func (e *MyError) Error() string { return e.msg }

// makeBad демонстрирует ловушку: объявляем nil-указатель *MyError и
// возвращаем его как error. Интерфейс получает пару (*MyError, nil) —
// и это НЕ nil-интерфейс.
func makeBad() error {
	var p *MyError = nil // ошибки нет — указатель nil
	return p             // но возвращаем через error: пара (тип=*MyError, значение=nil)
}

// makeGood — как правильно: возвращаем буквальный nil типа error.
func makeGood() error {
	return nil // пара (тип=nil, значение=nil) — настоящий nil-интерфейс
}

func main() {
	badErr := makeBad()
	goodErr := makeGood()

	// Несмотря на то что внутри badErr лежит nil-указатель, интерфейс не nil.
	fmt.Printf("badErr != nil  -> %v   (внутри указатель nil, но тип пары = *MyError)\n", badErr != nil)
	fmt.Printf("goodErr != nil -> %v\n", goodErr != nil)

	if badErr != nil {
		fmt.Println("ветка ошибки СРАБОТАЛА, хотя фактической ошибки нет — это и есть ловушка")
	}
}
