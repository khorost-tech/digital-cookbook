package main

import "fmt"

// assertFailFast — fail-loud: печатает провал и завершает процесс паникой
// (ненулевой код выхода). Тот же паттерн, что ../materialized-views/report.go
// и ../go/mergetree/report.go — каждый программный ассерт сценария идёт
// через эту функцию.
func assertFailFast(cond bool, format string, args ...any) {
	if !cond {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("[ASSERT FAILED] %s\n", msg)
		panic("assertion failed: " + msg)
	}
	fmt.Printf("[assert OK] %s\n", fmt.Sprintf(format, args...))
}
