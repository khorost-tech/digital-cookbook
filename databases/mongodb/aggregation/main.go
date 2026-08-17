// Command aggregation — стенд #4 серии "MongoDB: глубокое погружение":
// pipeline `$match->$group->$sort` (использование индекса на $match), цена
// `$lookup` как join (vs эквивалентная агрегация по уже денормализованным
// полям), `$unwind`+`$group` по items[], и лимит памяти blocking-стадий
// (100 МиБ) — с/без `allowDiskUse` — на РЕАЛЬНОМ 3-узловом replica set
// (rs0), поверх уже импортированного датасета (Task 3/4).
//
// Explain аналогично indexes/main.go: `explain` команда сервера
// (verbosity: "executionStats") + ручной разбор bson.D-дерева ответа — у
// mongo-go-driver/v2 нет высокоуровневого Collection.Explain(). У aggregate
// explain (в отличие от find explain, см. indexes/main.go) форма ответа
// ДРУГАЯ: верхний уровень содержит "stages" — массив по одной записи на
// СТАДИЮ ПАЙПЛАЙНА (не запроса), причём сервер может СЛИТЬ несколько
// логических стадий в одну физическую (см. pipelineScenario — $match+$group
// после него оба ушли внутрь одной записи "$cursor", если у планировщика
// была для этого возможность; либо, если решения нет, отдельная "$group").
// Здесь же обнаружился нетривиальный факт (см. lookupScenario) — сервер
// СЛИЛ следующий за $lookup $unwind ВНУТРЬ самого $lookup (поле
// "unwinding" в explain $lookup-стадии) — реальное, не предполагаемое
// поведение оптимизатора.
//
// Сценарии (см. ../ops/aggregation-demo.sh для оркестрации инфраструктуры):
//  1. pipeline $match->$group->$sort — orders: $match(status=paid) должен
//     использовать индекс (IXSCAN, не COLLSCAN) внутри "$cursor"-стадии
//     explain-ответа.
//  2. $lookup как join — orders(unwind items)x products(_id) против
//     ЭКВИВАЛЕНТНОЙ агрегации по уже денормализованным полям items (без
//     join) — реальная разница в стоимости (explain $lookup-стадии +
//     фактическое время обеих агрегаций).
//  3. $unwind+$group по items[] — revenue/qty по product_id, с
//     перекрёстной проверкой (сумма revenue из группировки должна СОВПАСТЬ
//     с суммой orders.total по всей коллекции — это один и тот же total,
//     посчитанный двумя разными путями).
//  4. лимит памяти blocking-стадии (100 МиБ) — $unwind+$sort по
//     некоррелированным с индексом полям: БЕЗ allowDiskUse сервер должен
//     вернуть ошибку (QueryExceededMemoryLimitNoDiskUseAllowed, код 292), С
//     allowDiskUse:true — пройти (внешняя сортировка со сбросом на диск).
//     Честная оговорка (проверено вживую при подготовке стенда): НЕ любой
//     blocking-аккумулятор можно вытолкнуть на диск — $group с $push
//     "$$ROOT" в ОДИН аккумулятор (без реального разбиения на много групп)
//     упирается в тот же лимит и падает С ОШИБКОЙ ДАЖЕ ПРИ allowDiskUse:true
//     ("$push used too much memory and cannot spill to disk", код 146
//     ExceededMemoryLimit) — allowDiskUse спасает внешнюю сортировку и
//     group-by-many-groups, но не единственный распухший аккумулятор
//     одной группы. Поэтому сценарий 4 использует $sort, а не $group —
//     единственная комбинация, где брифовый контраст "без флага
//     ошибка/с флагом проходит" реально воспроизводится.
//
// Ассерты — там, где бриф заявляет конкретный контракт (использование
// индекса на $match, лимит памяти без/с allowDiskUse, совпадение сумм
// revenue) — fail-loud (log.Fatalf). Цена $lookup — записывается как факт
// (FIXTURE-строка) с ассертом на направление и качество (дороже
// эквивалента без join, реальный индекс на foreign-стороне, а не
// COLLSCAN), не на конкретное число (см. FIXTURES.md §4).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const dbName = "cookbook"

// manifest — форма ../dataset/manifest.json (см. dataset/main.go) —
// единственный источник правды по seed/counts всего датасета cookbook.
type manifest struct {
	Seed     int64 `json:"seed"`
	Users    int   `json:"users"`
	Products int   `json:"products"`
	Orders   int   `json:"orders"`
}

func loadManifest() manifest {
	// Стенд всегда запускается с cwd=aggregation/ (см. ops/aggregation-demo.sh:
	// `-w /app/aggregation` в контейнере golang:1.25, где /app — весь каталог
	// mongodb/), поэтому ../dataset/manifest.json всегда рядом.
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

func main() {
	ctx := context.Background()
	m := loadManifest()
	log.Printf("manifest: seed=%d users=%d products=%d orders=%d", m.Seed, m.Users, m.Products, m.Orders)

	mongoURI := mongoconn.MustEnv("MONGO_URI")
	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	db := client.Database(dbName)
	usersColl := db.Collection("users")
	productsColl := db.Collection("products")
	ordersColl := db.Collection("orders")

	assertImportCounts(ctx, m, usersColl, productsColl, ordersColl)

	pipelineScenario(ctx, db, ordersColl)
	lookupScenario(ctx, db, ordersColl)
	unwindGroupScenario(ctx, ordersColl)
	memoryLimitScenario(ctx, ordersColl)

	log.Println("готово.")
}

// assertImportCounts — та же проверка, что и в modeling/indexes: счётчики
// импортированных коллекций должны совпасть с dataset/manifest.json.
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
	fmt.Printf("FIXTURE aggregation: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// -- explain-инфраструктура (тот же приём, что и indexes/main.go) --------

// runExplainE выполняет `explain` команду сервера над АГРЕГАТНЫМ пайплайном
// (explain: {aggregate: coll, pipeline: ..., cursor: {}}, verbosity: ...) —
// форма ответа у aggregate explain ДРУГАЯ, чем у find explain (см. topD
// комментарий в indexes/main.go): верхний уровень содержит "stages", не
// "queryPlanner" напрямую.
func runExplainAggregateE(ctx context.Context, db *mongo.Database, coll string, pipeline mongo.Pipeline, verbosity string) (bson.M, error) {
	cmd := bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "aggregate", Value: coll},
			{Key: "pipeline", Value: pipeline},
			{Key: "cursor", Value: bson.D{}},
		}},
		{Key: "verbosity", Value: verbosity},
	}
	var out bson.M
	err := db.RunCommand(ctx, cmd).Decode(&out)
	return out, err
}

func runExplainAggregate(ctx context.Context, db *mongo.Database, coll string, pipeline mongo.Pipeline, verbosity string) bson.M {
	out, err := runExplainAggregateE(ctx, db, coll, pipeline, verbosity)
	if err != nil {
		log.Fatalf("explain aggregate(%s): %v", coll, err)
	}
	return out
}

func bsonDGet(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// asD приводит any к bson.D, если это возможно, иначе возвращает (nil, false).
func asD(v any) (bson.D, bool) {
	d, ok := v.(bson.D)
	return d, ok
}

// pipelineStages достаёт top-level "stages" (массив, по одной записи на
// СТАДИЮ ПАЙПЛАЙНА) из ответа aggregate explain.
func pipelineStages(out bson.M) bson.A {
	v, ok := out["stages"]
	if !ok {
		log.Fatalf("explain aggregate: ответ не содержит \"stages\"")
	}
	arr, ok := v.(bson.A)
	if !ok {
		log.Fatalf("explain aggregate: \"stages\" неожиданного типа %T", v)
	}
	return arr
}

// findPipelineStage ищет в stages (см. pipelineStages) ПЕРВУЮ ЗАПИСЬ
// (bson.D целиком, не только значение под wantKey), содержащую ключ wantKey
// (например "$cursor", "$lookup", "$group", "$sort") — сервер может слить
// несколько логических стадий в одну физическую запись (см. package doc),
// поэтому "$group" МОЖЕТ отсутствовать как отдельная запись, даже если
// стадия $group есть в пайплайне.
//
// ВАЖНО (реально проверено вживую, не предполагалось заранее): у записи
// стадии статистические поля лежат на РАЗНЫХ уровнях вложенности в
// зависимости от стадии. У "$cursor" queryPlanner/executionStats вложены
// ВНУТРЬ значения самого "$cursor" (entry["$cursor"].queryPlanner). У
// "$lookup"/"$sort" статистика (totalDocsExamined/usedDisk/spills/...) —
// СОСЕДНИЕ ключи ЗАПИСИ, а не вложены внутрь значения "$lookup"/"$sort"
// (которое содержит только конфигурацию стадии — from/localField/... для
// $lookup). Поэтому эта функция возвращает ЦЕЛИКОМ запись (bson.D), а не
// bsonDGet(запись, wantKey) — вызывающий код сам решает, откуда брать
// нужное поле (см. pipelineScenario/lookupScenario).
func findPipelineStage(stages bson.A, wantKey string) (bson.D, bool) {
	for _, el := range stages {
		d, ok := asD(el)
		if !ok {
			continue
		}
		if bsonDGet(d, wantKey) != nil {
			return d, true
		}
	}
	return nil, false
}

// findStage — см. indexes/main.go: рекурсивный поиск стадии find-движка
// (IXSCAN/COLLSCAN/FETCH/...) по дереву winningPlan/queryPlan через
// "inputStage"/"inputStages".
func findStage(v any, name string) (bson.D, bool) {
	d, ok := asD(v)
	if !ok {
		return nil, false
	}
	if s, _ := bsonDGet(d, "stage").(string); s == name {
		return d, true
	}
	if inputStage := bsonDGet(d, "inputStage"); inputStage != nil {
		if found, ok := findStage(inputStage, name); ok {
			return found, true
		}
	}
	if inputStages := bsonDGet(d, "inputStages"); inputStages != nil {
		if arr, ok := inputStages.(bson.A); ok {
			for _, s := range arr {
				if found, ok := findStage(s, name); ok {
					return found, true
				}
			}
		}
	}
	return nil, false
}

func hasStage(v any, name string) bool {
	_, ok := findStage(v, name)
	return ok
}

// planTree достаёт дерево стадий find-движка из winningPlan аggregate
// explain'а. У aggregate explain (verbosity executionStats, сервер
// 8.2.11, SBE-движок) winningPlan обёрнут: {isCached, queryPlan: {...
// дерево ...}, slotBasedPlan: {...}} — то же самое дерево ("stage",
// "inputStage"), что и в классическом find explain (indexes/main.go), но
// на один уровень глубже. Если "queryPlan" отсутствует (классический
// движок вернул дерево прямо в winningPlan) — используем winningPlan как
// есть: реальное поведение сервера может отличаться по версии/плану, а не
// предполагается заранее.
func planTree(winningPlan bson.D) bson.D {
	if qp := bsonDGet(winningPlan, "queryPlan"); qp != nil {
		if d, ok := asD(qp); ok {
			return d
		}
	}
	return winningPlan
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	default:
		return 0
	}
}

func errStrOrNone(err error) string {
	if err == nil {
		return "<none>"
	}
	return err.Error()
}

func isNamespaceNotFound(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 26 // NamespaceNotFound
	}
	return false
}

// -- Сценарий 1: pipeline $match -> $group -> $sort -------------------------

// pipelineScenario — orders: $match(status="paid") -> $group(по user_id:
// totalSpent, orderCount) -> $sort(totalSpent desc). Индекс на status
// должен использоваться ВНУТРИ "$cursor"-стадии explain-ответа (сервер
// проталкивает $match — и, как выяснилось вживую, вместе с ним и $group —
// в единый SBE-план поверх IXSCAN), а не COLLSCAN.
func pipelineScenario(ctx context.Context, db *mongo.Database, ordersColl *mongo.Collection) {
	const idxName = "idx_status_agg"
	if _, err := ordersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "status", Value: 1}},
		Options: options.Index().SetName(idxName),
	}); err != nil {
		log.Fatalf("создать индекс orders.status: %v", err)
	}
	log.Printf("создан индекс %s на orders.status", idxName)

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{{Key: "status", Value: "paid"}}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$user_id"},
			{Key: "totalSpent", Value: bson.D{{Key: "$sum", Value: "$total"}}},
			{Key: "orderCount", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "totalSpent", Value: -1}}}},
	}

	out := runExplainAggregate(ctx, db, "orders", pipeline, "executionStats")
	stages := pipelineStages(out)

	cursorEntry, foundCursor := findPipelineStage(stages, "$cursor")
	if !foundCursor {
		log.Fatalf("assert: explain aggregate должен содержать стадию \"$cursor\" (найденные top-level ключи: %v)", stageKeys(stages))
	}
	// queryPlanner/executionStats вложены ВНУТРЬ значения "$cursor" (см.
	// комментарий findPipelineStage) — ещё один уровень asD, не siblings.
	cursorVal, ok := asD(bsonDGet(cursorEntry, "$cursor"))
	if !ok {
		log.Fatalf("assert: значение \"$cursor\" отсутствует или неожиданного типа")
	}
	qp, ok := asD(bsonDGet(cursorVal, "queryPlanner"))
	if !ok {
		log.Fatalf("assert: \"$cursor\".queryPlanner отсутствует или неожиданного типа")
	}
	winningPlan, ok := asD(bsonDGet(qp, "winningPlan"))
	if !ok {
		log.Fatalf("assert: \"$cursor\".queryPlanner.winningPlan отсутствует или неожиданного типа")
	}
	tree := planTree(winningPlan)

	ixscan, foundIX := findStage(tree, "IXSCAN")
	collscanPresent := hasStage(tree, "COLLSCAN")
	indexName, _ := bsonDGet(ixscan, "indexName").(string)

	es, hasES := asD(bsonDGet(cursorVal, "executionStats"))
	var nReturned, keysExamined, docsExamined int64
	if hasES {
		nReturned = toInt64(bsonDGet(es, "nReturned"))
		keysExamined = toInt64(bsonDGet(es, "totalKeysExamined"))
		docsExamined = toInt64(bsonDGet(es, "totalDocsExamined"))
	}

	// $group слился в "$cursor" (SBE): отдельной "$group"-записи в stages
	// может не быть — это НАБЛЮДЕНИЕ, не проверяемый ассерт (см. package
	// doc). $sort — отдельная поздняя стадия (после группировки данных уже
	// немного, дисковый спилл не ожидается); usedDisk/spills — СОСЕДНИЕ
	// ключи записи "$sort" (см. комментарий findPipelineStage), не вложены
	// внутрь значения "$sort".
	_, groupIsSeparateStage := findPipelineStage(stages, "$group")
	sortStage, foundSort := findPipelineStage(stages, "$sort")
	var sortUsedDisk bool
	var sortSpills int64
	if foundSort {
		sortUsedDisk, _ = bsonDGet(sortStage, "usedDisk").(bool)
		sortSpills = toInt64(bsonDGet(sortStage, "spills"))
	}

	// Реальный прогон (не explain) — фактическое время и результат.
	t0 := time.Now()
	cur, err := ordersColl.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("pipeline match->group->sort: aggregate: %v", err)
	}
	defer cur.Close(ctx)
	type groupRow struct {
		ID         bson.ObjectID `bson:"_id"`
		TotalSpent float64       `bson:"totalSpent"`
		OrderCount int32         `bson:"orderCount"`
	}
	var rows []groupRow
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("pipeline match->group->sort: decode: %v", err)
	}
	wallDur := time.Since(t0)

	var sumOrderCount int64
	for _, r := range rows {
		sumOrderCount += int64(r.OrderCount)
	}

	fmt.Printf("FIXTURE aggregation: pipeline_match_status=paid index=%s ixscan_found=%v collscan_present=%v group_merged_into_cursor=%v n_returned_groups=%d keys_examined=%d docs_examined=%d sort_used_disk=%v sort_spills=%d wall_latency=%v result_groups=%d sum_order_count=%d\n",
		indexName, foundIX, collscanPresent, !groupIsSeparateStage, nReturned, keysExamined, docsExamined, sortUsedDisk, sortSpills, wallDur, len(rows), sumOrderCount)

	if !foundIX || indexName != idxName {
		log.Fatalf("assert: $match(status=paid) должен использовать IXSCAN(%s), got found=%v index=%q", idxName, foundIX, indexName)
	}
	if collscanPresent {
		log.Fatalf("assert: план $match->$group->$sort НЕ должен содержать COLLSCAN")
	}
	if hasES && keysExamined != docsExamined {
		log.Fatalf("assert: равенство-фильтр status=paid по единственному индексному полю должен давать keysExamined==docsExamined, got keys=%d docs=%d", keysExamined, docsExamined)
	}
	if hasES && docsExamined != sumOrderCount {
		log.Fatalf("assert: totalDocsExamined explain'а (%d) должен совпасть с суммой orderCount по фактическому результату (%d) — те же paid-заказы, посчитанные двумя путями", docsExamined, sumOrderCount)
	}
	log.Printf("assert OK: $match(status=paid) -> IXSCAN(%s), без COLLSCAN, keysExamined==docsExamined==%d==sum(orderCount); wall=%v, групп=%d",
		indexName, docsExamined, wallDur, len(rows))
}

func stageKeys(stages bson.A) []string {
	keys := make([]string, 0, len(stages))
	for _, el := range stages {
		d, ok := asD(el)
		if !ok || len(d) == 0 {
			continue
		}
		keys = append(keys, d[0].Key)
	}
	return keys
}

// -- Сценарий 2: $lookup как join --------------------------------------------

// lookupScenario — orders(unwind items) x products(_id) через $lookup
// против ЭКВИВАЛЕНТНОЙ по объёму работы агрегации, использующей уже
// денормализованные в items поля (product_name) — БЕЗ join. Оба пайплайна
// группируют ТУ ЖЕ самую выборку unwound-items (600461 строка на текущем
// датасете), разница — только в наличии $lookup. Explain даёт РЕАЛЬНУЮ
// стоимость $lookup-стадии (totalDocsExamined/totalKeysExamined/
// collectionScans/indexesUsed/executionTimeMillisEstimate), фактический
// прогон — реальную разницу в wall-latency.
func lookupScenario(ctx context.Context, db *mongo.Database, ordersColl *mongo.Collection) {
	withLookup := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "products"},
			{Key: "localField", Value: "items.product_id"},
			{Key: "foreignField", Value: "_id"},
			{Key: "as", Value: "product_doc"},
		}}},
		bson.D{{Key: "$unwind", Value: "$product_doc"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$product_doc.category"},
			{Key: "revenue", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$multiply", Value: bson.A{"$items.qty", "$items.price"}}}}}},
			{Key: "lines", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	withoutLookup := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$items.product_name"},
			{Key: "revenue", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$multiply", Value: bson.A{"$items.qty", "$items.price"}}}}}},
			{Key: "lines", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}

	// explain (executionStats) — реальная стоимость $lookup-стадии.
	out := runExplainAggregate(ctx, db, "orders", withLookup, "executionStats")
	stages := pipelineStages(out)
	lookupStage, foundLookup := findPipelineStage(stages, "$lookup")
	if !foundLookup {
		log.Fatalf("assert: explain aggregate должен содержать стадию \"$lookup\" (top-level ключи: %v)", stageKeys(stages))
	}
	lookupDocsExamined := toInt64(bsonDGet(lookupStage, "totalDocsExamined"))
	lookupKeysExamined := toInt64(bsonDGet(lookupStage, "totalKeysExamined"))
	lookupCollScans := toInt64(bsonDGet(lookupStage, "collectionScans"))
	lookupNReturned := toInt64(bsonDGet(lookupStage, "nReturned"))
	lookupStageMs := toInt64(bsonDGet(lookupStage, "executionTimeMillisEstimate"))
	indexesUsed, _ := bsonDGet(lookupStage, "indexesUsed").(bson.A)
	// unwinding: следующий за $lookup $unwind слился ВНУТРЬ самой
	// $lookup-стадии (реальное поведение оптимизатора, не предположение) —
	// зафиксировано как факт, отдельной проверяемой стадии "$unwind" после
	// $lookup в stages может не быть.
	_, unwindAfterLookupIsSeparate := findPipelineStage(stagesAfter(stages, "$lookup"), "$unwind")

	// Реальные прогоны (не explain) — фактическая разница в wall-latency.
	t0 := time.Now()
	curL, err := ordersColl.Aggregate(ctx, withLookup)
	if err != nil {
		log.Fatalf("$lookup pipeline: aggregate: %v", err)
	}
	var lookupRows []bson.M
	if err := curL.All(ctx, &lookupRows); err != nil {
		log.Fatalf("$lookup pipeline: decode: %v", err)
	}
	lookupWall := time.Since(t0)

	t0 = time.Now()
	curN, err := ordersColl.Aggregate(ctx, withoutLookup)
	if err != nil {
		log.Fatalf("no-lookup equivalent pipeline: aggregate: %v", err)
	}
	var noLookupRows []bson.M
	if err := curN.All(ctx, &noLookupRows); err != nil {
		log.Fatalf("no-lookup equivalent pipeline: decode: %v", err)
	}
	noLookupWall := time.Since(t0)

	ratio := float64(lookupWall) / float64(noLookupWall)

	fmt.Printf("FIXTURE aggregation: lookup_docs_examined=%d lookup_keys_examined=%d lookup_collection_scans=%d lookup_n_returned=%d lookup_indexes_used=%v lookup_stage_time_estimate=%dms lookup_unwind_merged_into_lookup=%v lookup_wall_latency=%v lookup_groups=%d nolookup_wall_latency=%v nolookup_groups=%d cost_ratio=%.1fx\n",
		lookupDocsExamined, lookupKeysExamined, lookupCollScans, lookupNReturned, indexesUsed, lookupStageMs, !unwindAfterLookupIsSeparate, lookupWall, len(lookupRows), noLookupWall, len(noLookupRows), ratio)

	if lookupDocsExamined == 0 {
		log.Fatalf("assert: $lookup должен реально сканировать документы products (totalDocsExamined=0 — подозрительно, join не выполнялся)")
	}
	if lookupCollScans != 0 {
		log.Fatalf("assert: $lookup на products по _id (всегда индексировано) НЕ должен делать collectionScans, got %d", lookupCollScans)
	}
	if lookupWall <= noLookupWall {
		log.Fatalf("assert: $lookup-путь должен быть ДОРОЖЕ эквивалентной агрегации без join (та же выборка unwound items), got lookup=%v <= no_lookup=%v", lookupWall, noLookupWall)
	}
	log.Printf("assert OK: $lookup реально сканирует products через индекс (%v, docsExamined=%d, collectionScans=0) и дороже эквивалента без join в %.1fx (%v vs %v)",
		indexesUsed, lookupDocsExamined, ratio, lookupWall, noLookupWall)
}

// stagesAfter возвращает подмассив stages ПОСЛЕ первой записи с ключом
// afterKey (не включая её саму) — для проверки "следующая логическая
// стадия пайплайна слилась в предыдущую физическую запись или нет".
func stagesAfter(stages bson.A, afterKey string) bson.A {
	for i, el := range stages {
		if d, ok := asD(el); ok {
			if bsonDGet(d, afterKey) != nil {
				if i+1 < len(stages) {
					return stages[i+1:]
				}
				return bson.A{}
			}
		}
	}
	return bson.A{}
}

// -- Сценарий 3: $unwind + $group по items[] ---------------------------------

// unwindGroupScenario — revenue/qty по product_id через $unwind+$group.
// Перекрёстная проверка: сумма revenue по группам ДОЛЖНА совпасть с суммой
// orders.total по всей коллекции (dataset/main.go: order.Total — это
// round2(sum(item.Price*item.Qty)) по построению генератора) — один и тот
// же total, посчитанный двумя разными путями агрегации.
func unwindGroupScenario(ctx context.Context, ordersColl *mongo.Collection) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$items.product_id"},
			{Key: "totalQty", Value: bson.D{{Key: "$sum", Value: "$items.qty"}}},
			{Key: "totalRevenue", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$multiply", Value: bson.A{"$items.qty", "$items.price"}}}}}},
			{Key: "lines", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}

	t0 := time.Now()
	cur, err := ordersColl.Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("$unwind+$group by product_id: aggregate: %v", err)
	}
	type groupRow struct {
		ID           bson.ObjectID `bson:"_id"`
		TotalQty     int64         `bson:"totalQty"`
		TotalRevenue float64       `bson:"totalRevenue"`
		Lines        int32         `bson:"lines"`
	}
	var rows []groupRow
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("$unwind+$group by product_id: decode: %v", err)
	}
	wallDur := time.Since(t0)

	var sumRevenue float64
	var sumLines int64
	for _, r := range rows {
		sumRevenue += r.TotalRevenue
		sumLines += int64(r.Lines)
	}

	// Перекрёстная проверка независимым путём: $group{_id: null, s: $sum
	// orders.total} по всей (НЕ unwound) коллекции.
	crossPipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "s", Value: bson.D{{Key: "$sum", Value: "$total"}}},
		}}},
	}
	crossCur, err := ordersColl.Aggregate(ctx, crossPipeline)
	if err != nil {
		log.Fatalf("cross-check $sum(orders.total): aggregate: %v", err)
	}
	type sumRow struct {
		S float64 `bson:"s"`
	}
	var crossRows []sumRow
	if err := crossCur.All(ctx, &crossRows); err != nil {
		log.Fatalf("cross-check $sum(orders.total): decode: %v", err)
	}
	if len(crossRows) != 1 {
		log.Fatalf("cross-check $sum(orders.total): ожидалась 1 строка, получено %d", len(crossRows))
	}
	crossTotal := crossRows[0].S

	diff := sumRevenue - crossTotal
	if diff < 0 {
		diff = -diff
	}

	fmt.Printf("FIXTURE aggregation: unwind_group_distinct_products=%d unwind_group_lines=%d unwind_group_sum_revenue=%.2f cross_check_sum_orders_total=%.2f diff=%.4f wall_latency=%v\n",
		len(rows), sumLines, sumRevenue, crossTotal, diff, wallDur)

	const epsilon = 0.01 // накопленная погрешность float64 на ~600К сложений
	if diff > epsilon {
		log.Fatalf("assert: сумма revenue из $unwind+$group (%.2f) должна совпасть с $sum(orders.total) по всей коллекции (%.2f) с точностью %.2f, разница=%.4f", sumRevenue, crossTotal, epsilon, diff)
	}
	log.Printf("assert OK: $unwind+$group revenue (%.2f) совпадает с независимым $sum(orders.total) (%.2f), diff=%.4f <= %.2f; %d уникальных product_id, wall=%v",
		sumRevenue, crossTotal, diff, epsilon, len(rows), wallDur)
}

// -- Сценарий 4: лимит памяти blocking-стадии (100 МиБ) ---------------------

// memoryLimitScenario — $unwind+$sort по некоррелированным с индексом
// полям (items.qty, items.product_name, created_at) над ВСЕЙ развёрнутой
// коллекцией (~600К строк) — блокирующая сортировка, которой нужно держать
// весь набор в памяти ДО первого возвращаемого документа. С ЯВНО
// ПЕРЕДАННЫМ allowDiskUse:false — упирается в лимит 100 МиБ и должен
// вернуть ошибку (QueryExceededMemoryLimitNoDiskUseAllowed, код 292). С
// allowDiskUse:true — должен пройти (внешняя сортировка со сбросом
// промежуточных данных на диск), вернув ТО ЖЕ количество строк, что и
// независимая проверка $unwind+$count.
//
// ВАЖНАЯ оговорка про серверный параметр allowDiskUseByDefault (реально
// проверено вживую отдельным CommandMonitor-сравнением при подготовке
// стенда, НЕ предполагалось заранее): "без allowDiskUse" — это НЕ "поле
// allowDiskUse отсутствует в команде", а ИМЕННО "поле allowDiskUse ЯВНО
// равно false". Причина — легко упускаемый серверный параметр
// allowDiskUseByDefault, который по умолчанию true начиная с MongoDB 6.0:
// если per-query опция allowDiskUse вообще не передана, сервер подставляет
// значение этого параметра, а не отдельное умолчание команды aggregate. На
// этом же датасете и той же самой команде тир с ПРОПУЩЕННЫМ полем
// allowDiskUse (mongo.Collection.Aggregate без опций — то, что драйвер
// шлёт по умолчанию) сервер завершает сортировку УСПЕШНО за ~3.5s — ровно
// потому, что allowDiskUseByDefault=true разрешает спилл на диск без
// явного флага. Только ЯВНО переданное allowDiskUse:false форсирует
// старый жёсткий лимит 100 МиБ (код 292). Разница подтверждена дословно
// сравнением сырых команд через *event.CommandMonitor:
//   {"aggregate":"orders", ..., "allowDiskUse": false, ...}      -> код 292, ~150-220ms
//   {"aggregate":"orders", ...(поля allowDiskUse нет вовсе)...}  -> успех, ~3.5s, 600461 строк
//   {"aggregate":"orders", ..., "allowDiskUse": true, ...}       -> успех, ~3.4s, 600461 строк
// Поэтому этот стенд ЯВНО передаёт SetAllowDiskUse(false) для "без флага"
// (не полагается на умолчание клиента) — только так контраст из брифа
// («без флага — ошибка, с флагом — проходит») реально воспроизводится
// детерминированно; бриф под "без флага" имел в виду именно явный false.
// Если бы стенд просто не передавал опцию вовсе (как в первой версии, до
// этой проверки), сценарий 4 давал бы ложный "успех" в обоих случаях
// (ожидаемо, раз allowDiskUseByDefault=true) и уронил бы этот ассерт — что
// и произошло при живом прогоне до этого исправления (см. отчёт задачи).
func memoryLimitScenario(ctx context.Context, ordersColl *mongo.Collection) {
	countPipeline := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$count", Value: "n"}},
	}
	cntCur, err := ordersColl.Aggregate(ctx, countPipeline)
	if err != nil {
		log.Fatalf("$unwind+$count (ожидаемое число строк): aggregate: %v", err)
	}
	type countRow struct {
		N int64 `bson:"n"`
	}
	var cntRows []countRow
	if err := cntCur.All(ctx, &cntRows); err != nil {
		log.Fatalf("$unwind+$count: decode: %v", err)
	}
	if len(cntRows) != 1 {
		log.Fatalf("$unwind+$count: ожидалась 1 строка, получено %d", len(cntRows))
	}
	expectedItems := cntRows[0].N
	log.Printf("ожидаемое число unwound-строк (items[] по всем orders): %d", expectedItems)

	sortPipeline := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$items"}},
		bson.D{{Key: "$sort", Value: bson.D{
			{Key: "items.qty", Value: 1},
			{Key: "items.product_name", Value: 1},
			{Key: "created_at", Value: -1},
		}}},
	}

	// -- С ЯВНЫМ allowDiskUse:false: ожидаем ошибку сервера (лимит 100 МиБ).
	// См. комментарий над функцией — ИМЕННО явное false, не пропуск опции.
	noDiskOpts := options.Aggregate().SetAllowDiskUse(false)
	t0 := time.Now()
	curNoDisk, errNoDisk := ordersColl.Aggregate(ctx, sortPipeline, noDiskOpts)
	noDiskDur := time.Since(t0)
	var noDiskCode int32
	if errNoDisk == nil {
		// Сервер мог не ошибиться сразу на команде aggregate — тогда
		// ошибка (если будет) проявится при вычерпывании курсора.
		var tmp []bson.Raw
		errNoDisk = curNoDisk.All(ctx, &tmp)
		noDiskDur = time.Since(t0)
	}
	var cmdErr mongo.CommandError
	if errors.As(errNoDisk, &cmdErr) {
		noDiskCode = cmdErr.Code
	}

	fmt.Printf("FIXTURE aggregation: memory_limit_no_diskuse_error=%q memory_limit_no_diskuse_code=%d memory_limit_no_diskuse_latency=%v\n",
		errStrOrNone(errNoDisk), noDiskCode, noDiskDur)

	if errNoDisk == nil {
		log.Fatalf("assert: $unwind+$sort по ~%d строкам БЕЗ allowDiskUse должен упереться в лимит памяти (100 МиБ) и вернуть ошибку, но прошёл успешно", expectedItems)
	}
	if noDiskCode != 292 {
		log.Fatalf("assert: ошибка БЕЗ allowDiskUse должна иметь код 292 (QueryExceededMemoryLimitNoDiskUseAllowed), got code=%d err=%v", noDiskCode, errNoDisk)
	}
	log.Printf("assert OK: БЕЗ allowDiskUse — реальная ошибка сервера (код %d): %v", noDiskCode, errNoDisk)

	// -- С allowDiskUse:true: ожидаем успех, тот же набор строк. --
	opts := options.Aggregate().SetAllowDiskUse(true)
	t0 = time.Now()
	curDisk, err := ordersColl.Aggregate(ctx, sortPipeline, opts)
	if err != nil {
		log.Fatalf("assert: $unwind+$sort С allowDiskUse:true должен пройти, но aggregate вернул ошибку: %v", err)
	}
	var actualItems int64
	for curDisk.Next(ctx) {
		actualItems++
	}
	if err := curDisk.Err(); err != nil {
		log.Fatalf("assert: $unwind+$sort С allowDiskUse:true упал при вычерпывании курсора: %v", err)
	}
	_ = curDisk.Close(ctx)
	diskDur := time.Since(t0)

	fmt.Printf("FIXTURE aggregation: memory_limit_with_diskuse_success=true memory_limit_with_diskuse_rows=%d memory_limit_with_diskuse_latency=%v expected_rows=%d\n",
		actualItems, diskDur, expectedItems)

	if actualItems != expectedItems {
		log.Fatalf("assert: С allowDiskUse:true число возвращённых строк (%d) должно совпасть с независимым $unwind+$count (%d)", actualItems, expectedItems)
	}
	log.Printf("assert OK: С allowDiskUse:true — внешняя сортировка прошла успешно за %v, вернула %d строк (совпадает с $unwind+$count)", diskDur, actualItems)
	log.Printf("честная оговорка (проверено вживую при подготовке стенда): allowDiskUse спасает $sort (внешняя сортировка) и $group-by-many-groups, но НЕ единственный распухший $push-аккумулятор ОДНОЙ группы — `$group{_id:null, docs:{$push:\"$$ROOT\"}}` над тем же объёмом падает С ОШИБКОЙ ДАЖЕ ПРИ allowDiskUse:true (\"$push used too much memory and cannot spill to disk\", код 146 ExceededMemoryLimit); поэтому демонстрация лимита памяти в этом стенде — $sort, не $group-с-$push (см. package doc и FIXTURES.md §4)")
}
