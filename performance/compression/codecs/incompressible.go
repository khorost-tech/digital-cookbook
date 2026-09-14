package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunIncompressible показывает цену попытки сжать несжимаемое: процессорное
// время расходуется, а размер не уменьшается — и может вырасти на величину
// служебных структур формата.
func RunIncompressible(profilesDir string, reps int) ([]Measurement, error) {
	data, err := os.ReadFile(filepath.Join(profilesDir, "incompressible.bin"))
	if err != nil {
		return nil, err
	}
	var res []Measurement
	for _, c := range []Codec{
		lz4Codec{level: 0},
		zstdKlauspost{level: 3},
		gzipCodec{level: 6},
	} {
		m, err := Measure(c, "incompressible", data, reps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ПРОПУСК %s: %v\n", c.Name(), err)
			continue
		}
		delta := m.OutputBytes - m.InputBytes
		fmt.Fprintf(os.Stderr, "%-22s %d -> %d байт (%+d)  ratio=%.6f  сжатие=%.1f МБ/с\n",
			m.Codec, m.InputBytes, m.OutputBytes, delta, m.Ratio, m.CompressMBs)
		res = append(res, m)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("ни один замер не прошёл порог достоверности")
	}
	return res, nil
}
