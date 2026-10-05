package bench

import "testing"

// Общий SQL для обеих СУБД возможен только если плейсхолдеры приводятся к
// диалекту. Тест защищает от сдвига нумерации — ошибка здесь тихо испортила бы
// вставку, а не уронила её.
func TestRewritePlaceholders(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT id FROM events WHERE id = ?", "SELECT id FROM events WHERE id = $1"},
		{"INSERT INTO events (user_id, amount, payload) VALUES (?, ?, ?)",
			"INSERT INTO events (user_id, amount, payload) VALUES ($1, $2, $3)"},
		{"SELECT 1", "SELECT 1"},
	}
	for _, c := range cases {
		if got := RewritePlaceholders(c.in); got != c.want {
			t.Errorf("RewritePlaceholders(%q) = %q, хотели %q", c.in, got, c.want)
		}
	}
}
