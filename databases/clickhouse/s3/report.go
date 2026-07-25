package main

import "fmt"

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// assertFailFast — fail-loud: печатает провал и завершает процесс паникой
// (ненулевой код выхода). Тот же паттерн, что во всех предыдущих стендах
// серии (см. ../materialized-views/report.go, ../ops-stand и т.д.) —
// каждый программный ассерт сценария идёт через эту функцию.
func assertFailFast(cond bool, format string, args ...any) {
	if !cond {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("[ASSERT FAILED] %s\n", msg)
		panic("assertion failed: " + msg)
	}
	fmt.Printf("[assert OK] %s\n", fmt.Sprintf(format, args...))
}
