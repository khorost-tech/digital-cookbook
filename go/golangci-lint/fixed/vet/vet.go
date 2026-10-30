// Package vet — исправленная пара sample/vet.
package vet

import (
	"fmt"
	"os"
	"strconv"
)

// Report с верным глаголом формата.
func Report(n string) string {
	return n + " записей"
}

// Parse без затенения: ошибка Atoi уходит наружу.
func Parse(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

// Size не разыменовывает nil.
func Size(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Println("нет файла", err)
		return 0
	}
	return info.Size()
}
