package main

import (
	"encoding/csv"
	"fmt"
	"io"
)

// WriteCSV — сводка по замерам: по строке на кодек и уровень.
//
// Помимо колонок из брифа, в конец добавлены compress_passes/
// decompress_passes и compress_attempts/decompress_attempts. Бриф их не
// описывает (он писался до появления адаптивной калибровки числа проходов
// в Measure — см. measure.go), но это часть методики измерения, а не
// побочная деталь: число проходов внутри повтора и число потребовавшихся
// пересчётов калибровки прямо влияют на то, что означает время в
// compress_ms_median/decompress_ms_median. Без этих колонок в CSV читатель
// не сможет отличить «один быстрый проход» от «сотня проходов, поделенная
// на сотню» — а фикстуры статьи должны это различие сохранить.
func WriteCSV(w io.Writer, ms []Measurement) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"profile", "codec", "level", "input_bytes", "output_bytes", "ratio",
		"compress_mbs", "decompress_mbs", "reps",
		"compress_ms_median", "decompress_ms_median",
		"compress_passes", "decompress_passes",
		"compress_attempts", "decompress_attempts",
	}); err != nil {
		return err
	}
	for _, m := range ms {
		if err := cw.Write([]string{
			m.Profile, m.Codec, fmt.Sprintf("%d", m.Level),
			fmt.Sprintf("%d", m.InputBytes), fmt.Sprintf("%d", m.OutputBytes),
			fmt.Sprintf("%.4f", m.Ratio),
			fmt.Sprintf("%.2f", m.CompressMBs), fmt.Sprintf("%.2f", m.DecompressMBs),
			fmt.Sprintf("%d", m.Reps),
			fmt.Sprintf("%.3f", median(m.CompressMs)),
			fmt.Sprintf("%.3f", median(m.DecompressMs)),
			fmt.Sprintf("%d", m.CompressPasses),
			fmt.Sprintf("%d", m.DecompressPasses),
			fmt.Sprintf("%d", m.CompressAttempts),
			fmt.Sprintf("%d", m.DecompressAttempts),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteRepsCSV — все повторы поштучно. Нужен, чтобы читатель видел разброс,
// а не только медиану: в стенде inmemory отсутствие таких данных пришлось
// оговаривать отдельно, здесь они есть с самого начала.
func WriteRepsCSV(w io.Writer, ms []Measurement) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"profile", "codec", "level", "rep", "compress_ms", "decompress_ms",
	}); err != nil {
		return err
	}
	for _, m := range ms {
		for i := range m.CompressMs {
			if err := cw.Write([]string{
				m.Profile, m.Codec, fmt.Sprintf("%d", m.Level), fmt.Sprintf("%d", i+1),
				fmt.Sprintf("%.3f", m.CompressMs[i]),
				fmt.Sprintf("%.3f", m.DecompressMs[i]),
			}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
