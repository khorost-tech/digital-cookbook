// Package dead — присваивания и код, которые ни на что не влияют.
package dead

import "strings"

// Normalize обрезает пробелы и приводит строку к нижнему регистру. Обрезанное
// значение тут же перезаписывается — пробелы остаются.
func Normalize(s string) string {
	out := strings.TrimSpace(s) // want ineffassign staticcheck
	out = strings.ToLower(s)
	return out
}

// legacyFormat никто не вызывает.
func legacyFormat(s string) string { return "[" + s + "]" } // want unused // kind dead

// Same сравнивает a и b без учёта регистра — но сравнивает a само с собой.
func Same(a, b string) bool {
	return strings.ToLower(a) == strings.ToLower(a) // want staticcheck
}
