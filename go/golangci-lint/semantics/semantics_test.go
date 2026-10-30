package semantics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	fixeddb "tech.khorost/golangci/fixed/db"
	fixeddead "tech.khorost/golangci/fixed/dead"
	fixedperf "tech.khorost/golangci/fixed/perf"
	sampledb "tech.khorost/golangci/sample/db"
	sampledead "tech.khorost/golangci/sample/dead"
	sampleperf "tech.khorost/golangci/sample/perf"
)

// measure печатает величину для раннера стенда: «measure <имя> <значение>».
func measure(t *testing.T, name string, v int) {
	t.Helper()
	t.Logf("measure %s %d", name, v)
}

// Контроль: полный проход rows.Next() без Close — rows закрывается сам,
// соединение свободно. Поэтому прежний пример (Count) утечки не показывал.
func TestDrainedRowsReleaseConnection(t *testing.T) {
	db := Open(3)
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), "SELECT name FROM users")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	measure(t, "in_use", db.Stats().InUse)
	if db.Stats().InUse != 0 {
		t.Fatalf("после полного прохода соединение занято: %d", db.Stats().InUse)
	}
}

// sample/db.First выходит после первой строки и не закрывает rows: соединение занято.
func TestSampleFirstHoldsConnection(t *testing.T) {
	db := Open(3)
	defer db.Close()
	if _, err := sampledb.First(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	measure(t, "in_use", db.Stats().InUse)
	if db.Stats().InUse != 1 {
		t.Fatalf("ожидалось занятое соединение, InUse=%d", db.Stats().InUse)
	}
}

// fixed/db.First закрывает rows и при раннем выходе: соединение свободно.
func TestFixedFirstReleasesConnection(t *testing.T) {
	db := Open(3)
	defer db.Close()
	if _, err := fixeddb.First(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	measure(t, "in_use", db.Stats().InUse)
	if db.Stats().InUse != 0 {
		t.Fatalf("соединение занято: %d", db.Stats().InUse)
	}
}

// json.Encoder.Encode падает ещё до записи: тело пустое, и ответить 500 ещё можно.
// Поэтому «ошибка Encode значит, что клиент ушёл» — неверное обоснование подавления.
func TestEncodeFailsBeforeWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(rec).Encode(make(chan int))
	failed := 0
	if err != nil {
		failed = 1
	}
	measure(t, "encode_failed", failed)
	measure(t, "body_bytes", rec.Body.Len())
	measure(t, "header_written", map[bool]int{false: 0, true: 1}[rec.Flushed || rec.Body.Len() > 0])
	if err == nil || rec.Body.Len() != 0 {
		t.Fatalf("Encode: err=%v, тело %d байт", err, rec.Body.Len())
	}
	http.Error(rec, "internal", http.StatusInternalServerError)
	measure(t, "status_after", rec.Code)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("после ошибки Encode статус не выставился: %d", rec.Code)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// aliases — делит ли результат merge память с a: запись в результат видна в a.
func aliases(merge func(a, b []int) []int) bool {
	a := make([]int, 2, 10) // запас ёмкости — append в другой срез пишет в тот же массив
	c := merge(a, []int{7})
	c[0] = 99
	return a[0] == 99
}

// Контракт Merge — результат не делит память с a: sample нарушает, fixed — нет.
func TestMergeAliasing(t *testing.T) {
	measure(t, "sample_aliases", b2i(aliases(sampleperf.Merge)))
	measure(t, "fixed_aliases", b2i(aliases(fixedperf.Merge)))
	if !aliases(sampleperf.Merge) || aliases(fixedperf.Merge) {
		t.Fatal("контракт Merge: ожидалось — sample делит память, fixed нет")
	}
}

// Контракт Normalize — обрезать пробелы и привести к нижнему регистру.
func TestNormalizeTrims(t *testing.T) {
	in := "  Go "
	measure(t, "sample_trims", b2i(sampledead.Normalize(in) == "go"))
	measure(t, "fixed_trims", b2i(fixeddead.Normalize(in) == "go"))
	if sampledead.Normalize(in) == "go" || fixeddead.Normalize(in) != "go" {
		t.Fatal("контракт Normalize: ожидалось — sample не обрезает, fixed обрезает")
	}
}

// naiveIDs — исправление prealloc без оглядки на контракт: пустой срез вместо nil.
func naiveIDs(n []int) []string {
	out := make([]string, 0, len(n))
	for _, v := range n {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

func isNull(v []string) bool {
	b, _ := json.Marshal(v)
	return string(b) == "null"
}

// Исправление замечания тоже меняет поведение: на пустом входе nil (null) против [].
func TestIDsEmptyContract(t *testing.T) {
	measure(t, "sample_null", b2i(isNull(sampleperf.IDs(nil))))
	measure(t, "fixed_null", b2i(isNull(fixedperf.IDs(nil))))
	measure(t, "naive_null", b2i(isNull(naiveIDs(nil))))
	if !isNull(sampleperf.IDs(nil)) || !isNull(fixedperf.IDs(nil)) || isNull(naiveIDs(nil)) {
		t.Fatal("контракт IDs: sample и fixed — null, наивное исправление — []")
	}
}
