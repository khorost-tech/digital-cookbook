package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunMatrix прогоняет все кодеки на одном профиле. Каждый замер печатается
// сразу — прогон по всем уровням brotli небыстрый, и молчащий стенд
// неотличим от зависшего.
func RunMatrix(profilePath string, reps int) ([]Measurement, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	profile := filepath.Base(profilePath)
	fmt.Fprintf(os.Stderr, "профиль %s: %d байт, повторов %d\n", profile, len(data), reps)

	out := make([]Measurement, 0, len(AllCodecs()))
	for _, c := range AllCodecs() {
		m, err := Measure(c, profile, data, reps)
		if err != nil {
			// Замер ниже порога достоверности — не молчаливый пропуск:
			// печатаем причину, иначе таблица окажется неполной без следа.
			fmt.Fprintf(os.Stderr, "ПРОПУСК %s уровень %d: %v\n", c.Name(), c.Level(), err)
			continue
		}
		fmt.Fprintf(os.Stderr, "%-22s ур.%-3d ratio=%.3f  сжатие=%.1f МБ/с  разжатие=%.1f МБ/с  (проходов сж/раз=%d/%d, попыток сж/раз=%d/%d)\n",
			m.Codec, m.Level, m.Ratio, m.CompressMBs, m.DecompressMBs,
			m.CompressPasses, m.DecompressPasses, m.CompressAttempts, m.DecompressAttempts)
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ни один замер не прошёл порог достоверности")
	}
	return out, nil
}
