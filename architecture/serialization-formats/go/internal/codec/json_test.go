package codec

import "testing"

// json — контроль. Схема демонстративно игнорируется: даже пустая
// схема без содержимого не должна помешать кодированию.
func TestJSONCodecRoundTrip(t *testing.T) {
	c, err := New("json")
	if err != nil {
		t.Fatalf("New(json): %v", err)
	}
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, Schema{Name: "схема-которую-никто-не-читает"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b, Schema{Name: "и-эту"}, Schema{Name: "и-эту-тоже"})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("поле %s: got %#v, want %#v", k, got[k], v)
		}
	}
}
