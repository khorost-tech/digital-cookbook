package main

import (
	"context"
	"fmt"
	"time"

	"github.com/qdrant/go-client/qdrant"

	"vdb/dedicated/vec"
)

const coll = "docs"

type qdrantEngine struct{ c *qdrant.Client }

func newQdrant() *qdrantEngine {
	c, err := qdrant.NewClient(&qdrant.Config{Host: "qdrant", Port: 6334})
	must(err)
	return &qdrantEngine{c}
}

func (e *qdrantEngine) Name() string { return "qdrant" }

func (e *qdrantEngine) Reset(ctx context.Context) error {
	ok, err := e.c.CollectionExists(ctx, coll)
	if err != nil || !ok {
		return err
	}
	return e.c.DeleteCollection(ctx, coll)
}

func (e *qdrantEngine) Create(ctx context.Context, cfg Config) error {
	req := &qdrant.CreateCollection{
		CollectionName: coll,
		VectorsConfig:  qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: vec.Dim, Distance: qdrant.Distance_Dot}),
		HnswConfig: &qdrant.HnswConfigDiff{M: qdrant.PtrOf(uint64(cfg.M)),
			EfConstruct: qdrant.PtrOf(uint64(cfg.EfC))},
	}
	if cfg.Quant == "int8" {
		req.QuantizationConfig = qdrant.NewQuantizationScalar(&qdrant.ScalarQuantization{
			Type: qdrant.QuantizationType_Int8, AlwaysRam: qdrant.PtrOf(true)})
	}
	if err := e.c.CreateCollection(ctx, req); err != nil {
		return err
	}
	// индекс по метке: фильтр по нему Qdrant учитывает при обходе графа и при
	// оценке, не перебрать ли срез целиком
	_, err := e.c.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{CollectionName: coll, FieldName: "bucket",
		FieldType: qdrant.FieldType_FieldTypeInteger.Enum(), Wait: qdrant.PtrOf(true)})
	return err
}

func (e *qdrantEngine) Insert(ctx context.Context, ids []int, vecs [][]float32) error {
	pts := make([]*qdrant.PointStruct, len(ids))
	for i, id := range ids {
		pts[i] = &qdrant.PointStruct{Id: qdrant.NewIDNum(uint64(id)), Vectors: qdrant.NewVectorsDense(vecs[i]),
			Payload: qdrant.NewValueMap(map[string]any{"bucket": vec.Bucket(id)})}
	}
	_, err := e.c.Upsert(ctx, &qdrant.UpsertPoints{CollectionName: coll, Wait: qdrant.PtrOf(true), Points: pts})
	return err
}

// Ready — статус коллекции «зелёный» и проиндексировано не меньше 97% точек.
// Одного статуса мало: сегменты меньше порога индексации (indexing_threshold)
// Qdrant не индексирует вовсе и считает это нормой, а сразу после вставки
// оптимизатор может ещё не начать работу — статус уже Green, индекса ещё нет.
func (e *qdrantEngine) Ready(ctx context.Context) error {
	for {
		info, err := e.c.GetCollectionInfo(ctx, coll)
		if err != nil {
			return err
		}
		pts, idx := info.GetPointsCount(), info.GetIndexedVectorsCount()
		if info.GetStatus() == qdrant.CollectionStatus_Green && (pts < 20000 || float64(idx) >= 0.97*float64(pts)) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (e *qdrantEngine) Prepare(context.Context, Params) error { return nil }

func (e *qdrantEngine) Search(ctx context.Context, q []float32, k int, p Params) ([]int32, error) {
	sp := &qdrant.SearchParams{HnswEf: qdrant.PtrOf(uint64(p.Ef)), Exact: qdrant.PtrOf(p.Exact)}
	if p.Oversampling > 0 {
		sp.Quantization = &qdrant.QuantizationSearchParams{Rescore: qdrant.PtrOf(p.Rescore),
			Oversampling: qdrant.PtrOf(p.Oversampling)}
	}
	req := &qdrant.QueryPoints{CollectionName: coll, Query: qdrant.NewQueryDense(q), Limit: qdrant.PtrOf(uint64(k)), Params: sp}
	if p.BucketLt >= 0 {
		req.Filter = &qdrant.Filter{Must: []*qdrant.Condition{qdrant.NewRange("bucket", &qdrant.Range{Lt: qdrant.PtrOf(float64(p.BucketLt))})}}
	}
	res, err := e.c.Query(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]int32, len(res))
	for i, r := range res {
		out[i] = int32(r.GetId().GetNum())
	}
	return out, nil
}

func (e *qdrantEngine) Info(ctx context.Context) (map[string]any, error) {
	info, err := e.c.GetCollectionInfo(ctx, coll)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": info.GetStatus().String(), "segments": info.GetSegmentsCount(),
		"points": info.GetPointsCount(), "indexed_vectors": info.GetIndexedVectorsCount(),
		"optimizer": fmt.Sprint(info.GetOptimizerStatus().GetOk())}, nil
}
