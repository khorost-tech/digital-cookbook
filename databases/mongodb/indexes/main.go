// Command indexes — стенд #3 серии "MongoDB: глубокое погружение":
// индексы вглубь — multikey, правило ESR (Equality→Sort→Range), partial/TTL
// индексы, covered query — на РЕАЛЬНОМ 3-узловом replica set (rs0), поверх
// уже импортированного датасета (Task 3).
//
// В отличие от modeling/wiredtiger, которые опираются на high-level методы
// драйвера, этот стенд идёт напрямую через команду `explain` сервера
// (db.RunCommand({explain: {find: ..., filter: ..., sort: ..., hint: ...},
// verbosity: "executionStats"})) — у mongo-go-driver/v2 нет высокоуровневого
// Collection.Explain() (в отличие от некоторых старых/неофициальных
// драйверов), поэтому план и его статистика разбираются вручную из
// bson.D-дерева ответа (см. findStage/bsonDGet — тот же приём, что и в
// wiredtiger/main.go для serverStatus().wiredTiger).
//
// Сценарии (см. ../ops/indexes-demo.sh для оркестрации инфраструктуры):
//  1. multikey — индекс на users.tags (массив строк): запрос tags:"vip"
//     должен использовать IXSCAN с isMultiKey=true, без COLLSCAN.
//  2. ESR (Equality→Sort→Range) — два compound-индекса на orders с ОДНИМИ и
//     теми же полями (status, created_at, total), но в разном порядке:
//     idx_esr_correct (E,S,R = status,created_at,total) и idx_esr_wrong
//     (E,R,S = status,total,created_at). Один и тот же запрос
//     (status="paid" equality + total>500 range, sort по created_at) с
//     explicit .hint() на каждый индекс — контраст: ESR-верный порядок даёт
//     план БЕЗ blocking SORT-стадии, ESR-неверный порядок — план С SORT.
//  3. partial index — индекс на orders.total С partialFilterExpression
//     (total>1000). Запрос-подмножество (total>1500) реально его
//     использует (через hint). Запрос вне partial-фильтра (total>500,
//     пересекается, но не подмножество) — планировщик БЕЗ hint НЕ выбирает
//     partial-индекс (реальное поведение оптимизатора, не предположение).
//     Дополнительно — живое наблюдение (не жёсткий assert): что происходит,
//     если всё равно форсировать hint на несовместимый запрос.
//  4. TTL index — отдельная коллекция ttl_demo, индекс с
//     expireAfterSeconds=0 на поле expires_at (Date). Документ с
//     expires_at в прошлом должен быть удалён фоновым TTL monitor'ом
//     (по умолчанию каждые ~60s) — стенд реально ждёт (poll, до 120s) и
//     фиксирует, сколько это заняло по факту; документ с expires_at в
//     будущем должен остаться нетронутым за то же окно.
//  5. covered query — индекс {category:1, price:1} на products, запрос с
//     проекцией {category:1, price:1, _id:0} (проекция — подмножество
//     ключей индекса, _id явно исключён) должен полностью обслуживаться
//     из индекса: PROJECTION_COVERED в winningPlan, totalDocsExamined=0.
//
// Все explain-планы выполняются с default ReadPreference клиента (primary)
// — та же нода, где были только что созданы индексы.
//
// Ассерты — там, где брифом заявлен конкретный контракт поведения оптимизатора
// (multikey, ESR-контраст, covered query, "partial-индекс не выбирается
// планировщиком для несовместимого запроса") — fail-loud (log.Fatalf). Там,
// где бриф допускает несколько вариантов реального поведения сервера
// (форсированный hint на несовместимый partial-индекс, фактическое время
// срабатывания TTL monitor'а) — стенд печатает РЕАЛЬНОЕ наблюдение как факт
// и не форсирует искусственный "зелёный" ассерт (см. FIXTURES.md §3).
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
	// Стенд всегда запускается с cwd=indexes/ (см. ops/indexes-demo.sh:
	// `-w /app/indexes` в контейнере golang:1.25, где /app — весь каталог
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

	multikeyScenario(ctx, db, usersColl)
	esrScenario(ctx, db, ordersColl)
	partialScenario(ctx, db, ordersColl)
	ttlScenario(ctx, db)
	coveredScenario(ctx, db, productsColl)

	log.Println("готово.")
}

// assertImportCounts — та же проверка, что и в modeling/wiredtiger: счётчики
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
	fmt.Printf("FIXTURE indexes: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// -- explain-инфраструктура: RunCommand({explain: {...}, verbosity: ...}) --
// -- + разбор bson.D-дерева ответа. --------------------------------------

// runExplainE выполняет `explain` команду сервера (fail-loud обёртки нет —
// вызывающий код сам решает, Fatal это или ожидаемо-неопределённое
// поведение, см. partialScenario) и возвращает распакованный ответ целиком.
func runExplainE(ctx context.Context, db *mongo.Database, explainTarget bson.D, verbosity string) (bson.M, error) {
	cmd := bson.D{
		{Key: "explain", Value: explainTarget},
		{Key: "verbosity", Value: verbosity},
	}
	var out bson.M
	err := db.RunCommand(ctx, cmd).Decode(&out)
	return out, err
}

// runExplain — Fatal-обёртка над runExplainE для сценариев, где explain
// обязан пройти успешно (сам факт ошибки — уже провал сценария).
func runExplain(ctx context.Context, db *mongo.Database, explainTarget bson.D, verbosity string) bson.M {
	out, err := runExplainE(ctx, db, explainTarget, verbosity)
	if err != nil {
		log.Fatalf("explain %v: %v", explainTarget, err)
	}
	return out
}

// topD достаёт top-level поле key из ответа explain как bson.D — вложенные
// BSON-документы драйвер декодирует в bson.M-значение КАК bson.D (см. тот
// же приём и комментарий в wiredtiger/main.go: snapshotWT), не bson.M.
func topD(m bson.M, key string) bson.D {
	v, ok := m[key]
	if !ok {
		log.Fatalf("explain: ответ не содержит %q", key)
	}
	d, ok := v.(bson.D)
	if !ok {
		log.Fatalf("explain: поле %q неожиданного типа %T", key, v)
	}
	return d
}

// bsonDGet ищет значение по ключу в bson.D (линейный поиск — планы explain
// содержат считанные поля на уровень, вызывается считанные разы за прогон).
func bsonDGet(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// findStage рекурсивно ищет в дереве winningPlan/executionStages первую
// стадию с именем name (поле "stage"), спускаясь через "inputStage"
// (одиночная вложенная стадия) и "inputStages" (массив — AND_SORTED, OR,
// SORT_MERGE и т.п.). Возвращает саму стадию как bson.D.
func findStage(v any, name string) (bson.D, bool) {
	d, ok := v.(bson.D)
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

// toInt64 — та же служебная конвертация числовых полей explain-ответа, что
// и toInt64 в wiredtiger/main.go: сервер сериализует числа как
// int32/int64/float64 в зависимости от значения.
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

// -- Сценарий 1: multikey ---------------------------------------------------

// multikeyScenario — индекс на users.tags (массив строк, 2..5 тегов на
// пользователя, см. dataset/main.go genUsers). Запрос по одному значению
// тега должен использовать multikey IXSCAN, не COLLSCAN.
func multikeyScenario(ctx context.Context, db *mongo.Database, usersColl *mongo.Collection) {
	idxName, err := usersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "tags", Value: 1}},
		Options: options.Index().SetName("idx_tags_multikey"),
	})
	if err != nil {
		log.Fatalf("создать индекс users.tags: %v", err)
	}
	log.Printf("создан индекс %s на users.tags", idxName)

	explainTarget := bson.D{
		{Key: "find", Value: "users"},
		{Key: "filter", Value: bson.D{{Key: "tags", Value: "vip"}}},
	}
	out := runExplain(ctx, db, explainTarget, "executionStats")
	qp := topD(out, "queryPlanner")
	winningPlan := bsonDGet(qp, "winningPlan")
	es := topD(out, "executionStats")

	nReturned := toInt64(bsonDGet(es, "nReturned"))
	keysExamined := toInt64(bsonDGet(es, "totalKeysExamined"))
	docsExamined := toInt64(bsonDGet(es, "totalDocsExamined"))

	ixscan, foundIX := findStage(winningPlan, "IXSCAN")
	collscanPresent := hasStage(winningPlan, "COLLSCAN")
	isMultiKey, _ := bsonDGet(ixscan, "isMultiKey").(bool)
	indexName, _ := bsonDGet(ixscan, "indexName").(string)

	fmt.Printf("FIXTURE indexes: multikey_filter=tags:vip index=%s ixscan_found=%v is_multikey=%v collscan_present=%v n_returned=%d keys_examined=%d docs_examined=%d\n",
		indexName, foundIX, isMultiKey, collscanPresent, nReturned, keysExamined, docsExamined)

	if !foundIX {
		log.Fatalf("assert: запрос по tags должен использовать IXSCAN, стадия не найдена в winningPlan")
	}
	if collscanPresent {
		log.Fatalf("assert: запрос по tags НЕ должен использовать COLLSCAN")
	}
	if !isMultiKey {
		log.Fatalf("assert: индекс %s на массиве users.tags должен быть multikey (isMultiKey=true), got false", indexName)
	}
	log.Printf("assert OK: multikey-запрос tags=vip -> IXSCAN(%s), isMultiKey=true, COLLSCAN отсутствует; keysExamined=%d docsExamined=%d nReturned=%d",
		indexName, keysExamined, docsExamined, nReturned)
}

// -- Сценарий 2: ESR (Equality -> Sort -> Range) -----------------------------

// esrScenario — контраст двух compound-индексов на orders с ОДНИМИ и теми
// же тремя полями (status, created_at, total), но в разном порядке:
// idx_esr_correct следует правилу ESR (Equality: status, Sort: created_at,
// Range: total), idx_esr_wrong нарушает его (Equality: status, Range:
// total, Sort: created_at). Один и тот же запрос — status="paid" (equality)
// + total>500 (range) + sort по created_at — форсируется через .hint() на
// каждый индекс по очереди, чтобы явно сравнить планы для ОДНОГО и того же
// запроса на РАЗНЫХ индексах (без hint планировщик сам выбрал бы только
// один из них, и контраста было бы не увидеть).
func esrScenario(ctx context.Context, db *mongo.Database, ordersColl *mongo.Collection) {
	if _, err := ordersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}, {Key: "total", Value: 1}},
		Options: options.Index().SetName("idx_esr_correct"),
	}); err != nil {
		log.Fatalf("создать idx_esr_correct: %v", err)
	}
	if _, err := ordersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "status", Value: 1}, {Key: "total", Value: 1}, {Key: "created_at", Value: 1}},
		Options: options.Index().SetName("idx_esr_wrong"),
	}); err != nil {
		log.Fatalf("создать idx_esr_wrong: %v", err)
	}
	log.Printf("созданы два compound-индекса на orders: idx_esr_correct (status,created_at,total — E,S,R) и idx_esr_wrong (status,total,created_at — E,R,S)")

	filter := bson.D{
		{Key: "status", Value: "paid"},
		{Key: "total", Value: bson.D{{Key: "$gt", Value: 500.0}}},
	}
	sortSpec := bson.D{{Key: "created_at", Value: 1}}

	type planResult struct {
		hasSort, hasCollscan  bool
		nReturned, keys, docs int64
		indexName             string
	}
	run := func(hintName string) planResult {
		explainTarget := bson.D{
			{Key: "find", Value: "orders"},
			{Key: "filter", Value: filter},
			{Key: "sort", Value: sortSpec},
			{Key: "hint", Value: hintName},
		}
		out := runExplain(ctx, db, explainTarget, "executionStats")
		qp := topD(out, "queryPlanner")
		winningPlan := bsonDGet(qp, "winningPlan")
		es := topD(out, "executionStats")
		ixscan, _ := findStage(winningPlan, "IXSCAN")
		idxName, _ := bsonDGet(ixscan, "indexName").(string)
		return planResult{
			hasSort:     hasStage(winningPlan, "SORT"),
			hasCollscan: hasStage(winningPlan, "COLLSCAN"),
			nReturned:   toInt64(bsonDGet(es, "nReturned")),
			keys:        toInt64(bsonDGet(es, "totalKeysExamined")),
			docs:        toInt64(bsonDGet(es, "totalDocsExamined")),
			indexName:   idxName,
		}
	}

	correct := run("idx_esr_correct")
	wrong := run("idx_esr_wrong")

	fmt.Printf("FIXTURE indexes: esr_variant=correct index=%s order=E,S,R(status,created_at,total) sort_stage=%v collscan=%v n_returned=%d keys_examined=%d docs_examined=%d\n",
		correct.indexName, correct.hasSort, correct.hasCollscan, correct.nReturned, correct.keys, correct.docs)
	fmt.Printf("FIXTURE indexes: esr_variant=wrong index=%s order=E,R,S(status,total,created_at) sort_stage=%v collscan=%v n_returned=%d keys_examined=%d docs_examined=%d\n",
		wrong.indexName, wrong.hasSort, wrong.hasCollscan, wrong.nReturned, wrong.keys, wrong.docs)

	if correct.nReturned != wrong.nReturned {
		log.Fatalf("assert: оба индекса ищут ОДИН и тот же запрос (status=paid, total>500), nReturned должен совпасть — correct=%d wrong=%d", correct.nReturned, wrong.nReturned)
	}
	if correct.hasCollscan || wrong.hasCollscan {
		log.Fatalf("assert: forced-hint explain НЕ должен давать COLLSCAN ни на одном из вариантов (correct_collscan=%v wrong_collscan=%v)", correct.hasCollscan, wrong.hasCollscan)
	}
	if correct.hasSort {
		log.Fatalf("assert: ESR-верный индекс (E,S,R) НЕ должен требовать blocking SORT-стадию, но она присутствует")
	}
	if !wrong.hasSort {
		log.Fatalf("assert: ESR-неверный индекс (E,R,S — range перед sort-полем) ДОЛЖЕН требовать blocking SORT-стадию, но она отсутствует")
	}
	log.Printf("assert OK: ESR-контраст на ОДНОМ запросе (status=paid + total>500, sort created_at) — idx_esr_correct (E,S,R) БЕЗ SORT, idx_esr_wrong (E,R,S) С SORT; nReturned=%d одинаков для обоих", correct.nReturned)
}

// -- Сценарий 3: partial index -----------------------------------------------

// partialScenario — partial-индекс на orders.total с
// partialFilterExpression {total: {$gt: 1000}}. Запрос-подмножество
// (total>1500) реально его использует (форсированный hint на совместимый
// запрос обязан пройти). Запрос вне партиции (total>500 — пересекается с
// {total>1000}, но НЕ является его подмножеством: есть значения 500<total
// <=1000, которых в индексе просто нет) без hint НЕ должен быть выбран
// планировщиком — это и есть наблюдаемый эффект partial-индекса. Отдельно,
// информационно (без Fatal) — что происходит при форсированном hint на этот
// же несовместимый запрос: реальное поведение сервера, не предположение.
func partialScenario(ctx context.Context, db *mongo.Database, ordersColl *mongo.Collection) {
	partialFilter := bson.D{{Key: "total", Value: bson.D{{Key: "$gt", Value: 1000.0}}}}
	if _, err := ordersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "total", Value: 1}},
		Options: options.Index().SetName("idx_total_partial").SetPartialFilterExpression(partialFilter),
	}); err != nil {
		log.Fatalf("создать idx_total_partial (partialFilterExpression total>1000): %v", err)
	}
	log.Printf("создан partial-индекс idx_total_partial на orders.total (partialFilterExpression: total>1000)")

	// Запрос-подмножество partial-фильтра: forced hint обязан пройти.
	filterCompatible := bson.D{{Key: "total", Value: bson.D{{Key: "$gt", Value: 1500.0}}}}
	explainCompatible := bson.D{
		{Key: "find", Value: "orders"},
		{Key: "filter", Value: filterCompatible},
		{Key: "hint", Value: "idx_total_partial"},
	}
	outC := runExplain(ctx, db, explainCompatible, "executionStats")
	qpC := topD(outC, "queryPlanner")
	winningC := bsonDGet(qpC, "winningPlan")
	esC := topD(outC, "executionStats")
	ixC, foundC := findStage(winningC, "IXSCAN")
	indexNameC, _ := bsonDGet(ixC, "indexName").(string)
	nReturnedC := toInt64(bsonDGet(esC, "nReturned"))
	keysC := toInt64(bsonDGet(esC, "totalKeysExamined"))
	docsC := toInt64(bsonDGet(esC, "totalDocsExamined"))

	fmt.Printf("FIXTURE indexes: partial_query=compatible(total>1500) hinted_ixscan_found=%v index=%s n_returned=%d keys_examined=%d docs_examined=%d\n",
		foundC, indexNameC, nReturnedC, keysC, docsC)
	if !foundC || indexNameC != "idx_total_partial" {
		log.Fatalf("assert: запрос total>1500 (подмножество partialFilter total>1000) обязан использовать idx_total_partial через hint, got found=%v index=%q", foundC, indexNameC)
	}
	log.Printf("assert OK: partial-индекс используется для запроса-подмножества (total>1500 ⊆ partialFilter total>1000): keysExamined=%d docsExamined=%d nReturned=%d", keysC, docsC, nReturnedC)

	// Запрос ВНЕ partial-фильтра, БЕЗ hint — планировщик сам не должен
	// выбрать partial-индекс (реальный контракт оптимизатора).
	filterIncompatible := bson.D{{Key: "total", Value: bson.D{{Key: "$gt", Value: 500.0}}}}
	explainIncompatibleNatural := bson.D{
		{Key: "find", Value: "orders"},
		{Key: "filter", Value: filterIncompatible},
	}
	outN := runExplain(ctx, db, explainIncompatibleNatural, "executionStats")
	qpN := topD(outN, "queryPlanner")
	winningN := bsonDGet(qpN, "winningPlan")
	esN := topD(outN, "executionStats")
	ixN, foundN := findStage(winningN, "IXSCAN")
	indexNameN := ""
	if foundN {
		indexNameN, _ = bsonDGet(ixN, "indexName").(string)
	}
	collscanN := hasStage(winningN, "COLLSCAN")
	nReturnedN := toInt64(bsonDGet(esN, "nReturned"))

	fmt.Printf("FIXTURE indexes: partial_query=incompatible(total>500) natural_ixscan_found=%v natural_index=%s natural_collscan=%v natural_n_returned=%d\n",
		foundN, indexNameN, collscanN, nReturnedN)
	if indexNameN == "idx_total_partial" {
		log.Fatalf("assert: partial-индекс НЕ должен естественно выбираться планировщиком для запроса total>500 (пересекается, но не подмножество partialFilter total>1000), но выбран %s", indexNameN)
	}
	log.Printf("assert OK: планировщик НЕ выбрал partial-индекс для несовместимого запроса (естественный план: index=%q collscan=%v)", indexNameN, collscanN)

	// Информационно (НЕ Fatal): что происходит при форсированном hint на
	// partial-индекс для того же несовместимого запроса — реальное
	// поведение сервера записывается как факт, а не предполагается заранее.
	explainIncompatibleHinted := bson.D{
		{Key: "find", Value: "orders"},
		{Key: "filter", Value: filterIncompatible},
		{Key: "hint", Value: "idx_total_partial"},
	}
	outH, errHinted := runExplainE(ctx, db, explainIncompatibleHinted, "executionStats")
	fmt.Printf("FIXTURE indexes: partial_query=incompatible(total>500) forced_hint_error=%q\n", errStrOrNone(errHinted))
	if errHinted != nil {
		log.Printf("наблюдение (не ассерт): форсированный hint на partial-индекс для несовместимого запроса вернул ошибку сервера: %v", errHinted)
		return
	}
	// Сервер не вернул ошибку — важно зафиксировать, ЧТО именно он реально
	// сделал: тихо проигнорировал hint (и выбрал другой план) или всё же
	// использовал partial-индекс за пределами его partialFilterExpression
	// (что было бы нарушением документированного контракта partial-индексов
	// и заслуживало бы отдельного разбора). Разбираем winningPlan так же,
	// как и в остальных сценариях — без предположений.
	qpH := topD(outH, "queryPlanner")
	winningH := bsonDGet(qpH, "winningPlan")
	esH := topD(outH, "executionStats")
	ixH, foundH := findStage(winningH, "IXSCAN")
	indexNameH := ""
	if foundH {
		indexNameH, _ = bsonDGet(ixH, "indexName").(string)
	}
	collscanH := hasStage(winningH, "COLLSCAN")
	nReturnedH := toInt64(bsonDGet(esH, "nReturned"))
	docsH := toInt64(bsonDGet(esH, "totalDocsExamined"))
	fmt.Printf("FIXTURE indexes: partial_query=incompatible(total>500) forced_hint_plan_index=%s forced_hint_collscan=%v forced_hint_n_returned=%d forced_hint_docs_examined=%d natural_n_returned_for_comparison=%d\n",
		indexNameH, collscanH, nReturnedH, docsH, nReturnedN)
	if indexNameH == "idx_total_partial" {
		if nReturnedH < nReturnedN {
			log.Printf("ВАЖНОЕ наблюдение (не ассерт этого стенда, но факт, заслуживающий отдельного разбора в статье): форсированный hint на partial-индекс для НЕсовместимого запроса (total>500) explain'ится БЕЗ ошибки, winningPlan реально ссылается на idx_total_partial, И возвращает МЕНЬШЕ документов, чем корректный полный скан (%d < %d) — т.е. сервер ТИХО возвращает НЕПОЛНЫЙ/некорректный результат вместо ошибки. Документы с 500<total<=1000 физически отсутствуют в partial-индексе и потеряны при forced hint.", nReturnedH, nReturnedN)
		} else {
			log.Printf("наблюдение: форсированный hint на partial-индекс для несовместимого запроса explain'ится БЕЗ ошибки, winningPlan ссылается на idx_total_partial, но nReturned(%d) НЕ меньше natural(%d) — вопреки ожиданию потери документов; см. отчёт", nReturnedH, nReturnedN)
		}
	} else {
		log.Printf("наблюдение (не ассерт): форсированный hint на partial-индекс для несовместимого запроса explain'ится без ошибки, но сервер ТИХО ИГНОРИРУЕТ hint и реально использует другой план (index=%q collscan=%v) — hint не заставил использовать partial-индекс вне его partialFilterExpression", indexNameH, collscanH)
	}
}

// -- Сценарий 4: TTL index ----------------------------------------------------

// ttlScenario — отдельная коллекция ttl_demo, индекс с
// expireAfterSeconds=0 на поле expires_at (Date) — "expire at the exact
// clock time in this field". Документ с expires_at в прошлом должен быть
// удалён фоновым TTL monitor'ом (по умолчанию проходит раз в ~60s);
// документ с expires_at в будущем должен остаться. Стенд реально ждёт
// (poll до ttlMaxWait) и фиксирует фактическое время — TTL monitor не
// вызывается напрямую пользователем, только естественное ожидание.
const (
	ttlPollInterval = 10 * time.Second
	ttlMaxAttempts  = 12 // 12 * 10s = 120s верхняя граница ожидания
)

func ttlScenario(ctx context.Context, db *mongo.Database) {
	ttlColl := db.Collection("ttl_demo")
	if err := ttlColl.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("drop ttl_demo: %v", err)
	}

	if _, err := ttlColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetName("idx_ttl_expires_at").SetExpireAfterSeconds(0),
	}); err != nil {
		log.Fatalf("создать TTL-индекс ttl_demo.expires_at: %v", err)
	}
	log.Printf("создан TTL-индекс idx_ttl_expires_at на ttl_demo.expires_at (expireAfterSeconds=0 — expire at the clock time in the field)")

	now := time.Now().UTC()
	pastDoc := bson.D{{Key: "_id", Value: "ttl-past"}, {Key: "expires_at", Value: now.Add(-1 * time.Hour)}, {Key: "note", Value: "должен истечь"}}
	futureDoc := bson.D{{Key: "_id", Value: "ttl-future"}, {Key: "expires_at", Value: now.Add(1 * time.Hour)}, {Key: "note", Value: "должен остаться"}}
	if _, err := ttlColl.InsertMany(ctx, []any{pastDoc, futureDoc}); err != nil {
		log.Fatalf("insert ttl_demo (past+future): %v", err)
	}
	log.Printf("вставлены 2 документа: ttl-past (expires_at=%s, уже в прошлом) и ttl-future (expires_at=%s, в будущем)", now.Add(-1*time.Hour).Format(time.RFC3339), now.Add(1*time.Hour).Format(time.RFC3339))

	t0 := time.Now()
	var pastGone bool
	var elapsed time.Duration
	for attempt := 0; attempt < ttlMaxAttempts; attempt++ {
		time.Sleep(ttlPollInterval)
		elapsed = time.Since(t0)
		count, err := ttlColl.CountDocuments(ctx, bson.D{{Key: "_id", Value: "ttl-past"}})
		if err != nil {
			log.Fatalf("countDocuments ttl-past (poll %d): %v", attempt, err)
		}
		log.Printf("TTL poll #%d (%v с момента вставки): ttl-past присутствует=%v", attempt+1, elapsed.Round(time.Second), count == 1)
		if count == 0 {
			pastGone = true
			break
		}
	}
	futureCount, err := ttlColl.CountDocuments(ctx, bson.D{{Key: "_id", Value: "ttl-future"}})
	if err != nil {
		log.Fatalf("countDocuments ttl-future: %v", err)
	}

	fmt.Printf("FIXTURE indexes: ttl_index=idx_ttl_expires_at ttl_past_doc_expired=%v ttl_wait=%v ttl_future_doc_present=%v max_wait=%v\n",
		pastGone, elapsed.Round(time.Second), futureCount == 1, time.Duration(ttlMaxAttempts)*ttlPollInterval)

	if pastGone {
		log.Printf("assert OK: TTL-индекс удалил документ с expires_at в прошлом за %v (фоновый TTL monitor)", elapsed.Round(time.Second))
	} else {
		log.Printf("наблюдение (не Fatal — расхождение с ожиданием брифа): документ с expires_at в прошлом НЕ был удалён TTL monitor'ом за %v ожидания; TTL monitor по умолчанию проходит раз в ~60s, но точный момент прохода после старта mongod не гарантирован в пределах этого окна — см. отчёт", elapsed.Round(time.Second))
	}
	if futureCount != 1 {
		log.Fatalf("assert: документ с expires_at в будущем НЕ должен быть удалён за то же окно ожидания, но futureCount=%d", futureCount)
	}
	log.Printf("assert OK: документ с expires_at в будущем остался нетронутым за %v ожидания", elapsed.Round(time.Second))
}

// -- Сценарий 5: covered query -------------------------------------------------

// coveredScenario — индекс {category:1, price:1} на products; запрос с
// проекцией {category:1, price:1, _id:0} (подмножество ключей индекса, _id
// явно исключён) должен полностью обслуживаться из индекса без обращения к
// самим документам: PROJECTION_COVERED в winningPlan, totalDocsExamined=0.
func coveredScenario(ctx context.Context, db *mongo.Database, productsColl *mongo.Collection) {
	if _, err := productsColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "category", Value: 1}, {Key: "price", Value: 1}},
		Options: options.Index().SetName("idx_category_price_covering"),
	}); err != nil {
		log.Fatalf("создать idx_category_price_covering: %v", err)
	}
	log.Printf("создан индекс idx_category_price_covering на products {category:1, price:1}")

	explainTarget := bson.D{
		{Key: "find", Value: "products"},
		{Key: "filter", Value: bson.D{{Key: "category", Value: "electronics"}}},
		{Key: "projection", Value: bson.D{{Key: "category", Value: 1}, {Key: "price", Value: 1}, {Key: "_id", Value: 0}}},
	}
	out := runExplain(ctx, db, explainTarget, "executionStats")
	qp := topD(out, "queryPlanner")
	winningPlan := bsonDGet(qp, "winningPlan")
	es := topD(out, "executionStats")

	docsExamined := toInt64(bsonDGet(es, "totalDocsExamined"))
	keysExamined := toInt64(bsonDGet(es, "totalKeysExamined"))
	nReturned := toInt64(bsonDGet(es, "nReturned"))
	_, hasCovered := findStage(winningPlan, "PROJECTION_COVERED")
	hasFetch := hasStage(winningPlan, "FETCH")

	fmt.Printf("FIXTURE indexes: covered_query_filter=category:electronics projection_covered=%v fetch_present=%v total_docs_examined=%d total_keys_examined=%d n_returned=%d\n",
		hasCovered, hasFetch, docsExamined, keysExamined, nReturned)

	if docsExamined != 0 {
		log.Fatalf("assert: covered-запрос должен иметь totalDocsExamined=0, got %d", docsExamined)
	}
	if !hasCovered {
		log.Fatalf("assert: covered-запрос должен использовать стадию PROJECTION_COVERED в winningPlan")
	}
	if hasFetch {
		log.Fatalf("assert: covered-запрос НЕ должен иметь FETCH-стадию (документы вообще не читаются)")
	}
	log.Printf("assert OK: covered query (products, category=electronics, проекция {category,price,_id:0}) — totalDocsExamined=0, PROJECTION_COVERED присутствует, FETCH отсутствует; keysExamined=%d nReturned=%d",
		keysExamined, nReturned)
}
