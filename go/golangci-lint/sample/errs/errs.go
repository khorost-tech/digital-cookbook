// Package errs — ошибки, которые теряются молча.
package errs

import (
	"errors"
	"fmt"
	"os"
)

var ErrNotFound = errors.New("не найдено")

// Cleanup удаляет временный файл и не смотрит, получилось ли.
func Cleanup(path string) {
	os.Remove(path) // want errcheck // noise gosec
}

// IsNotFound сравнивает ошибку через ==: обёрнутая ErrNotFound не совпадёт.
func IsNotFound(err error) bool {
	return err == ErrNotFound // want errorlint
}

// Load оборачивает ошибку через %v: цепочка рвётся, errors.Is её не найдёт.
func Load(path string) ([]byte, error) {
	b, err := os.ReadFile(path) // noise gosec
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %v", path, err) // want errorlint
	}
	return b, nil
}
