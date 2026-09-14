package main

import (
	"fmt"
	"os"

	ddzstd "github.com/DataDog/zstd"
	"github.com/valyala/gozstd"
)

// --- zstd через valyala/gozstd (cgo, libzstd) ---

type zstdValyala struct{ level int }

func (z zstdValyala) Name() string { return "zstd/valyala-cgo" }
func (z zstdValyala) Level() int   { return z.level }

func (z zstdValyala) Compress(src []byte) ([]byte, error) {
	return gozstd.CompressLevel(nil, src, z.level), nil
}

func (z zstdValyala) Decompress(src []byte) ([]byte, error) {
	return gozstd.Decompress(nil, src)
}

// --- zstd через DataDog/zstd (cgo, libzstd) ---

type zstdDataDog struct{ level int }

func (z zstdDataDog) Name() string { return "zstd/datadog-cgo" }
func (z zstdDataDog) Level() int   { return z.level }

func (z zstdDataDog) Compress(src []byte) ([]byte, error) {
	return ddzstd.CompressLevel(nil, src, z.level)
}

func (z zstdDataDog) Decompress(src []byte) ([]byte, error) {
	return ddzstd.Decompress(nil, src)
}

// ZstdCodecs — один и тот же алгоритм в трёх реализациях на одних и тех же
// номинальных уровнях. Вопрос сценария: означает ли «уровень 3» одно и то же.
//
// Важно для интерпретации результатов: klauspost/compress НЕ пробрасывает
// номинальный уровень напрямую. zstdKlauspost (codec.go) вызывает
// zstd.EncoderLevelFromZstd(level), которая по исходнику
// klauspost/compress@v1.19.0, zstd/encoder_options.go (метод
// EncoderLevelFromZstd, ок. строк 203-214), квантует произвольный номинальный
// уровень всего в 4 пресета: level<3 -> SpeedFastest, 3<=level<6 ->
// SpeedDefault, 6<=level<10 -> SpeedBetterCompression, level>=10 ->
// SpeedBestCompression. Это задокументированное поведение библиотеки
// («EncoderLevelFromZstd will return an encoder level that closest matches
// the compression ratio of a specific zstd compression level. Many input
// values will provide the same compression level.») — не гипотеза. Уровни
// этого сценария (1, 3, 7) попадают в три РАЗНЫХ бакета (Fastest/Default/
// Better), поэтому расхождение с cgo-обвязками (valyala/gozstd, DataDog/zstd
// — обе пробрасывают уровень напрямую в libzstd, где градаций 22) видно на
// всех трёх уровнях. Замер (не из исходника, а с этого стенда): klauspost на
// уровнях 3 и 5 — оба в бакете SpeedDefault — дают побайтово идентичный сжатый
// вывод на products.ndjson; связь «квантование -> расхождение размеров» этим
// подтверждена измерением, а не только чтением кода. Подробности и границы
// бакетов — в разделе «Фиксы после ревью», task-4-compression-report.md.
func ZstdCodecs() []Codec {
	var out []Codec
	for _, lvl := range []int{1, 3, 7} {
		out = append(out,
			zstdKlauspost{level: lvl},
			zstdValyala{level: lvl},
			zstdDataDog{level: lvl},
		)
	}
	return out
}

// RunZstdImpl прогоняет три реализации на одном профиле.
func RunZstdImpl(profilePath string, reps int) ([]Measurement, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	out := make([]Measurement, 0, len(ZstdCodecs()))
	for _, c := range ZstdCodecs() {
		m, err := Measure(c, "products.ndjson", data, reps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ПРОПУСК %s ур.%d: %v\n", c.Name(), c.Level(), err)
			continue
		}
		fmt.Fprintf(os.Stderr, "%-22s ур.%-3d выход=%d байт  ratio=%.4f  сжатие=%.1f МБ/с\n",
			m.Codec, m.Level, m.OutputBytes, m.Ratio, m.CompressMBs)
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ни один замер не прошёл порог достоверности")
	}
	return out, nil
}
