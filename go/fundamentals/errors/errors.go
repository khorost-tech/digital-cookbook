// errors.go — идиоматичная обработка ошибок в Go: sentinel-ошибки,
// обёртывание через %w, распаковка цепочки через errors.Is / errors.As,
// объединение нескольких ошибок через errors.Join (Go 1.20+).
//
// Пакет назван errorscookbook, чтобы не конфликтовать со стандартным
// пакетом errors, который здесь импортируется.
package errorscookbook

import (
	"errors"
	"fmt"
)

// ErrNotFound — sentinel-ошибка: заранее объявленное значение, с которым
// сравнивают через errors.Is. Экспортируется, чтобы вызывающий код мог
// проверить именно этот случай, не разбирая текст сообщения.
var ErrNotFound = errors.New("запись не найдена")

// ValidationError — типизированная ошибка: несёт структурированные детали
// (поле и причину), которые вызывающий достаёт через errors.As.
type ValidationError struct {
	Field  string
	Reason string
}

// Error реализует интерфейс error.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("поле %q: %s", e.Field, e.Reason)
}

// LookupUser имитирует обращение к хранилищу. Для неизвестного id
// возвращает ErrNotFound, обёрнутую в контекст через %w: обёртка добавляет
// сообщение, но сохраняет sentinel внутри цепочки для errors.Is.
func LookupUser(id string) error {
	if id == "" {
		// %w сохраняет ErrNotFound в цепочке; errors.Is найдёт его сквозь обёртку.
		return fmt.Errorf("поиск пользователя %q: %w", id, ErrNotFound)
	}
	return nil
}

// ValidateUser имитирует валидацию и на пустом имени возвращает
// *ValidationError, обёрнутую в контекст. errors.As достанет её из цепочки.
func ValidateUser(name string) error {
	if name == "" {
		verr := &ValidationError{Field: "name", Reason: "не может быть пустым"}
		return fmt.Errorf("валидация пользователя: %w", verr)
	}
	return nil
}

// RegisterUser выполняет обе проверки и объединяет их результаты через
// errors.Join: в возвращённой ошибке живут ОБЕ причины, и errors.Is найдёт
// любую из них. Если обе проверки прошли, возвращает nil.
func RegisterUser(id, name string) error {
	// errors.Join(nil, nil) == nil, поэтому при успехе получаем чистый nil.
	return errors.Join(LookupUser(id), ValidateUser(name))
}
