// interfaces.go — интерфейсы в Go: маленький интерфейс и неявное его
// удовлетворение, принцип «accept interfaces, return structs», type switch.
//
// В Go нет ключевого слова implements: тип удовлетворяет интерфейсу
// автоматически, если у него есть нужные методы. Это позволяет объявлять
// интерфейс НА СТОРОНЕ ПОТРЕБИТЕЛЯ, не трогая тип, который его реализует.
package interfaces

import (
	"fmt"
	"strings"
)

// Describer — маленький интерфейс (один метод). Мелкие интерфейсы легче
// удовлетворять и подменять в тестах.
type Describer interface {
	Describe() string
}

// Point — конкретный тип. Нигде не указано, что он реализует Describer —
// удовлетворение неявное, по наличию метода Describe.
type Point struct {
	X, Y int
}

// Describe даёт Point неявно удовлетворить Describer.
func (p Point) Describe() string {
	return fmt.Sprintf("точка (%d, %d)", p.X, p.Y)
}

// User — ещё один тип, тоже неявно удовлетворяющий Describer.
type User struct {
	Name string
}

func (u User) Describe() string {
	return "пользователь " + u.Name
}

// JoinDescriptions — иллюстрация «accept interfaces, return structs»:
// принимаем абстракцию (срез Describer, любые типы с методом Describe),
// а возвращаем конкретный тип (string). Функция не знает и не должна знать,
// какие именно типы ей передали.
func JoinDescriptions(items []Describer) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.Describe())
	}
	return strings.Join(parts, "; ")
}

// ClassifyKind — демонстрация type switch: разбираем динамический тип,
// спрятанный за интерфейсом any, и возвращаем читаемую метку.
func ClassifyKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case int:
		return fmt.Sprintf("int(%d)", x)
	case string:
		return fmt.Sprintf("string(%q)", x)
	case Describer:
		// Любой тип, реализующий Describer, попадёт сюда.
		return "Describer: " + x.Describe()
	default:
		return fmt.Sprintf("неизвестный тип %T", x)
	}
}
