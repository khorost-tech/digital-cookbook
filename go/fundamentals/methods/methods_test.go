// methods_test.go — тесты корректности: value- против pointer-ресивера,
// удовлетворение интерфейса через method set, method value и method expression.
package methods

import "testing"

// TestValueReceiverKeepsOriginal: метод на value-ресивере работает с копией,
// поэтому после IncValue исходное значение НЕ меняется.
func TestValueReceiverKeepsOriginal(t *testing.T) {
	c := Counter{N: 0}
	c.IncValue()
	if c.N != 0 {
		t.Errorf("после IncValue ожидали N=0 (мутация копии не видна), получили N=%d", c.N)
	}
}

// TestPointerReceiverMutatesOriginal: метод на pointer-ресивере мутирует
// оригинал, поэтому после IncPtr значение увеличивается.
func TestPointerReceiverMutatesOriginal(t *testing.T) {
	c := Counter{N: 0}
	c.IncPtr() // Go автоматически берёт адрес: (&c).IncPtr()
	if c.N != 1 {
		t.Errorf("после IncPtr ожидали N=1 (мутация оригинала), получили N=%d", c.N)
	}
}

// TestPointerSatisfiesInterface: у *Doc метод String в method set, значит *Doc
// удовлетворяет Stringer. Это позитивный путь.
//
// Обратный случай — присвоить Doc{} (значение) переменной Stringer — НЕ
// компилируется: у String pointer-ресивер, метод отсутствует в method set
// значения Doc. Строку
//
//	var s Stringer = Doc{}
//
// намеренно не включаем в тест: она не собралась бы и уронила бы весь пакет.
func TestPointerSatisfiesInterface(t *testing.T) {
	var s Stringer = AsStringer(&Doc{Title: "readme"})
	if got := s.String(); got != "doc:readme" {
		t.Errorf("String() = %q, ожидали %q", got, "doc:readme")
	}
}

// TestValueReceiverInBothMethodSets: метод с value-ресивером входит в method
// set И значения, И указателя. Поэтому Labeler удовлетворяют и Labeled, и
// *Labeled.
func TestValueReceiverInBothMethodSets(t *testing.T) {
	val := Labeled{Text: "v"}
	ptr := &Labeled{Text: "p"}

	var a Labeler = val // ок: value-метод в method set значения
	var b Labeler = ptr // ок: value-метод в method set указателя

	if a.Label() != "label:v" {
		t.Errorf("значение: Label() = %q, ожидали %q", a.Label(), "label:v")
	}
	if b.Label() != "label:p" {
		t.Errorf("указатель: Label() = %q, ожидали %q", b.Label(), "label:p")
	}
}

// TestMethodValue: method value (f := c.IncPtr) связывает ресивер и мутирует
// оригинал при каждом вызове.
func TestMethodValue(t *testing.T) {
	if got := MethodValueDemo(3); got != 3 {
		t.Errorf("MethodValueDemo(3) = %d, ожидали 3", got)
	}
}

// TestMethodExpression: method expression (Counter.IncValue) — функция с
// ресивером-аргументом; value-ресивер работает с копией, оригинал не меняется.
func TestMethodExpression(t *testing.T) {
	if got := MethodExpressionDemo(); got != 5 {
		t.Errorf("MethodExpressionDemo() = %d, ожидали 5 (копия, мутация не видна)", got)
	}
}

// TestDescribe: интерфейсное значение вызывает метод динамического типа.
func TestDescribe(t *testing.T) {
	if got := Describe(&Doc{Title: "x"}); got != "Stringer -> doc:x" {
		t.Errorf("Describe = %q, ожидали %q", got, "Stringer -> doc:x")
	}
}
