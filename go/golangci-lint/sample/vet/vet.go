// Package vet — ошибки, которые видит go vet, и две, которые он видит только по настройке.
package vet

import (
	"fmt"
	"os"
	"strconv"
)

// Report путает глагол формата.
func Report(n string) string {
	return fmt.Sprintf("%d записей", n) // want govet staticcheck
}

// Parse затеняет err: ошибка Atoi теряется, наружу уходит nil.
func Parse(s string) (int, error) {
	var err error
	n := 0
	if s != "" {
		n, err := strconv.Atoi(s) // want govet
		_ = n
		_ = err
	}
	return n, err
}

// Size разыменовывает заведомо nil: проверка выше доказывает, что info == nil.
func Size(path string) int64 {
	info, err := os.Stat(path)
	if info == nil {
		fmt.Println("нет файла", err)
		return info.Size() // want govet
	}
	return info.Size()
}
