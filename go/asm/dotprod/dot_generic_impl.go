package dotprod

// dotGeneric — эталонная (golden) реализация. Есть на всех архитектурах,
// служит и fallback-ом, и образцом корректности для asm-версий в тестах.
func dotGeneric(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
