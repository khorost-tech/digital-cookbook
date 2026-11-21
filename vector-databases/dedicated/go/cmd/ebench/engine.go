package main

import "context"

// Config — параметры коллекции, общие для трёх движков.
type Config struct {
	M, EfC int
	Quant  string // "" — float32, "int8" — скалярная квантизация (Qdrant)
	Filter string // Weaviate: filterStrategy ("" — по умолчанию, acorn; "sweeping")
}

// Params — параметры одного поиска.
type Params struct {
	Ef           int
	BucketLt     int // < 0 — без фильтра
	Exact        bool
	Rescore      bool    // квантизация: пересчёт по исходным векторам
	Oversampling float64 // квантизация: во сколько раз больше кандидатов
	Strong       bool    // Milvus: уровень согласованности Strong вместо умолчания
}

// Engine — то, что стенд делает с каждым движком одинаково.
type Engine interface {
	Name() string
	Reset(ctx context.Context) error
	Create(ctx context.Context, cfg Config) error
	Insert(ctx context.Context, ids []int, vecs [][]float32) error
	// Ready ждёт, пока движок закончит индексацию всего вставленного.
	Ready(ctx context.Context) error
	// Prepare — то, что нужно перед серией поисков с данным ef (Weaviate задаёт ef в схеме).
	Prepare(ctx context.Context, p Params) error
	Search(ctx context.Context, q []float32, k int, p Params) ([]int32, error)
	Info(ctx context.Context) (map[string]any, error)
}
