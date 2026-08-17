// Command modeling — стенд #1 серии "MongoDB: глубокое погружение":
// embed vs reference на реальном датасете rs0 + PG jsonb-контраст.
//
// Использует уже загруженные коллекции cookbook.{users,products,orders}
// (mongoimport, см. ../ops/modeling-demo.sh) и таблицы cookbook.orders_doc/
// orders (psql -f dataset/out/pg-seed.sql, тот же скрипт). Сам стенд:
//
//  1. Проверяет счётчики импортированных коллекций против ../dataset/manifest.json
//     (единственный источник правды по seed/counts, см. dataset/main.go).
//  2. Строит referenced-вариант заказов (orders_ref: items урезаны до
//     {product_id, qty}, без денормализованных product_name/price) через
//     $out — модель "по ссылке", контраст embedded-модели `orders`
//     (items уже полностью денормализованы генератором датасета).
//  3. Меряет на случайной выборке заказов (см. sampleSize) типичный доступ
//     "заказ + его позиции":
//     - embedded: 1 FindOne на заказ (items уже внутри) — 1 round-trip/заказ;
//     - referenced (наивно, по одному подтягиванию): FindOne на заказ +
//       FindOne на каждый item.product_id — 1+N round-trip/заказ;
//     - referenced ($lookup, одним агрегатным вызовом на всю выборку):
//       ОДИН aggregate + сколько потребуется getMore, чтобы драйвер вычерпал
//       курсор (cur.All) — round-trip'ы РЕАЛЬНО измеряются через
//       *event.CommandMonitor на выделенном клиенте (см.
//       scenarioReferencedLookup), не предполагаются равными 1. Честная
//       оговорка про то, что "меньше round-trip'ов" верно только для
//       наивного referenced-пути, остаётся в силе — просто число теперь
//       измеренное, а не буквальная единица.
//  4. Растит документ (growth_demo: одна запись, $push с $each пачками)
//     до упора в лимит BSON-документа (16 МиБ) — что реально происходит.
//  5. PG jsonb-контраст: размер (pg_column_size) и латентность чтения по
//     ключу той же выборки заказов — orders_doc.doc (jsonb) в PG против
//     bson-размера ($bsonSize) и FindOne(projection) в Mongo.
//
// Ассерты — на КАЧЕСТВЕННЫЕ инварианты (см. README «Стенд #1» /
// FIXTURES.md §1), не на конкретные числа: числа стенд производит сам и
// печатает как факты (строки "FIXTURE modeling: ...").
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const (
	dbName     = "cookbook"
	sampleSize = 500

	// growthBatchSize — сколько элементов добавляется за один $push
	// ($each), growthMaxBatches — предохранитель от бесконечного цикла,
	// если по каким-то причинам лимит документа не будет достигнут.
	growthBatchSize  = 100
	growthMaxBatches = 400
	// growthNoteBytes — размер "полезной нагрузки" одного элемента
	// растущего массива; подобран так, чтобы упереться в лимит документа
	// (16 МиБ) за разумное число раундов, см. README «Стенд #1».
	growthNoteBytes = 2000
)

// manifest — форма ../dataset/manifest.json (см. dataset/main.go) —
// единственный источник правды по seed/counts всего датасета cookbook.
type manifest struct {
	Seed     int64 `json:"seed"`
	Users    int   `json:"users"`
	Products int   `json:"products"`
	Orders   int   `json:"orders"`
}

func loadManifest() manifest {
	// Относительный путь — стенд всегда запускается с cwd=modeling/ (см.
	// ops/modeling-demo.sh: `-w /app/modeling` в контейнере golang:1.25,
	// где /app — весь каталог mongodb/), поэтому ../dataset/manifest.json
	// всегда рядом, независимо от того, где физически лежит хост-репо.
	raw, err := os.ReadFile("../dataset/manifest.json")
	if err != nil {
		log.Fatalf("прочитать ../dataset/manifest.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Fatalf("распарсить ../dataset/manifest.json: %v", err)
	}
	return m
}

// -- Доменные структуры для декодирования (подмножество полей dataset/main.go) --

type orderItem struct {
	ProductID bson.ObjectID `bson:"product_id"`
	Qty       int           `bson:"qty"`
}

type orderEmbedded struct {
	ID    bson.ObjectID `bson:"_id"`
	Items []orderItem   `bson:"items"`
}

type orderRef struct {
	ID    bson.ObjectID `bson:"_id"`
	Items []orderItem   `bson:"items"`
}

type product struct {
	ID    bson.ObjectID `bson:"_id"`
	Price float64       `bson:"price"`
}

type sampleID struct {
	ID bson.ObjectID `bson:"_id"`
}

func main() {
	ctx := context.Background()
	m := loadManifest()
	log.Printf("manifest: seed=%d users=%d products=%d orders=%d", m.Seed, m.Users, m.Products, m.Orders)

	mongoURI := mongoconn.MustEnv("MONGO_URI")
	pgDSN := mongoconn.MustEnv("PG_DSN")

	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	pgPool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pg connect: %v", err)
	}
	defer pgPool.Close()
	if err := pgPool.Ping(ctx); err != nil {
		log.Fatalf("pg ping: %v", err)
	}

	db := client.Database(dbName)
	usersColl := db.Collection("users")
	productsColl := db.Collection("products")
	ordersColl := db.Collection("orders")
	ordersRefColl := db.Collection("orders_ref")
	growthColl := db.Collection("growth_demo")

	assertImportCounts(ctx, m, usersColl, productsColl, ordersColl)
	buildOrdersRef(ctx, ordersColl, ordersRefColl, m)

	samples := sampleOrderIDs(ctx, ordersColl, sampleSize)
	log.Printf("выборка для сценариев чтения: %d заказов (случайно, $sample)", len(samples))

	embedRT, embedDur := scenarioEmbedded(ctx, ordersColl, samples)
	refRT, refDur, refItems := scenarioReferencedNaive(ctx, ordersRefColl, productsColl, samples)
	lookupRT, lookupAggregateCalls, lookupGetMoreCalls, lookupDur, lookupOrders := scenarioReferencedLookup(ctx, mongoURI, dbName, samples)

	fmt.Printf("FIXTURE modeling: sample_orders=%d embed_round_trips=%d embed_latency=%v ref_naive_round_trips=%d ref_naive_latency=%v ref_naive_items=%d ref_lookup_round_trips=%d ref_lookup_aggregate_calls=%d ref_lookup_getmore_calls=%d ref_lookup_latency=%v ref_lookup_orders_returned=%d\n",
		len(samples), embedRT, embedDur, refRT, refDur, refItems, lookupRT, lookupAggregateCalls, lookupGetMoreCalls, lookupDur, lookupOrders)

	if embedRT >= refRT {
		log.Fatalf("assert: embed должен экономить round-trips vs наивный referenced, got embed=%d ref_naive=%d", embedRT, refRT)
	}
	if lookupRT >= refRT {
		log.Fatalf("assert: $lookup-referenced (batched) должен делать меньше round-trips чем наивный referenced, got lookup=%d ref_naive=%d", lookupRT, refRT)
	}
	log.Printf("assert OK: embed_round_trips(%d) < ref_naive_round_trips(%d); ref_lookup_round_trips(%d) < ref_naive_round_trips(%d)", embedRT, refRT, lookupRT, refRT)
	log.Printf("честная оговорка: ref_lookup_round_trips(%d) = %d aggregate + %d getMore, РЕАЛЬНО измерено *event.CommandMonitor на выделенном клиенте (курсор по выборке из %d заказов, дефолтный первый батч ~101 документ тянет getMore) на ВСЮ выборку сразу, это НЕ apples-to-apples с embed_round_trips(%d) на заказ; см. FIXTURES.md §1", lookupRT, lookupAggregateCalls, lookupGetMoreCalls, len(samples), embedRT)

	growthResult := scenarioGrowth(ctx, growthColl)
	fmt.Printf("FIXTURE modeling: growth_batches_ok=%d growth_items_ok=%d growth_last_size_bytes=%d growth_hit_limit=%v growth_limit_err=%q growth_latency_first10pct=%v growth_latency_last10pct=%v\n",
		growthResult.batchesOK, growthResult.itemsOK, growthResult.lastSizeBytes, growthResult.hitLimit, growthResult.limitErr, growthResult.avgFirst10, growthResult.avgLast10)

	if growthResult.batchesOK < 2 {
		log.Fatalf("assert: рост документа должен быть наблюдаем минимум на нескольких успешных батчах, got batchesOK=%d", growthResult.batchesOK)
	}
	if !sort.SliceIsSorted(growthResult.sizes, func(i, j int) bool { return growthResult.sizes[i] < growthResult.sizes[j] }) {
		log.Fatalf("assert: размер растущего документа должен строго увеличиваться от батча к батчу, got sizes=%v", growthResult.sizes)
	}
	log.Printf("assert OK: рост документа наблюдаем — %d успешных батчей, размер строго возрастал %d -> %d байт", growthResult.batchesOK, growthResult.sizes[0], growthResult.lastSizeBytes)

	pgResult := scenarioPGContrast(ctx, pgPool, ordersColl, samples)
	fmt.Printf("FIXTURE modeling: pg_jsonb_avg_size_bytes=%d mongo_bson_avg_size_bytes=%d pg_key_read_avg_latency=%v mongo_key_read_avg_latency=%v pg_reads_ok=%d mongo_reads_ok=%d sample=%d\n",
		pgResult.pgAvgSize, pgResult.mongoAvgSize, pgResult.pgAvgLatency, pgResult.mongoAvgLatency, pgResult.pgReadsOK, pgResult.mongoReadsOK, len(samples))

	if pgResult.pgReadsOK != len(samples) || pgResult.mongoReadsOK != len(samples) {
		log.Fatalf("assert: все чтения PG jsonb и Mongo для выборки должны быть успешны, got pg_ok=%d mongo_ok=%d sample=%d", pgResult.pgReadsOK, pgResult.mongoReadsOK, len(samples))
	}
	log.Printf("assert OK: PG jsonb / Mongo key-read — все %d чтений в обеих системах успешны", len(samples))

	log.Println("готово.")
}

// assertImportCounts проверяет, что импорт mongoimport дал ровно те же
// счётчики, что зафиксированы в dataset/manifest.json (единственный
// источник правды по seed/counts).
func assertImportCounts(ctx context.Context, m manifest, usersColl, productsColl, ordersColl *mongo.Collection) {
	uc, err := usersColl.CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments users: %v", err)
	}
	pc, err := productsColl.CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments products: %v", err)
	}
	oc, err := ordersColl.CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments orders: %v", err)
	}
	fmt.Printf("FIXTURE modeling: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// buildOrdersRef строит referenced-вариант заказов: items урезаны до
// {product_id, qty} (product_name/price убраны — они денормализованы в
// исходном `orders` специально генератором датасета ради embedded-модели).
// $out — идемпотентно перезаписывает коллекцию целиком при каждом прогоне.
func buildOrdersRef(ctx context.Context, ordersColl, ordersRefColl *mongo.Collection, m manifest) {
	t0 := time.Now()
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "status", Value: 1},
			{Key: "created_at", Value: 1},
			{Key: "total", Value: 1},
			{Key: "items", Value: bson.D{{Key: "$map", Value: bson.D{
				{Key: "input", Value: "$items"},
				{Key: "as", Value: "it"},
				{Key: "in", Value: bson.D{
					{Key: "product_id", Value: "$$it.product_id"},
					{Key: "qty", Value: "$$it.qty"},
				}},
			}}}},
		}}},
		bson.D{{Key: "$out", Value: ordersRefColl.Name()}},
	}
	cur, err := ordersColl.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("build orders_ref ($out): %v", err)
	}
	_ = cur.Close(ctx)
	dur := time.Since(t0)

	rc, err := ordersRefColl.CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments orders_ref: %v", err)
	}
	fmt.Printf("FIXTURE modeling: orders_ref_build_latency=%v orders_ref_count=%d\n", dur, rc)
	if int(rc) != m.Orders {
		log.Fatalf("assert: orders_ref должен содержать столько же документов, сколько orders (%d != %d)", rc, m.Orders)
	}
}

// sampleOrderIDs — случайная выборка _id заказов через $sample (реальный
// случайный срез, не первые N по порядку вставки/индекса).
func sampleOrderIDs(ctx context.Context, ordersColl *mongo.Collection, n int) []bson.ObjectID {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: n}}}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cur, err := ordersColl.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("$sample orders: %v", err)
	}
	defer cur.Close(ctx)
	var rows []sampleID
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("$sample orders decode: %v", err)
	}
	ids := make([]bson.ObjectID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// scenarioEmbedded — "заказ + его позиции" на embedded-модели: 1 FindOne
// на заказ, items уже внутри документа (денормализованы генератором).
func scenarioEmbedded(ctx context.Context, ordersColl *mongo.Collection, ids []bson.ObjectID) (roundTrips int, dur time.Duration) {
	t0 := time.Now()
	for _, id := range ids {
		var o orderEmbedded
		if err := ordersColl.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&o); err != nil {
			log.Fatalf("embedded FindOne(%s): %v", id.Hex(), err)
		}
		roundTrips++
		_ = o
	}
	return roundTrips, time.Since(t0)
}

// scenarioReferencedNaive — "заказ + его позиции" на referenced-модели,
// наивно (типичный app-код без $lookup): FindOne на заказ, затем по
// FindOne на products за каждый item.product_id — 1+N round-trip/заказ.
func scenarioReferencedNaive(ctx context.Context, ordersRefColl, productsColl *mongo.Collection, ids []bson.ObjectID) (roundTrips int, dur time.Duration, totalItems int) {
	t0 := time.Now()
	for _, id := range ids {
		var o orderRef
		if err := ordersRefColl.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&o); err != nil {
			log.Fatalf("referenced FindOne order(%s): %v", id.Hex(), err)
		}
		roundTrips++
		for _, it := range o.Items {
			var p product
			if err := productsColl.FindOne(ctx, bson.D{{Key: "_id", Value: it.ProductID}}).Decode(&p); err != nil {
				log.Fatalf("referenced FindOne product(%s): %v", it.ProductID.Hex(), err)
			}
			roundTrips++
			totalItems++
		}
	}
	return roundTrips, time.Since(t0), totalItems
}

// scenarioReferencedLookup — та же выборка, но ОДНИМ агрегатным вызовом на
// ВСЮ выборку сразу ($match {_id:{$in:...}} + $lookup products) — честная
// оговорка: это НЕ apples-to-apples с per-заказ round-trip'ами embedded/
// referenced-наивного сценариев, а иллюстрация того, что "меньше round-
// trip'ов" для referenced-модели верно ТОЛЬКО для наивного пути (см. лог
// стенда и FIXTURES.md §1).
//
// round-trips здесь — РЕАЛЬНО измеренное число сетевых команд, а не
// предположение: cur.All вычерпывает курсор агрегации, и при выборке 500
// заказов дефолтный первый батч (~101 документ) драйвер добирает
// дополнительными командами getMore — КАЖДАЯ из них отдельный сетевой
// round-trip, который наивное "1 round-trip на всю выборку" не учитывало.
// Считаем через *event.CommandMonitor (go.mongodb.org/mongo-driver/v2/event)
// на ВЫДЕЛЕННОМ клиенте (отдельное соединение только для этого сценария),
// чтобы событиями aggregate/getMore других сценариев (buildOrdersRef,
// sampleOrderIDs, docBSONSize и т.д. на основном клиенте) счётчик не
// засорялся.
func scenarioReferencedLookup(ctx context.Context, mongoURI, dbName string, ids []bson.ObjectID) (roundTrips, aggregateCalls, getMoreCalls int, dur time.Duration, ordersReturned int) {
	var aggCount, getMoreCount int64
	monitor := &event.CommandMonitor{
		Started: func(_ context.Context, evt *event.CommandStartedEvent) {
			switch evt.CommandName {
			case "aggregate":
				atomic.AddInt64(&aggCount, 1)
			case "getMore":
				atomic.AddInt64(&getMoreCount, 1)
			}
		},
	}
	clientOpts := options.Client().ApplyURI(mongoURI).SetMonitor(monitor)
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		log.Fatalf("$lookup: выделенный клиент с CommandMonitor: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("$lookup: ping выделенного клиента: %v", err)
	}
	ordersRefColl := client.Database(dbName).Collection("orders_ref")

	t0 := time.Now()
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}}},
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "products"},
			{Key: "localField", Value: "items.product_id"},
			{Key: "foreignField", Value: "_id"},
			{Key: "as", Value: "resolved_products"},
		}}},
	}
	cur, err := ordersRefColl.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("$lookup orders_ref: %v", err)
	}
	defer cur.Close(ctx)
	var rows []bson.M
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("$lookup orders_ref decode: %v", err)
	}
	dur = time.Since(t0)
	aggregateCalls = int(atomic.LoadInt64(&aggCount))
	getMoreCalls = int(atomic.LoadInt64(&getMoreCount))
	return aggregateCalls + getMoreCalls, aggregateCalls, getMoreCalls, dur, len(rows)
}

type growthResult struct {
	batchesOK     int
	itemsOK       int
	lastSizeBytes int64
	hitLimit      bool
	limitErr      string
	avgFirst10    time.Duration
	avgLast10     time.Duration
	sizes         []int64
}

type bsonSizeDoc struct {
	Size int64 `bson:"size"`
}

// docBSONSize — реальный BSON-размер документа _id=id в coll, посчитанный
// СЕРВЕРОМ через $bsonSize (агрегатный оператор) — не оценка на клиенте.
// Fatal-обёртка над docBSONSizeE для мест, где сбой действительно должен
// остановить стенд (scenarioGrowth — там сам факт неудачи $bsonSize внутри
// цикла роста — не часть измеряемого сценария и должен падать громко).
func docBSONSize(ctx context.Context, coll *mongo.Collection, id any) int64 {
	size, err := docBSONSizeE(ctx, coll, id)
	if err != nil {
		log.Fatalf("%v", err)
	}
	return size
}

// docBSONSizeE — то же самое, но БЕЗ Fatal: возвращает ошибку вызывающему
// коду. Нужна там, где отдельный сбой на одном сэмпле не должен ронять весь
// стенд, а должен накапливаться как реальный неуспех (см. scenarioPGContrast
// и FIXTURES.md §1 — ассерт "все чтения выборки успешны" должен быть
// осмысленной, а не тавтологической проверкой).
func docBSONSizeE(ctx context.Context, coll *mongo.Collection, id any) (int64, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{{Key: "_id", Value: id}}}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "size", Value: bson.D{{Key: "$bsonSize", Value: "$$ROOT"}}}}}},
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("$bsonSize: %w", err)
	}
	defer cur.Close(ctx)
	var rows []bsonSizeDoc
	if err := cur.All(ctx, &rows); err != nil {
		return 0, fmt.Errorf("$bsonSize decode: %w", err)
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("$bsonSize: ожидался 1 документ, получено %d", len(rows))
	}
	return rows[0].Size, nil
}

// scenarioGrowth — растит один документ (массив items) пачками $push с
// $each, пока сервер не откажет (BSON-документ упёрся в лимит 16 МиБ) или
// не будет достигнут предохранитель growthMaxBatches. Записывает РЕАЛЬНОЕ
// поведение (латентность на батч, финальный размер, текст ошибки, если
// лимит был достигнут) — без предположений заранее.
func scenarioGrowth(ctx context.Context, growthColl *mongo.Collection) growthResult {
	const growthDocID = "growth-demo-1"
	if err := growthColl.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("drop growth_demo: %v", err)
	}
	if _, err := growthColl.InsertOne(ctx, bson.D{{Key: "_id", Value: growthDocID}, {Key: "items", Value: bson.A{}}}); err != nil {
		log.Fatalf("insert growth_demo seed doc: %v", err)
	}

	note := strings.Repeat("x", growthNoteBytes)
	var res growthResult
	var latencies []time.Duration

	for batch := 0; batch < growthMaxBatches; batch++ {
		items := make(bson.A, 0, growthBatchSize)
		for k := 0; k < growthBatchSize; k++ {
			idx := batch*growthBatchSize + k
			items = append(items, bson.D{{Key: "idx", Value: idx}, {Key: "note", Value: note}})
		}
		update := bson.D{{Key: "$push", Value: bson.D{{Key: "items", Value: bson.D{{Key: "$each", Value: items}}}}}}

		t0 := time.Now()
		_, err := growthColl.UpdateOne(ctx, bson.D{{Key: "_id", Value: growthDocID}}, update)
		batchDur := time.Since(t0)

		if err != nil {
			res.hitLimit = true
			res.limitErr = err.Error()
			log.Printf("growth: батч %d упёрся в лимит документа: %v", batch, err)
			break
		}

		latencies = append(latencies, batchDur)
		res.batchesOK++
		res.itemsOK += growthBatchSize
		size := docBSONSize(ctx, growthColl, growthDocID)
		res.sizes = append(res.sizes, size)
		res.lastSizeBytes = size
	}

	if n := len(latencies); n > 0 {
		tenth := n / 10
		if tenth < 1 {
			tenth = 1
		}
		res.avgFirst10 = avgDuration(latencies[:min(tenth, n)])
		res.avgLast10 = avgDuration(latencies[max(0, n-tenth):])
	}
	return res
}

func avgDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return sum / time.Duration(len(ds))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isNamespaceNotFound(err error) bool {
	// Drop() коллекции, которой ещё нет (первый прогон стенда) —
	// не ошибка сценария, а идемпотентность повторного запуска.
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 26 // NamespaceNotFound
	}
	return false
}

type pgContrastResult struct {
	pgAvgSize       int64
	mongoAvgSize    int64
	pgAvgLatency    time.Duration
	mongoAvgLatency time.Duration
	pgReadsOK       int
	mongoReadsOK    int
}

// scenarioPGContrast — тот же документ (заказ), что в Mongo (embedded
// orders), как jsonb в PG (orders_doc, заполнена dataset/out/pg-seed.sql):
// размер (pg_column_size — реальный размер хранения колонки, включая
// возможный TOAST) против $bsonSize в Mongo, и латентность чтения ОДНОГО
// поля по ключу (status) — по одному запросу на заказ в обеих системах
// (не батчами — сопоставимо со scenarioEmbedded/scenarioReferencedNaive).
//
// PG- и Mongo-стороны обрабатываются НЕЗАВИСИМО друг от друга и НЕ
// прерывают выборку целиком на первой ошибке: сбой на одном сэмпле в одной
// системе логируется и пропускается (в средние size/latency не попадает),
// но остальные сэмплы и другая система продолжают обрабатываться. Поэтому
// pgReadsOK/mongoReadsOK — РЕАЛЬНЫЕ счётчики успехов, которые могут разойтись
// с len(ids) и друг с другом, если что-то пойдёт не так — а не тавтология,
// которая всегда равна len(ids) потому что любой сбой уже уронил бы процесс
// раньше (см. FIXTURES.md §1 / ревью Task 3).
func scenarioPGContrast(ctx context.Context, pgPool *pgxpool.Pool, ordersColl *mongo.Collection, ids []bson.ObjectID) pgContrastResult {
	var res pgContrastResult
	var pgSizes, mongoSizes []int64
	var pgLatencies, mongoLatencies []time.Duration

	proj := options.FindOne().SetProjection(bson.D{{Key: "status", Value: 1}})
	for _, id := range ids {
		hexID := id.Hex()

		if size, dur, err := pgReadOne(ctx, pgPool, hexID); err != nil {
			log.Printf("pg contrast: сэмпл %s пропущен (не в pgReadsOK): %v", hexID, err)
		} else {
			pgSizes = append(pgSizes, size)
			pgLatencies = append(pgLatencies, dur)
			res.pgReadsOK++
		}

		if size, dur, err := mongoReadOne(ctx, ordersColl, id, proj); err != nil {
			log.Printf("pg contrast: сэмпл %s пропущен (не в mongoReadsOK): %v", hexID, err)
		} else {
			mongoSizes = append(mongoSizes, size)
			mongoLatencies = append(mongoLatencies, dur)
			res.mongoReadsOK++
		}
	}

	res.pgAvgSize = avgInt64(pgSizes)
	res.mongoAvgSize = avgInt64(mongoSizes)
	res.pgAvgLatency = avgDuration(pgLatencies)
	res.mongoAvgLatency = avgDuration(mongoLatencies)
	return res
}

// pgReadOne — размер колонки (pg_column_size) + латентность чтения status
// по ключу для ОДНОГО заказа в PG. Обе операции должны пройти, иначе сэмпл
// целиком не засчитывается в pgReadsOK (возвращается ошибка, ничего не
// логируется здесь — это дело вызывающего кода).
func pgReadOne(ctx context.Context, pgPool *pgxpool.Pool, hexID string) (size int64, dur time.Duration, err error) {
	if err := pgPool.QueryRow(ctx, `SELECT pg_column_size(doc) FROM orders_doc WHERE id = $1`, hexID).Scan(&size); err != nil {
		return 0, 0, fmt.Errorf("pg pg_column_size(%s): %w", hexID, err)
	}

	t0 := time.Now()
	var status string
	if err := pgPool.QueryRow(ctx, `SELECT doc->>'status' FROM orders_doc WHERE id = $1`, hexID).Scan(&status); err != nil {
		return 0, 0, fmt.Errorf("pg key-read status(%s): %w", hexID, err)
	}
	return size, time.Since(t0), nil
}

// mongoReadOne — $bsonSize + латентность чтения status по ключу для ОДНОГО
// заказа в Mongo. Та же логика "всё-или-ничего для этого сэмпла", что и в
// pgReadOne, только для mongoReadsOK.
func mongoReadOne(ctx context.Context, ordersColl *mongo.Collection, id bson.ObjectID, proj *options.FindOneOptionsBuilder) (size int64, dur time.Duration, err error) {
	size, err = docBSONSizeE(ctx, ordersColl, id)
	if err != nil {
		return 0, 0, fmt.Errorf("mongo $bsonSize(%s): %w", id.Hex(), err)
	}

	t0 := time.Now()
	var doc bson.M
	if err := ordersColl.FindOne(ctx, bson.D{{Key: "_id", Value: id}}, proj).Decode(&doc); err != nil {
		return 0, 0, fmt.Errorf("mongo key-read status(%s): %w", id.Hex(), err)
	}
	return size, time.Since(t0), nil
}

func avgInt64(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += x
	}
	return sum / int64(len(xs))
}
