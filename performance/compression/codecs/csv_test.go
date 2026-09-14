package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteCSVHeaderAndRow(t *testing.T) {
	ms := []Measurement{{
		Codec: "zstd/klauspost", Level: 3, Profile: "products",
		InputBytes: 1000, OutputBytes: 250, Ratio: 4,
		CompressMBs: 120.5, DecompressMBs: 480.25, Reps: 2,
		CompressMs: []float64{51.5, 52.5}, DecompressMs: []float64{60, 62},
		// Разные значения на сжатие и разжатие специально: если поля
		// когда-нибудь переставят местами при правке WriteCSV, тест на
		// одинаковых числах прошёл бы и после перестановки. Здесь — нет.
		CompressPasses: 3, DecompressPasses: 7,
		CompressAttempts: 1, DecompressAttempts: 2,
	}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, ms); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк %d, ожидалось 2 (заголовок + запись)", len(lines))
	}
	if !strings.HasPrefix(lines[0], "profile,codec,level,input_bytes,output_bytes,ratio,") {
		t.Fatalf("неожиданный заголовок: %s", lines[0])
	}
	wantHeader := "profile,codec,level,input_bytes,output_bytes,ratio," +
		"compress_mbs,decompress_mbs,reps,compress_ms_median,decompress_ms_median," +
		"compress_passes,decompress_passes,compress_attempts,decompress_attempts"
	if lines[0] != wantHeader {
		t.Fatalf("заголовок:\n  получено %q\n  ожидалось %q", lines[0], wantHeader)
	}
	// Медиана двух значений — среднее пары; проверяем, что в CSV попала
	// именно она, а не первый замер.
	if !strings.Contains(lines[1], "52") {
		t.Fatalf("медиана 52 не найдена в строке: %s", lines[1])
	}

	row := strings.Split(lines[1], ",")
	if len(row) != len(strings.Split(wantHeader, ",")) {
		t.Fatalf("в строке %d полей, ожидалось %d: %s", len(row), len(strings.Split(wantHeader, ",")), lines[1])
	}
	// Позиции соответствуют wantHeader: ...,compress_passes(11),
	// decompress_passes(12),compress_attempts(13),decompress_attempts(14).
	const (
		idxCompressPasses     = 11
		idxDecompressPasses   = 12
		idxCompressAttempts   = 13
		idxDecompressAttempts = 14
	)
	checks := []struct {
		name string
		idx  int
		want string
	}{
		{"compress_passes", idxCompressPasses, "3"},
		{"decompress_passes", idxDecompressPasses, "7"},
		{"compress_attempts", idxCompressAttempts, "1"},
		{"decompress_attempts", idxDecompressAttempts, "2"},
	}
	for _, c := range checks {
		if row[c.idx] != c.want {
			t.Fatalf("%s: получено %q, ожидалось %q (строка: %s)", c.name, row[c.idx], c.want, lines[1])
		}
	}
}

func TestWriteRepsCSVRowPerRep(t *testing.T) {
	ms := []Measurement{{
		Codec: "lz4/pierrec", Level: 0, Profile: "products", Reps: 3,
		CompressMs: []float64{50, 51, 52}, DecompressMs: []float64{60, 61, 62},
	}}
	var buf bytes.Buffer
	if err := WriteRepsCSV(&buf, ms); err != nil {
		t.Fatalf("WriteRepsCSV: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("строк %d, ожидалось 4 (заголовок + 3 повтора)", len(lines))
	}
}
