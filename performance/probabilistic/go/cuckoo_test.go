package main

import "testing"

func TestCuckooDeleteRemovesMembership(t *testing.T) {
	if !cuckooDeleteWorks() {
		t.Fatal("после Delete ключ всё ещё найден — удаление не работает")
	}
}
