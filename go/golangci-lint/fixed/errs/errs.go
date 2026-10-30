// Package errs — исправленная пара sample/errs.
package errs

import (
	"errors"
	"fmt"
	"os"
)

var ErrNotFound = errors.New("не найдено")

// Cleanup возвращает ошибку удаления.
func Cleanup(path string) error {
	return os.Remove(path)
}

// IsNotFound находит ErrNotFound и в обёрнутой ошибке.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Load оборачивает ошибку через %w.
func Load(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	return b, nil
}
