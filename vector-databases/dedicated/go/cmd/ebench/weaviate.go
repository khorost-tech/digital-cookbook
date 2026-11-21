package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/grpc"
	"github.com/weaviate/weaviate/entities/models"

	"vdb/dedicated/vec"
)

const class = "Doc"

type weaviateEngine struct{ c *weaviate.Client }

// newWeaviate — REST для схемы, gRPC (порт 50051) для пакетной вставки и поиска.
func newWeaviate() *weaviateEngine {
	c, err := weaviate.NewClient(weaviate.Config{Host: "weaviate:8080", Scheme: "http",
		GrpcConfig: &grpc.Config{Host: "weaviate:50051"}, ConnectionClient: &http.Client{Timeout: 10 * time.Minute}})
	must(err)
	return &weaviateEngine{c}
}

func (e *weaviateEngine) Name() string { return "weaviate" }

func (e *weaviateEngine) Reset(ctx context.Context) error {
	ok, err := e.c.Schema().ClassExistenceChecker().WithClassName(class).Do(ctx)
	if err != nil || !ok {
		return err
	}
	return e.c.Schema().ClassDeleter().WithClassName(class).Do(ctx)
}

// Create — класс без векторайзера, HNSW по скалярному произведению. ef для
// поиска в Weaviate — свойство индекса в схеме, его меняет Prepare.
func (e *weaviateEngine) Create(ctx context.Context, cfg Config) error {
	if cfg.Quant != "" {
		return fmt.Errorf("weaviate: квантизация в стенде не меряется")
	}
	vic := map[string]any{"distance": "dot", "maxConnections": cfg.M, "efConstruction": cfg.EfC, "ef": 40}
	if cfg.Filter != "" {
		vic["filterStrategy"] = cfg.Filter
	}
	return e.c.Schema().ClassCreator().WithClass(&models.Class{
		Class: class, Vectorizer: "none", VectorIndexType: "hnsw",
		VectorIndexConfig: vic,
		Properties: []*models.Property{{Name: "rid", DataType: []string{"int"}},
			{Name: "bucket", DataType: []string{"int"}, IndexFilterable: ptr(true), IndexRangeFilters: ptr(true)}},
	}).Do(ctx)
}

func ptr[T any](v T) *T { return &v }

// uuid — детерминированный идентификатор объекта по номеру документа.
func uuid(id int) strfmt.UUID {
	return strfmt.UUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", id))
}

func (e *weaviateEngine) Insert(ctx context.Context, ids []int, vecs [][]float32) error {
	objs := make([]*models.Object, len(ids))
	for i, id := range ids {
		objs[i] = &models.Object{Class: class, ID: uuid(id), Vector: vecs[i],
			Properties: map[string]any{"rid": id, "bucket": vec.Bucket(id)}}
	}
	res, err := e.c.Batch().ObjectsBatcher().WithObjects(objs...).Do(ctx)
	if err != nil {
		return err
	}
	for _, r := range res {
		if r.Result != nil && r.Result.Errors != nil {
			return fmt.Errorf("weaviate batch: %v", r.Result.Errors.Error[0].Message)
		}
	}
	return nil
}

// Ready — индексация синхронная (ASYNC_INDEXING=false): объект в индексе к
// моменту ответа на вставку.
func (e *weaviateEngine) Ready(context.Context) error { return nil }

func (e *weaviateEngine) Prepare(ctx context.Context, p Params) error {
	c, err := e.c.Schema().ClassGetter().WithClassName(class).Do(ctx)
	if err != nil {
		return err
	}
	cfg := c.VectorIndexConfig.(map[string]any)
	if fmt.Sprint(cfg["ef"]) == fmt.Sprint(p.Ef) {
		return nil
	}
	cfg["ef"] = p.Ef
	c.VectorIndexConfig = cfg
	return e.c.Schema().ClassUpdater().WithClass(c).Do(ctx)
}

func (e *weaviateEngine) Search(ctx context.Context, q []float32, k int, p Params) ([]int32, error) {
	g := e.c.GraphQL().Get().WithClassName(class).WithFields(graphql.Field{Name: "rid"}).
		WithNearVector(e.c.GraphQL().NearVectorArgBuilder().WithVector(q)).WithLimit(k)
	if p.BucketLt >= 0 {
		g = g.WithWhere(filters.Where().WithPath([]string{"bucket"}).WithOperator(filters.LessThan).
			WithValueInt(int64(p.BucketLt)))
	}
	resp, err := g.Do(ctx)
	if err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("weaviate: %s", resp.Errors[0].Message)
	}
	items := resp.Data["Get"].(map[string]any)[class].([]any)
	out := make([]int32, len(items))
	for i, it := range items {
		out[i] = int32(it.(map[string]any)["rid"].(float64))
	}
	return out, nil
}

func (e *weaviateEngine) Info(ctx context.Context) (map[string]any, error) {
	st, err := e.c.Cluster().NodesStatusGetter().WithClass(class).WithOutput("verbose").Do(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, n := range st.Nodes {
		for _, s := range n.Shards {
			out["objects"] = s.ObjectCount
			out["indexing_status"] = s.VectorIndexingStatus
			out["vector_queue"] = s.VectorQueueLength
		}
	}
	return out, nil
}
