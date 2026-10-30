// Package dead — исправленная пара sample/dead.
package dead

import "strings"

// Normalize без лишнего присваивания.
func Normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Same сравнивает два разных аргумента.
func Same(a, b string) bool {
	return strings.EqualFold(a, b)
}
