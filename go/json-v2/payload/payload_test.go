package payload

import "testing"

func TestLargeArrayDeterministic(t *testing.T) {
	a := LargeArray(100)
	b := LargeArray(100)
	if string(a.JSON) != string(b.JSON) {
		t.Fatal("генерация не детерминирована: два вызова дали разный JSON")
	}
	if len(a.JSON) == 0 {
		t.Fatal("пустой payload")
	}
}

func TestAllFormsNonEmpty(t *testing.T) {
	for _, c := range All() {
		if len(c.JSON) == 0 {
			t.Fatalf("форма %s пуста", c.Name)
		}
	}
}
