package access

import (
	"reflect"
	"testing"
)

// Раздельные sink'и, чтобы компилятор не выкинул операции. strSink — типизирован,
// поэтому прямое чтение НЕ боксится (иначе бенч мерил бы аллокацию string→any,
// а не сам доступ к полю). anySink — для reflect, где .Interface() всё равно
// возвращает any.
var (
	strSink string
	anySink any
)

// --- ЧТЕНИЕ ПОЛЯ ---

// BenchmarkReadDirect — прямой доступ к полю. Эталон: компилятор знает
// смещение поля, это одна инструкция.
func BenchmarkReadDirect(b *testing.B) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	b.ReportAllocs()
	for b.Loop() {
		strSink = u.Name
	}
}

// BenchmarkReadFieldByName — reflect с поиском поля по ИМЕНИ на каждом
// вызове: строковый поиск по полям типа + боксинг результата в interface{}.
func BenchmarkReadFieldByName(b *testing.B) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	b.ReportAllocs()
	for b.Loop() {
		anySink = reflect.ValueOf(u).FieldByName("Name").Interface()
	}
}

// BenchmarkReadFieldByIndex — reflect по заранее известному индексу поля
// (типовая оптимизация сериализаторов: индекс вычислили один раз). Дешевле
// поиска по имени, но всё равно с боксингом в interface{}.
func BenchmarkReadFieldByIndex(b *testing.B) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	b.ReportAllocs()
	for b.Loop() {
		anySink = reflect.ValueOf(u).Field(1).Interface()
	}
}

// --- ЗАПИСЬ ПОЛЯ ---

// BenchmarkSetDirect — прямое присваивание.
func BenchmarkSetDirect(b *testing.B) {
	u := &User{}
	b.ReportAllocs()
	for b.Loop() {
		u.Name = "новое"
	}
}

// BenchmarkSetReflect — запись через reflect с поиском поля по имени.
func BenchmarkSetReflect(b *testing.B) {
	u := &User{}
	b.ReportAllocs()
	for b.Loop() {
		reflect.ValueOf(u).Elem().FieldByName("Name").SetString("новое")
	}
}

// --- СЕРИАЛИЗАЦИЯ структуры в map[string]any ---

// toMapDirect — руками, без reflect: знаем поля на этапе компиляции.
func toMapDirect(u User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "email": u.Email}
}

// toMapReflect — обобщённо через reflect: перебор полей, чтение тегов,
// боксинг каждого значения. Так работают encoding/json и валидаторы.
func toMapReflect(v any) map[string]any {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	out := make(map[string]any, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[tag] = rv.Field(i).Interface()
	}
	return out
}

func BenchmarkToMapDirect(b *testing.B) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	b.ReportAllocs()
	for b.Loop() {
		anySink = toMapDirect(u)
	}
}

func BenchmarkToMapReflect(b *testing.B) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	b.ReportAllocs()
	for b.Loop() {
		anySink = toMapReflect(u)
	}
}
