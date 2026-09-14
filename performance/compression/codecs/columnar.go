package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunColumnar сравнивает построчную и колоночную раскладку одних и тех же
// значений одним и тем же кодеком на одном уровне. Всё, кроме порядка байт,
// удерживается постоянным — иначе сравнение измеряло бы смесь эффектов.
func RunColumnar(profilesDir string, reps int) ([]Measurement, error) {
	var res []Measurement
	for _, f := range []struct{ file, label string }{
		{"table-row.bin", "table-row"},
		{"table-col.bin", "table-col"},
	} {
		data, err := os.ReadFile(filepath.Join(profilesDir, f.file))
		if err != nil {
			return nil, err
		}
		for _, c := range []Codec{zstdKlauspost{level: 3}, gzipCodec{level: 6}} {
			m, err := Measure(c, f.label, data, reps)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ПРОПУСК %s %s: %v\n", c.Name(), f.label, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "%-10s %-22s %d -> %d байт  ratio=%.4f\n",
				f.label, m.Codec, m.InputBytes, m.OutputBytes, m.Ratio)
			res = append(res, m)
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("ни один замер не прошёл порог достоверности")
	}
	return res, nil
}
