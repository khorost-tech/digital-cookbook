//go:build !amd64 && !arm64

package dotprod

// Dot вычисляет скалярное произведение a и b (равной длины).
// На архитектурах без asm-реализации используется чистый Go.
func Dot(a, b []float64) float64 {
	if len(a) != len(b) {
		panic("dotprod: length mismatch")
	}
	return dotGeneric(a, b)
}
