package buildmode

import "testing"

// Режим обязан определяться однозначно: он попадает в заголовок отчёта, и
// «не смог определить» там недопустимо.
func TestCurrentIsKnown(t *testing.T) {
	m := Current()
	if m != ModeJSONv2 && m != ModeNoJSONv2 {
		t.Fatalf("режим не распознан: %q (raw=%q)", m, Raw())
	}
}

func TestParse(t *testing.T) {
	if got := parse("nojsonv2,cacheprog"); got != ModeNoJSONv2 {
		t.Fatalf("ожидался %q, получено %q", ModeNoJSONv2, got)
	}
	if got := parse("cacheprog"); got != ModeJSONv2 {
		t.Fatalf("ожидался %q, получено %q", ModeJSONv2, got)
	}
	if got := parse(""); got != ModeJSONv2 {
		t.Fatalf("пустой GOEXPERIMENT — это умолчание 1.27, ожидался %q, получено %q", ModeJSONv2, got)
	}
}
