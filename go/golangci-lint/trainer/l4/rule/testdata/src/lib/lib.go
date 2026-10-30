package lib

import "errors"

func Parse(s string) int {
	if s == "" {
		panic("пустая строка") // want `panic в библиотечном пакете`
	}
	return len(s)
}

func Must(err error) {
	if err != nil {
		panic(err) // want `panic в библиотечном пакете`
	}
}

// panicf — своя функция с похожим именем, не встроенная panic.
func panicf(string) error { return errors.New("x") }

func Safe() error { return panicf("нет") }
