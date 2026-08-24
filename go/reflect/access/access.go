// Package access показывает базовые операции reflect: чтение полей по имени,
// перебор полей структуры с тегами, изменение значения через адресуемый
// reflect.Value и простой обобщённый обход (Kind switch). Всё это — цена
// гибкости: то, что компилятор делает статически, здесь происходит в рантайме.
package access

import (
	"fmt"
	"reflect"
	"strings"
)

// User — подопытная структура с тегами, как у типовой модели для JSON/ORM.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	admin bool   // неэкспортируемое поле — через reflect читается, но не пишется
}

// FieldByName достаёт значение экспортируемого поля по имени. Возвращает
// ok=false, если поля нет. Ошибиться в имени можно только в рантайме — вся
// проверка ушла из компилятора.
func FieldByName(v any, name string) (any, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	f := rv.FieldByName(name)
	if !f.IsValid() {
		return nil, false
	}
	return f.Interface(), true
}

// JSONTags собирает карту «имя поля → значение json-тега», перебирая поля
// типа. Так работают сериализаторы: читают reflect.StructField.Tag.
func JSONTags(v any) map[string]string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[f.Name], _, _ = strings.Cut(tag, ",") // отрезаем ,omitempty и пр.
	}
	return out
}

// SetString меняет строковое поле по имени. Требует УКАЗАТЕЛЬ: значение
// должно быть адресуемым (settable), иначе reflect паникует. Это третий
// «закон рефлексии»: чтобы менять через reflect, нужен адрес.
func SetString(ptr any, field, value string) error {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("нужен ненулевой указатель, получили %T", ptr)
	}
	f := rv.Elem().FieldByName(field)
	if !f.IsValid() {
		return fmt.Errorf("нет поля %q", field)
	}
	if !f.CanSet() {
		return fmt.Errorf("поле %q неизменяемо (неэкспортируемое?)", field)
	}
	if f.Kind() != reflect.String {
		return fmt.Errorf("поле %q не строка, а %s", field, f.Kind())
	}
	f.SetString(value)
	return nil
}

// Describe обходит любое значение и печатает вид (Kind) — иллюстрация того,
// что reflect работает с динамическим типом, а не статическим.
func Describe(v any) string {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int64:
		return fmt.Sprintf("целое %d", rv.Int())
	case reflect.String:
		return fmt.Sprintf("строка %q", rv.String())
	case reflect.Slice, reflect.Array:
		return fmt.Sprintf("последовательность из %d элементов", rv.Len())
	case reflect.Struct:
		return fmt.Sprintf("структура %s с %d полями", rv.Type().Name(), rv.NumField())
	default:
		return fmt.Sprintf("нечто вида %s", rv.Kind())
	}
}
