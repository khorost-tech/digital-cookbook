package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Манифест — эталон для всех проверок стенда, поэтому он обязан совпадать с
// тем, что реально записано в файл. Расхождение здесь означало бы, что все
// критерии сравниваются с выдуманным числом.
func TestManifestMatchesFile(t *testing.T) {
	dir := t.TempDir()
	writeStream(t, dir, 500, 42, "t1", false)

	var m manifest
	readJSON(t, filepath.Join(dir, "manifest.json"), &m)

	lines, byLevel, errorsCheckout, withReqID := scanLog(t, filepath.Join(dir, "app.log"))

	if lines != m.Total {
		t.Errorf("строк в файле %d, в манифесте %d", lines, m.Total)
	}
	for lvl, n := range byLevel {
		if m.ByLevel[lvl] != n {
			t.Errorf("уровень %s: в файле %d, в манифесте %d", lvl, n, m.ByLevel[lvl])
		}
	}
	if errorsCheckout != m.ErrorsCheckout {
		t.Errorf("error у checkout: в файле %d, в манифесте %d", errorsCheckout, m.ErrorsCheckout)
	}
	// В обычном режиме поля request_id быть не должно ВОВСЕ: пустая строка тоже
	// создала бы поле в индексе и исказила замер объёма.
	if withReqID != 0 {
		t.Errorf("в обычном режиме %d записей с request_id, ожидалось 0", withReqID)
	}
}

func TestHighCardinalityAddsUniqueField(t *testing.T) {
	dir := t.TempDir()
	writeStream(t, dir, 300, 42, "t2", true)

	seen := map[string]bool{}
	f, err := os.Open(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.RequestID == "" {
			t.Fatal("в режиме высокой кардинальности пустой request_id")
		}
		if seen[r.RequestID] {
			t.Fatalf("request_id повторился: %s", r.RequestID)
		}
		seen[r.RequestID] = true
	}
	if len(seen) != 300 {
		t.Errorf("уникальных request_id %d, ожидалось 300", len(seen))
	}
}

// Один seed — один и тот же файл побайтово. Без этого замер объёма сравнивал бы
// разные данные, а разницу списывал на бэкенды.
func TestSameSeedSameBytes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeStream(t, a, 200, 7, "same", false)
	writeStream(t, b, 200, 7, "same", false)

	ba, err := os.ReadFile(filepath.Join(a, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(filepath.Join(b, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ba) != string(bb) {
		t.Error("один seed дал разные файлы")
	}
}

// --- хелперы тестов ---------------------------------------------------------

func writeStream(t *testing.T, dir string, count int, seed int64, run string, highCard bool) {
	t.Helper()
	// База фиксирована: тест детерминизма сравнивает файлы побайтово, и
	// «сейчас» из двух вызовов дало бы разное время.
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := generate(dir, count, seed, run, highCard, base); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func scanLog(t *testing.T, path string) (lines int, byLevel map[string]int, errorsCheckout, withReqID int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	byLevel = map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("строка %d не разбирается: %v", lines+1, err)
		}
		lines++
		byLevel[r.Level]++
		if r.Service == "checkout" && r.Level == "error" {
			errorsCheckout++
		}
		if r.RequestID != "" {
			withReqID++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines, byLevel, errorsCheckout, withReqID
}
