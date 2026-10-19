package main

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"  Trim  Me  ", "trim-me"},
		{"CI/CD на практике", "ci-cd"}, // не-ASCII серии схлопываются как разделители
		{"already-a-slug", "already-a-slug"},
		{"Multiple---Dashes", "multiple-dashes"},
		{"!!!", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
