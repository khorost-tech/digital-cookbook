// methods.go — методы и ресиверы в Go: value-receiver против pointer-receiver,
// method set и удовлетворение интерфейса, method value и method expression.
//
// Метод — это функция с особым аргументом-ресивером. Ресивер бывает двух видов:
//   - value receiver  (func (c Counter) …)  — метод получает КОПИЮ значения,
//     мутации не видны снаружи;
//   - pointer receiver (func (c *Counter) …) — метод получает указатель на
//     оригинал, мутации сохраняются.
//
// От вида ресивера зависит и method set: у значения T в method set входят
// только методы с value-ресивером, у указателя *T — методы обоих видов.
// Отсюда правило удовлетворения интерфейсов, разобранное ниже на типе Doc.
package methods

import "fmt"

// Counter — маленький тип, на котором виден контраст двух ресиверов.
type Counter struct {
	N int
}

// IncValue объявлен на VALUE-ресивере: метод работает с копией Counter,
// поэтому увеличение поля теряется, как только метод вернёт управление.
// Оригинал, на котором вызвали метод, остаётся прежним.
func (c Counter) IncValue() {
	c.N++ // мутируется копия, снаружи не видно
}

// IncPtr объявлен на POINTER-ресивере: метод получает указатель на оригинал,
// поэтому увеличение поля сохраняется в исходном значении.
func (c *Counter) IncPtr() {
	c.N++ // мутируется оригинал
}

// Stringer — минимальный интерфейс с одним методом String.
// (Аналог fmt.Stringer, объявлен локально, чтобы урок был самодостаточным.)
type Stringer interface {
	String() string
}

// Doc — тип, у которого метод String объявлен на УКАЗАТЕЛЬНОМ ресивере.
// Из-за этого String входит в method set указателя *Doc, но НЕ значения Doc.
type Doc struct {
	Title string
}

// String объявлен на *Doc: метод в method set только у *Doc.
func (d *Doc) String() string {
	return "doc:" + d.Title
}

// AsStringer возвращает *Doc как Stringer. Присвоение работает, потому что у
// *Doc метод String есть в method set.
//
// А вот значение Doc присвоить Stringer НЕЛЬЗЯ — метод с pointer-ресивером не
// входит в method set значения. Такая строка не компилируется:
//
//	var s Stringer = Doc{} // cannot use Doc{} as Stringer: String has pointer receiver
//
// Поэтому её здесь нет — её невозможно закомпилировать; проверяем позитивный
// путь через указатель.
func AsStringer(d *Doc) Stringer {
	return d // ок: *Doc удовлетворяет Stringer
}

// Labeled — тип с VALUE-ресивером у метода Label. Такой метод входит в method
// set И значения Labeled, И указателя *Labeled, поэтому интерфейс Labeler
// удовлетворяют оба (см. тест).
type Labeled struct {
	Text string
}

// Labeler — интерфейс над методом Label.
type Labeler interface {
	Label() string
}

// Label объявлен на value-ресивере: доступен и у Labeled, и у *Labeled.
func (l Labeled) Label() string {
	return "label:" + l.Text
}

// MethodValueDemo показывает method value: выражение c.IncPtr «замораживает»
// ресивер c и даёт обычную функцию func(), которую можно вызвать позже. Так как
// IncPtr — pointer-ресивер, method value держит указатель на c и мутирует его.
// Возвращаем итоговое c.N после n вызовов.
func MethodValueDemo(n int) int {
	c := &Counter{}
	f := c.IncPtr // method value: ресивер c связан, тип f — func()
	for i := 0; i < n; i++ {
		f()
	}
	return c.N
}

// MethodExpressionDemo показывает method expression: Counter.IncValue — это
// функция func(Counter), где ресивер стал ПЕРВЫМ явным аргументом. Ресивер
// value-типа, поэтому функция работает с копией и мутация наружу не выходит.
// Возвращаем поле N исходного значения после вызова — оно не изменилось.
func MethodExpressionDemo() int {
	g := Counter.IncValue // method expression: тип g — func(Counter)
	c := Counter{N: 5}
	g(c) // c передаётся по значению (копия), оригинал не меняется
	return c.N
}

// describe — вспомогательный вывод, чтобы у пакета был импорт fmt в примерах.
func describe(s Stringer) string {
	return fmt.Sprintf("Stringer -> %s", s.String())
}

// Describe экспортирует describe для использования в тестах.
func Describe(s Stringer) string {
	return describe(s)
}
