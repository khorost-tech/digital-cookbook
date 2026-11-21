package main

import (
	"context"
	"fmt"
	"time"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"

	"vdb/dedicated/vec"
)

type milvusEngine struct{ c *milvusclient.Client }

func newMilvus() *milvusEngine {
	c, err := milvusclient.New(context.Background(), &milvusclient.ClientConfig{Address: "milvus:19530"})
	must(err)
	return &milvusEngine{c}
}

func (e *milvusEngine) Name() string { return "milvus" }

func (e *milvusEngine) Reset(ctx context.Context) error {
	return e.c.DropCollection(ctx, milvusclient.NewDropCollectionOption(coll))
}

// Create — коллекция с уровнем согласованности по умолчанию (Bounded), индекс
// HNSW создаётся на пустой коллекции, коллекция сразу загружается в память:
// вставки идут в растущие (growing) сегменты, по Flush — запечатываются и
// индексируются.
func (e *milvusEngine) Create(ctx context.Context, cfg Config) error {
	if cfg.Quant != "" {
		return fmt.Errorf("milvus: квантизация в стенде не меряется")
	}
	schema := entity.NewSchema().WithName(coll).
		WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeInt64).WithIsPrimaryKey(true)).
		WithField(entity.NewField().WithName("bucket").WithDataType(entity.FieldTypeInt64)).
		WithField(entity.NewField().WithName("emb").WithDataType(entity.FieldTypeFloatVector).WithDim(vec.Dim))
	if err := e.c.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(coll, schema)); err != nil {
		return err
	}
	t, err := e.c.CreateIndex(ctx, milvusclient.NewCreateIndexOption(coll, "emb", index.NewHNSWIndex(entity.IP, cfg.M, cfg.EfC)))
	if err != nil {
		return err
	}
	if err := t.Await(ctx); err != nil {
		return err
	}
	lt, err := e.c.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(coll))
	if err != nil {
		return err
	}
	return lt.Await(ctx)
}

func (e *milvusEngine) Insert(ctx context.Context, ids []int, vecs [][]float32) error {
	id64 := make([]int64, len(ids))
	b := make([]int64, len(ids))
	for i, id := range ids {
		id64[i], b[i] = int64(id), int64(vec.Bucket(id))
	}
	_, err := e.c.Insert(ctx, milvusclient.NewColumnBasedInsertOption(coll).WithInt64Column("id", id64).
		WithInt64Column("bucket", b).WithFloatVectorColumn("emb", vec.Dim, vecs))
	return err
}

// Ready — Flush запечатывает растущие сегменты, затем ждём, пока индекс
// построен по всем строкам.
func (e *milvusEngine) Ready(ctx context.Context) error {
	ft, err := e.c.Flush(ctx, milvusclient.NewFlushOption(coll))
	if err != nil {
		return err
	}
	if err := ft.Await(ctx); err != nil {
		return err
	}
	for {
		d, err := e.c.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(coll, "emb"))
		if err != nil {
			return err
		}
		if d.PendingIndexRows == 0 && d.IndexedRows == d.TotalRows && d.TotalRows > 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (e *milvusEngine) Prepare(context.Context, Params) error { return nil }

func (e *milvusEngine) Search(ctx context.Context, q []float32, k int, p Params) ([]int32, error) {
	opt := milvusclient.NewSearchOption(coll, k, []entity.Vector{entity.FloatVector(q)}).WithANNSField("emb").
		WithAnnParam(index.NewHNSWAnnParam(p.Ef))
	if p.BucketLt >= 0 {
		opt = opt.WithFilter(fmt.Sprintf("bucket < %d", p.BucketLt))
	}
	if p.Strong {
		opt = opt.WithConsistencyLevel(entity.ClStrong)
	}
	rs, err := e.c.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	ids := rs[0].IDs.(*column.ColumnInt64).Data()
	out := make([]int32, len(ids))
	for i, id := range ids {
		out[i] = int32(id)
	}
	return out, nil
}

func (e *milvusEngine) Info(ctx context.Context) (map[string]any, error) {
	d, err := e.c.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(coll, "emb"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"total_rows": d.TotalRows, "indexed_rows": d.IndexedRows, "pending_rows": d.PendingIndexRows}, nil
}
