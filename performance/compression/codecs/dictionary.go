package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// zstdWithDict — тот же кодек, но со словарём, обученным на выборке событий.
// Словарь даёт компрессору контекст, которого в одном мелком сообщении
// физически нет: именно поэтому эффект ожидается на мелких похожих payload'ах
// и не ожидается на крупном корпусе.
type zstdWithDict struct {
	level int
	dict  []byte
}

func (z zstdWithDict) Name() string { return "zstd/klauspost+dict" }
func (z zstdWithDict) Level() int   { return z.level }

func (z zstdWithDict) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(z.level)),
		zstd.WithEncoderDict(z.dict))
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(src); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (z zstdWithDict) Decompress(src []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(src), zstd.WithDecoderDicts(z.dict))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// dictHistoryCap — размер History (сырого содержимого словаря, по которому
// компрессор ищет совпадения): в klauspost/compress v1.19.0 у
// BuildDictOptions нет параметра MaxDictSize (проверено `go doc` — см.
// Step 1a брифа и task-5-compression-report.md), размер History равен
// ровно тому, что в него передали. Кэп — наш собственный выбор, чтобы
// получить словарь сопоставимого с классическим `zstd --train` размера,
// а не выдать за словарь мегабайты сырого корпуса.
const dictHistoryCap = 16 * 1024

// buildHistory склеивает обучающие сообщения до достижения dictHistoryCap.
// Порядок совпадает с порядком в trainN — детерминированно от входа.
func buildHistory(samples [][]byte, cap int) []byte {
	var buf bytes.Buffer
	for _, s := range samples {
		if buf.Len() >= cap {
			break
		}
		buf.Write(s)
		buf.WriteByte('\n')
	}
	h := buf.Bytes()
	if len(h) > cap {
		h = h[:cap]
	}
	return h
}

// RunDictionary отвечает на два вопроса: даёт ли словарь выигрыш на мелких
// похожих сообщениях и до какого размера payload этот выигрыш сохраняется.
//
// Замер ведётся по сумме размеров отдельно сжатых сообщений, а не по одному
// склеенному буферу: склейка дала бы компрессору тот самый межсообщенческий
// контекст, ради отсутствия которого словарь и нужен. Это ключевая деталь
// методики — при склейке сценарий измерял бы не то, что заявляет.
//
// reps — часть общего интерфейса сценариев (сигнатура как у RunMatrix/
// RunColumnar), но сценарий словаря отвечает на вопрос о РАЗМЕРЕ, а не о
// скорости: сжатие 180 000 отдельных сообщений уже само по себе многократный
// прогон компрессора, отдельная петля повторов здесь не нужна.
// Воспроизводимость размеров подтверждается снаружи — несколькими запусками
// всего бинарника (см. task-5-compression-report.md).
func RunDictionary(profilesDir string, reps int) ([]Measurement, error) {
	_ = reps
	raw, err := os.ReadFile(filepath.Join(profilesDir, "events-small.ndjson"))
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) < 1000 {
		return nil, fmt.Errorf("событий %d — мало для сценария", len(lines))
	}

	// Словарь обучается на первых 10% сообщений, замеряется на остальных:
	// обучение и замер на одних данных завысили бы результат.
	//
	// Contents — вся обучающая выборка (статистика Huffman/FSE-таблиц
	// строится по ней целиком). History — фактическое "тело" словаря, по
	// которому ищутся совпадения; кэпнуто dictHistoryCap отдельно от
	// Contents (см. buildHistory). ID должен быть ненулевым и Offsets —
	// ненулевыми: при ID=0 loadDict отказывает словарь с ошибкой
	// "dictionaries cannot have ID 0" при попытке его использовать, а
	// нулевые Offsets аналогично отклоняются как невалидный словарь —
	// BuildDict заполняет Offsets из статистики обучающей выборки только
	// частично (top-3 по факту использованных повторных смещений), поэтому
	// стартовые значения нужно засеять самим. {1,4,8} — стандартные
	// начальные repeat-offsets формата zstd, тот же выбор в
	// TestBuildDictLevelPathsRoundTrip самой библиотеки (dict_test.go).
	trainN := len(lines) / 10
	trainSet := lines[:trainN]
	history := buildHistory(trainSet, dictHistoryCap)
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: trainSet,
		History:  history,
		Offsets:  [3]int{1, 4, 8},
	})
	if err != nil {
		return nil, fmt.Errorf("построение словаря: %w", err)
	}
	measured := lines[trainN:]
	fmt.Fprintf(os.Stderr, "словарь: обучен на %d сообщениях, размер %d байт; замер на %d сообщениях\n",
		trainN, len(dict), len(measured))

	sumSizes := func(c Codec) (in, out int64, err error) {
		for _, ln := range measured {
			comp, e := c.Compress(ln)
			if e != nil {
				return 0, 0, e
			}
			in += int64(len(ln))
			out += int64(len(comp))
		}
		return in, out, nil
	}

	var res []Measurement
	for _, c := range []Codec{
		zstdKlauspost{level: 3},
		zstdWithDict{level: 3, dict: dict},
	} {
		in, out, err := sumSizes(c)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "%-24s суммарно %d -> %d байт, ratio=%.4f\n",
			c.Name(), in, out, float64(in)/float64(out))
		res = append(res, Measurement{
			Codec: c.Name(), Level: c.Level(), Profile: "events-small (по сообщениям)",
			InputBytes: in, OutputBytes: out, Ratio: float64(in) / float64(out),
			Reps: 1,
		})
	}
	return res, nil
}
