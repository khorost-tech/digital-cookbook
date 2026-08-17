// Command sharding — стенд #6 серии "MongoDB: глубокое погружение":
// шардирование ВЖИВУЮ на РЕАЛЬНОМ шардированном кластере (mongos + config RS
// csrs + два шарда-RS shard1/shard2), поверх уже импортированного датасета
// (Task 8, см. ../ops/sharding-demo.sh для оркестрации топологии).
//
// Стенд подключается к РОУТЕРУ mongos (не к отдельному шарду) и меряет
// РЕАЛЬНЫЕ факты кластера — все числа ПРОИЗВЕДЕНЫ, ни одно не выдумано:
//
//  1. Распределение чанков hashed vs ranged (config.chunks по шардам).
//     Две коллекции с ОДНИМ и тем же датасетом orders (200k):
//     orders_hashed — shard key {_id: "hashed"};
//     orders_ranged — shard key {_id: 1} (ranged на МОНОТОННОМ ObjectID —
//     классический источник write-хотспота).
//     Импорт делается при ВЫКЛЮЧЕННОМ балансировщике (см. demo-скрипт:
//     sh.stopBalancer перед mongoimport), поэтому "pre-balance" срез
//     показывает распределение РОВНО так, как его сделал роутер по shard
//     key на вставке: hashed — по обоим шардам, ranged — весь в один
//     начальный чанк [minKey,maxKey) на primary-шарде (хотспот).
//     Ассерт: hashed охватывает ОБА шарда; ranged сконцентрирован сильнее
//     hashed (max-доля чанков одного шарда у ranged строго больше).
//
//  2. Балансировщик. Стенд ВКЛЮЧАЕТ балансировщик (balancerStart) и реально
//     ждёт (poll balancerStatus + распределение чанков), пока раунд не
//     сойдётся; "post-balance" срез фиксирует, куда сдвинулось
//     распределение (движение чанков к равномерности). Наблюдение (не
//     форсированный зелёный ассерт) — реальные числа "до/после".
//
//  3. Резолюция запроса (targeting) через mongos explain:
//     запрос по shard key (_id равно конкретному значению) — winningPlan
//     должен быть SINGLE_SHARD (роутер бьёт РОВНО один шард);
//     запрос без shard key (по status) — SHARD_MERGE (scatter-gather по
//     обоим шардам).
//     Ассерт: targeted-запрос → ровно 1 шард; scatter → >1 шард.
//
//  4. Resharding (8.x): попытка сменить shard key orders_ranged вживую
//     (reshardCollection на {user_id:"hashed"}). Операция тяжёлая; стенд
//     даёт ей ограниченное окно и фиксирует РЕАЛЬНЫЙ исход — успех (с
//     проверкой, что shard key реально сменился в config.collections) ЛИБО
//     честно "не воспроизведено в окне стенда" (без Fatal, см. FIXTURES §6).
//
// Дисциплина ассертов — как во всей серии: где брифом заявлен жёсткий
// контракт (hashed охватывает оба шарда, ranged концентрированнее, targeting
// single vs scatter) — fail-loud (log.Fatalf); где реальное поведение
// многовариантно (итог балансировки, исход resharding) — печатаем факт как
// есть, без искусственного зелёного.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const (
	dbName        = "cookbook"
	nsHashed      = "cookbook.orders_hashed"
	nsRanged      = "cookbook.orders_ranged"
	collHashed    = "orders_hashed"
	collRanged    = "orders_ranged"
	reshardTarget = "user_id" // новый shard key для orders_ranged при resharding
)

// manifest — форма ../dataset/manifest.json (единственный источник правды по
// seed/counts датасета cookbook, см. остальные стенды серии).
type manifest struct {
	Seed     int64 `json:"seed"`
	Users    int   `json:"users"`
	Products int   `json:"products"`
	Orders   int   `json:"orders"`
}

func loadManifest() manifest {
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

	mongoURI := mongoconn.MustEnv("MONGO_URI") // URI роутера mongos
	// Роутер mongos и его DNS-имя в свежесозданной сети docker могут быть
	// готовы не мгновенно (регистрация имени в embedded DNS + прогрев
	// роутинговой таблицы после shardCollection): ретраим Connect несколько
	// раз, а не падаем на первой же ошибке server selection/DNS.
	client := connectWithRetry(mongoURI, 12, 5*time.Second)
	defer func() { _ = client.Disconnect(context.Background()) }()

	db := client.Database(dbName)
	admin := client.Database("admin")
	config := client.Database("config")

	// 0. Санити: подключены к mongos и в кластере два шарда. На свежесобранном
	//    кластере роутер mongos может отдавать метаданные (реестр шардов,
	//    роутинговую таблицу) не мгновенно — прогрев кэша после addShard/
	//    shardCollection. Поэтому НЕ падаем на первом чтении, а ждём, пока
	//    mongos стабильно покажет ровно 2 шарда.
	shards := waitShardsReady(ctx, config, 2, 30, 3*time.Second)
	fmt.Printf("FIXTURE sharding: shards=%v shard_count=%d\n", shards, len(shards))
	if len(shards) != 2 {
		log.Fatalf("assert: ожидалось РОВНО 2 шарда в кластере, получено %d (%v)", len(shards), shards)
	}

	// 1. Импорт совпал с manifest (обе коллекции — тот же датасет orders).
	assertImportCounts(ctx, m, db)

	// 2. Балансировщик ДОЛЖЕН быть выключен на момент старта (его выключает
	//    demo-скрипт перед mongoimport) — иначе "pre-balance" срез не
	//    отражает чистую раскладку роутера по shard key на вставке.
	balOn := balancerRunning(ctx, admin)
	fmt.Printf("FIXTURE sharding: balancer_enabled_at_start=%v\n", balOn)
	if balOn {
		log.Printf("предупреждение: балансировщик включён на старте — pre-balance срез может быть уже частично сбалансирован; принудительно останавливаю")
		stopBalancer(ctx, admin)
		waitBalancerStopped(ctx, admin)
	}

	// 3. PRE-BALANCE распределение чанков.
	hashedPre := chunkDistribution(ctx, config, nsHashed)
	rangedPre := chunkDistribution(ctx, config, nsRanged)
	printDist("pre", "hashed", hashedPre)
	printDist("pre", "ranged", rangedPre)
	assertPreBalance(hashedPre, rangedPre)

	// 4. Targeting (не зависит от баланса — роутинг по shard key).
	targetingScenario(ctx, db, config)

	// 5. Балансировщик: включаем, ждём схождения, меряем post-balance.
	balanceScenario(ctx, admin, config, hashedPre, rangedPre)

	// 6. Resharding (честный исход).
	reshardScenario(ctx, admin, config)

	log.Println("готово.")
}

// connectWithRetry повторяет mongoconn.Connect до attempts раз с паузой delay
// между попытками (транзиентные DNS/server-selection ошибки на свежей сети).
func connectWithRetry(uri string, attempts int, delay time.Duration) *mongo.Client {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		client, err := mongoconn.Connect(uri)
		if err == nil {
			if i > 1 {
				log.Printf("mongo connect (mongos): успех с попытки #%d", i)
			}
			return client
		}
		lastErr = err
		log.Printf("mongo connect (mongos) попытка #%d/%d: %v — повтор через %v", i, attempts, err, delay)
		time.Sleep(delay)
	}
	log.Fatalf("mongo connect (mongos): не удалось подключиться за %d попыток: %v", attempts, lastErr)
	return nil
}

// -- config-кластер: шарды, коллекции, чанки --------------------------------

// shardChunks — распределение чанков по шардам одной коллекции.
type shardChunks struct {
	perShard map[string]int64
	total    int64
}

func (d shardChunks) shardsCovered() int {
	n := 0
	for _, c := range d.perShard {
		if c > 0 {
			n++
		}
	}
	return n
}

// maxShare — максимальная доля чанков, приходящаяся на один шард (0..1).
func (d shardChunks) maxShare() float64 {
	if d.total == 0 {
		return 0
	}
	var mx int64
	for _, c := range d.perShard {
		if c > mx {
			mx = c
		}
	}
	return float64(mx) / float64(d.total)
}

// orderedKeys — имена шардов в стабильном порядке (для детерминированной печати).
func (d shardChunks) orderedKeys() []string {
	ks := make([]string, 0, len(d.perShard))
	for k := range d.perShard {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// waitShardsReady опрашивает config.shards через mongos, пока не увидит want
// шардов (прогрев метаданных роутера на свежем кластере), до attempts попыток
// с паузой delay. Возвращает последний прочитанный список.
func waitShardsReady(ctx context.Context, config *mongo.Database, want, attempts int, delay time.Duration) []string {
	var last []string
	for i := 1; i <= attempts; i++ {
		last = listShards(ctx, config)
		if len(last) == want {
			if i > 1 {
				log.Printf("cluster ready: mongos показал %d шардов с попытки #%d", want, i)
			}
			return last
		}
		log.Printf("ожидание готовности кластера #%d/%d: mongos видит %d шардов (%v), нужно %d — повтор через %v",
			i, attempts, len(last), last, want, delay)
		time.Sleep(delay)
	}
	return last
}

func listShards(ctx context.Context, config *mongo.Database) []string {
	cur, err := config.Collection("shards").Find(ctx, bson.D{})
	if err != nil {
		log.Fatalf("config.shards.find: %v", err)
	}
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		log.Fatalf("config.shards decode: %v", err)
	}
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		if id, ok := d["_id"].(string); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// collectionUUID достаёт uuid коллекции из config.collections по namespace —
// в config.chunks (MongoDB 5.0+) чанки ссылаются на коллекцию через uuid,
// а не через строковый ns.
func collectionUUID(ctx context.Context, config *mongo.Database, ns string) any {
	var doc bson.M
	err := config.Collection("collections").FindOne(ctx, bson.D{{Key: "_id", Value: ns}}).Decode(&doc)
	if err != nil {
		log.Fatalf("config.collections.findOne(%s): %v", ns, err)
	}
	uuid, ok := doc["uuid"]
	if !ok {
		log.Fatalf("config.collections(%s): нет поля uuid (%v)", ns, doc)
	}
	return uuid
}

// currentShardKey читает текущий shard key коллекции из config.collections
// (поле key) — используется для проверки resharding.
func currentShardKey(ctx context.Context, config *mongo.Database, ns string) bson.D {
	var doc bson.M
	err := config.Collection("collections").FindOne(ctx, bson.D{{Key: "_id", Value: ns}}).Decode(&doc)
	if err != nil {
		log.Fatalf("config.collections.findOne(%s) для shard key: %v", ns, err)
	}
	if key, ok := doc["key"].(bson.D); ok {
		return key
	}
	if key, ok := doc["key"].(bson.M); ok {
		out := bson.D{}
		for k, v := range key {
			out = append(out, bson.E{Key: k, Value: v})
		}
		return out
	}
	return nil
}

// chunkDistribution группирует config.chunks по shard для одной коллекции.
func chunkDistribution(ctx context.Context, config *mongo.Database, ns string) shardChunks {
	uuid := collectionUUID(ctx, config, ns)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "uuid", Value: uuid}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$shard"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	cur, err := config.Collection("chunks").Aggregate(ctx, pipeline)
	if err != nil {
		log.Fatalf("config.chunks.aggregate(%s): %v", ns, err)
	}
	var rows []struct {
		Shard string `bson:"_id"`
		Count int64  `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		log.Fatalf("config.chunks decode(%s): %v", ns, err)
	}
	d := shardChunks{perShard: map[string]int64{}}
	for _, r := range rows {
		d.perShard[r.Shard] = r.Count
		d.total += r.Count
	}
	return d
}

func printDist(phase, kind string, d shardChunks) {
	parts := ""
	for _, k := range d.orderedKeys() {
		parts += fmt.Sprintf(" %s=%d", k, d.perShard[k])
	}
	fmt.Printf("FIXTURE sharding: chunks_%s_%s total=%d shards_covered=%d max_share=%.3f perShard{%s }\n",
		phase, kind, d.total, d.shardsCovered(), d.maxShare(), parts)
}

// -- Ассерты и сценарии ------------------------------------------------------

func assertImportCounts(ctx context.Context, m manifest, db *mongo.Database) {
	hc, err := db.Collection(collHashed).CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments %s: %v", collHashed, err)
	}
	rc, err := db.Collection(collRanged).CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments %s: %v", collRanged, err)
	}
	fmt.Printf("FIXTURE sharding: import_orders_hashed=%d import_orders_ranged=%d expected=%d\n", hc, rc, m.Orders)
	if int(hc) != m.Orders || int(rc) != m.Orders {
		log.Fatalf("assert: обе коллекции должны содержать %d заказов (hashed=%d ranged=%d)", m.Orders, hc, rc)
	}
	log.Printf("assert OK: обе шардированные коллекции импортированы полностью (%d документов каждая)", m.Orders)
}

// assertPreBalance — жёсткий контракт брифа: hashed охватывает ОБА шарда;
// ranged (монотонный _id, балансировщик выключен) сконцентрирован сильнее.
func assertPreBalance(hashed, ranged shardChunks) {
	if hashed.shardsCovered() < 2 {
		log.Fatalf("assert: hashed shard key должен распределять чанки по ОБОИМ шардам на вставке, покрыто шардов=%d (%v)",
			hashed.shardsCovered(), hashed.perShard)
	}
	if ranged.maxShare() <= hashed.maxShare() {
		log.Fatalf("assert: ranged shard key на монотонном _id должен быть КОНЦЕНТРИРОВАННЕЕ hashed (хотспот): ranged.maxShare=%.3f не больше hashed.maxShare=%.3f",
			ranged.maxShare(), hashed.maxShare())
	}
	log.Printf("assert OK: pre-balance — hashed охватывает оба шарда (maxShare=%.3f), ranged сконцентрирован (maxShare=%.3f, хотспот на одном шарде)",
		hashed.maxShare(), ranged.maxShare())
}

// -- targeting: SINGLE_SHARD vs SHARD_MERGE через mongos explain -------------

func targetingScenario(ctx context.Context, db, config *mongo.Database) {
	// Берём реальный существующий _id из orders_hashed.
	var one bson.M
	if err := db.Collection(collHashed).FindOne(ctx, bson.D{}).Decode(&one); err != nil {
		log.Fatalf("targeting: не удалось взять образец _id из %s: %v", collHashed, err)
	}
	sampleID := one["_id"]

	// (a) запрос ПО shard key -> ожидаем SINGLE_SHARD.
	targetedStage, targetedShards := explainTopStage(ctx, db, bson.D{
		{Key: "find", Value: collHashed},
		{Key: "filter", Value: bson.D{{Key: "_id", Value: sampleID}}},
	})
	fmt.Printf("FIXTURE sharding: targeting_with_shard_key stage=%s shards_targeted=%d\n", targetedStage, targetedShards)

	// (b) запрос БЕЗ shard key (по status) -> ожидаем SHARD_MERGE (scatter).
	scatterStage, scatterShards := explainTopStage(ctx, db, bson.D{
		{Key: "find", Value: collHashed},
		{Key: "filter", Value: bson.D{{Key: "status", Value: "paid"}}},
	})
	fmt.Printf("FIXTURE sharding: targeting_without_shard_key stage=%s shards_targeted=%d\n", scatterStage, scatterShards)

	if targetedShards != 1 {
		log.Fatalf("assert: запрос по shard key (_id) должен бить РОВНО 1 шард, targeted=%d (stage=%s)", targetedShards, targetedStage)
	}
	if scatterShards <= 1 {
		log.Fatalf("assert: запрос без shard key (status) должен быть scatter-gather по >1 шарду, targeted=%d (stage=%s)", scatterShards, scatterStage)
	}
	log.Printf("assert OK: targeting — по shard key stage=%s (%d шард), без shard key stage=%s (%d шарда, scatter-gather)",
		targetedStage, targetedShards, scatterStage, scatterShards)
}

// explainTopStage выполняет mongos explain (queryPlanner) и возвращает
// верхнюю стадию winningPlan (SINGLE_SHARD/SHARD_MERGE) и число целевых шардов
// (длину winningPlan.shards). ВАЖНО: драйвер декодирует ВЛОЖЕННЫЕ BSON-
// документы в bson.M-ответе как bson.D (не bson.M) — тот же приём разбора
// дерева через bsonDGet, что и в indexes/main.go.
func explainTopStage(ctx context.Context, db *mongo.Database, explainTarget bson.D) (string, int) {
	cmd := bson.D{
		{Key: "explain", Value: explainTarget},
		{Key: "verbosity", Value: "queryPlanner"},
	}
	var out bson.M
	if err := db.RunCommand(ctx, cmd).Decode(&out); err != nil {
		log.Fatalf("explain %v: %v", explainTarget, err)
	}
	qp, _ := out["queryPlanner"].(bson.D)
	wp, _ := bsonDGet(qp, "winningPlan").(bson.D)
	stage, _ := bsonDGet(wp, "stage").(string)
	shardsTargeted := 0
	if arr, ok := bsonDGet(wp, "shards").(bson.A); ok {
		shardsTargeted = len(arr)
	}
	return stage, shardsTargeted
}

// bsonDGet — линейный поиск значения по ключу в bson.D (планы explain читаются
// считанные разы за прогон).
func bsonDGet(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// -- балансировщик -----------------------------------------------------------

func balancerRunning(ctx context.Context, admin *mongo.Database) bool {
	var out bson.M
	if err := admin.RunCommand(ctx, bson.D{{Key: "balancerStatus", Value: 1}}).Decode(&out); err != nil {
		log.Fatalf("balancerStatus: %v", err)
	}
	// mode="full"/"off"; inBalancerRound — идёт ли раунд прямо сейчас.
	mode, _ := out["mode"].(string)
	return mode == "full"
}

func stopBalancer(ctx context.Context, admin *mongo.Database) {
	if err := admin.RunCommand(ctx, bson.D{{Key: "balancerStop", Value: 1}, {Key: "maxTimeMS", Value: 60000}}).Err(); err != nil {
		log.Fatalf("balancerStop: %v", err)
	}
}

func startBalancer(ctx context.Context, admin *mongo.Database) {
	if err := admin.RunCommand(ctx, bson.D{{Key: "balancerStart", Value: 1}, {Key: "maxTimeMS", Value: 60000}}).Err(); err != nil {
		log.Fatalf("balancerStart: %v", err)
	}
}

func waitBalancerStopped(ctx context.Context, admin *mongo.Database) {
	for i := 0; i < 30; i++ {
		var out bson.M
		if err := admin.RunCommand(ctx, bson.D{{Key: "balancerStatus", Value: 1}}).Decode(&out); err == nil {
			if inRound, _ := out["inBalancerRound"].(bool); !inRound {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// balanceScenario включает балансировщик и ждёт схождения: распределение
// чанков перестаёт меняться несколько замеров подряд ИЛИ истекает бюджет
// времени. Фиксирует post-balance срез (движение чанков к равномерности).
func balanceScenario(ctx context.Context, admin, config *mongo.Database, hashedPre, rangedPre shardChunks) {
	log.Printf("включаю балансировщик (balancerStart), жду схождения раунда...")
	startBalancer(ctx, admin)

	const (
		poll       = 10 * time.Second
		maxRounds  = 18 // до ~3 минут суммарно — бюджет окна стенда
		stableNeed = 3  // столько одинаковых подряд замеров считаем схождением
	)
	prevSig := ""
	stable := 0
	var elapsed time.Duration
	t0 := time.Now()
	for i := 0; i < maxRounds; i++ {
		time.Sleep(poll)
		elapsed = time.Since(t0)
		h := chunkDistribution(ctx, config, nsHashed)
		r := chunkDistribution(ctx, config, nsRanged)
		sig := fmt.Sprintf("%v|%v", h.perShard, r.perShard)
		inRound := false
		var st bson.M
		if err := admin.RunCommand(ctx, bson.D{{Key: "balancerStatus", Value: 1}}).Decode(&st); err == nil {
			inRound, _ = st["inBalancerRound"].(bool)
		}
		log.Printf("balance poll #%d (%v): hashed=%v ranged=%v inBalancerRound=%v", i+1, elapsed.Round(time.Second), h.perShard, r.perShard, inRound)
		if sig == prevSig && !inRound {
			stable++
			if stable >= stableNeed {
				break
			}
		} else {
			stable = 0
			prevSig = sig
		}
	}

	hashedPost := chunkDistribution(ctx, config, nsHashed)
	rangedPost := chunkDistribution(ctx, config, nsRanged)
	printDist("post", "hashed", hashedPost)
	printDist("post", "ranged", rangedPost)
	fmt.Printf("FIXTURE sharding: balance_settle_time_approx=%v\n", elapsed.Round(time.Second))

	// Движение чанков ranged: во сколько раз выросло покрытие шардов / как
	// упала max-доля (число, а не форсированный зелёный).
	fmt.Printf("FIXTURE sharding: ranged_movement pre{covered=%d max_share=%.3f} post{covered=%d max_share=%.3f}\n",
		rangedPre.shardsCovered(), rangedPre.maxShare(), rangedPost.shardsCovered(), rangedPost.maxShare())
	fmt.Printf("FIXTURE sharding: hashed_movement pre{covered=%d max_share=%.3f} post{covered=%d max_share=%.3f}\n",
		hashedPre.shardsCovered(), hashedPre.maxShare(), hashedPost.shardsCovered(), hashedPost.maxShare())

	// Наблюдение (не форсированный зелёный): сошёлся ли ranged к обоим шардам.
	if rangedPost.shardsCovered() >= 2 {
		log.Printf("наблюдение: балансировщик распределил ranged по обоим шардам (movement covered %d->%d, max_share %.3f->%.3f)",
			rangedPre.shardsCovered(), rangedPost.shardsCovered(), rangedPre.maxShare(), rangedPost.maxShare())
	} else {
		log.Printf("наблюдение: за окно стенда (%v) балансировщик НЕ распределил ranged по обоим шардам (covered=%d, max_share=%.3f) — фиксируем как есть (см. FIXTURES §6)",
			elapsed.Round(time.Second), rangedPost.shardsCovered(), rangedPost.maxShare())
	}
}

// -- resharding (8.x): честный исход -----------------------------------------

func reshardScenario(ctx context.Context, admin, config *mongo.Database) {
	before := currentShardKey(ctx, config, nsRanged)
	fmt.Printf("FIXTURE sharding: reshard_target_ns=%s key_before=%v new_key=user_id(hashed)\n", nsRanged, before)

	// reshardCollection блокирует до завершения координатора. Даём
	// ограниченное окно через контекст; при таймауте/ошибке — честно
	// фиксируем "не воспроизведено", без Fatal. 8 минут — по факту
	// предварительной живой проверки этого стенда (см. FIXTURES §6):
	// на 200k документов orders_ranged (~75МБ) авторитетный успешный прогон
	// занял 5m29s; более ранняя попытка на 5-минутном потолке не успела и
	// вернула MaxTimeMSExpired на ~4m37s. 8 минут даёт запас сверх
	// наблюдённой стоимости (~5.5 минут), не будучи неограниченным
	// ожиданием: клиентский таймаут НЕ отменяет операцию на сервере (она
	// resumable и продолжится независимо от клиента), но стенд обязан
	// вернуть управление в предсказуемое время.
	rctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	cmd := bson.D{
		{Key: "reshardCollection", Value: nsRanged},
		{Key: "key", Value: bson.D{{Key: reshardTarget, Value: "hashed"}}},
	}
	t0 := time.Now()
	err := admin.RunCommand(rctx, cmd).Err()
	elapsed := time.Since(t0)

	if err != nil {
		fmt.Printf("FIXTURE sharding: reshard_result=NOT_REPRODUCED elapsed=%v error=%q\n", elapsed.Round(time.Second), err.Error())
		log.Printf("наблюдение (не Fatal): reshardCollection не завершился в окне стенда за %v — %v; фиксируем как НЕ воспроизведено (см. FIXTURES §6)",
			elapsed.Round(time.Second), err)
		return
	}

	after := currentShardKey(ctx, config, nsRanged)
	changed := shardKeyHasField(after, reshardTarget)
	fmt.Printf("FIXTURE sharding: reshard_result=OK elapsed=%v key_after=%v key_changed=%v\n", elapsed.Round(time.Second), after, changed)
	if !changed {
		log.Printf("наблюдение: reshardCollection вернул успех за %v, но config.collections.key=%v не содержит нового поля %q — фиксируем расхождение",
			elapsed.Round(time.Second), after, reshardTarget)
		return
	}
	log.Printf("resharding ВЖИВУЮ: shard key %s сменён на {%s:hashed} за %v (config.collections подтверждает)", nsRanged, reshardTarget, elapsed.Round(time.Second))
}

func shardKeyHasField(key bson.D, field string) bool {
	for _, e := range key {
		if e.Key == field {
			return true
		}
	}
	return false
}
