package main

import (
	"bytes"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

type Codec interface {
	Name() string
	Level() int
	Compress(src []byte) ([]byte, error)
	Decompress(src []byte) ([]byte, error)
}

// --- zstd (чистый Go, klauspost) ---

type zstdKlauspost struct{ level int }

func (z zstdKlauspost) Name() string { return "zstd/klauspost" }
func (z zstdKlauspost) Level() int   { return z.level }

func (z zstdKlauspost) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(z.level)))
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

func (z zstdKlauspost) Decompress(src []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// --- gzip ---

type gzipCodec struct{ level int }

func (g gzipCodec) Name() string { return "gzip/klauspost" }
func (g gzipCodec) Level() int   { return g.level }

func (g gzipCodec) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, g.level)
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

func (g gzipCodec) Decompress(src []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// --- lz4 ---

type lz4Codec struct{ level int }

func (l lz4Codec) Name() string { return "lz4/pierrec" }
func (l lz4Codec) Level() int   { return l.level }

// lz4Level переводит семантический уровень (0, 1..9 — как во всех остальных
// кодеках здесь) в константу lz4.CompressionLevel. Прямое приведение типа
// int(level) не годится: библиотека кодирует уровни как заранее заданные
// битовые значения (Level9 = 1<<17, а не 9), и CompressionLevelOption
// отклоняет с ошибкой всё, что не совпало с одной из её констант.
//
// Уровни 1..8 отображены для полноты (весь диапазон, который принимает
// библиотека), но в матрицу стенда не входят: AllCodecs() использует только
// lz4 уровней 0 и 9 (см. ниже).
func lz4Level(level int) lz4.CompressionLevel {
	switch level {
	case 0:
		return lz4.Fast
	case 1:
		return lz4.Level1
	case 2:
		return lz4.Level2
	case 3:
		return lz4.Level3
	case 4:
		return lz4.Level4
	case 5:
		return lz4.Level5
	case 6:
		return lz4.Level6
	case 7:
		return lz4.Level7
	case 8:
		return lz4.Level8
	default:
		return lz4.Level9
	}
}

func (l lz4Codec) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := lz4.NewWriter(&buf)
	if err := w.Apply(lz4.CompressionLevelOption(lz4Level(l.level))); err != nil {
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

func (l lz4Codec) Decompress(src []byte) ([]byte, error) {
	return io.ReadAll(lz4.NewReader(bytes.NewReader(src)))
}

// --- brotli ---

type brotliCodec struct{ level int }

func (b brotliCodec) Name() string { return "brotli/andybalholm" }
func (b brotliCodec) Level() int   { return b.level }

func (b brotliCodec) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, b.level)
	if _, err := w.Write(src); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (b brotliCodec) Decompress(src []byte) ([]byte, error) {
	return io.ReadAll(brotli.NewReader(bytes.NewReader(src)))
}

// AllCodecs — набор для матрицы. Уровни выбраны так, чтобы покрыть края
// диапазона и середину: крайние показывают форму кривой, средние — рабочую
// точку по умолчанию.
func AllCodecs() []Codec {
	return []Codec{
		lz4Codec{level: 0}, // быстрый режим по умолчанию
		lz4Codec{level: 9},
		zstdKlauspost{level: 1},
		zstdKlauspost{level: 3},
		zstdKlauspost{level: 7},
		zstdKlauspost{level: 11},
		gzipCodec{level: 1},
		gzipCodec{level: 6},
		gzipCodec{level: 9},
		brotliCodec{level: 1},
		brotliCodec{level: 5},
		brotliCodec{level: 11},
	}
}
