// Command replication — стенд #5 серии "MongoDB: глубокое погружение":
// репликация вживую (oplog, write concern, read concern/причинная
// согласованность) — и НАСТОЯЩИЙ failover (docker stop primary, реальные
// перевыборы) на РЕАЛЬНОМ 3-узловом replica set (rs0), поверх уже
// импортированного датасета (Task 3+).
//
// Бинарник работает в ДВЕ ФАЗЫ (см. ../ops/replication-demo.sh — только
// оркестрация решает, КОГДА какая фаза запускается; сам failover — то
// есть `docker stop` контейнера primary и ожидание нового primary —
// делает ИМЕННО demo-скрипт, не этот Go-код, ровно как того требует бриф):
//
//  1. "core" (аргумент по умолчанию, если os.Args[1] не задан) — на ЕЩЁ
//     ЖИВОМ, не тронутом failover'ом кластере:
//       - oplog: пишем документ, читаем породившуюся запись из
//         local.oplog.rs — реальная структура события (op/ns/o/ts/wall).
//       - write concern: w:1 vs w:majority — реальные латентности серии
//         одиночных insertOne (значимо для write concern: ack ждётся на
//         КАЖДУЮ отдельную запись, bulk смазал бы разницу).
//       - read concern / причинная согласованность: причинно-согласованная
//         сессия делает read-your-writes через secondary — read preference
//         secondary. Механизм доказывается НЕ на глазок, а перехватом
//         сырой команды find через *event.CommandMonitor: у сессии с
//         CausalConsistency=true драйвер ОБЯЗАН добавить
//         readConcern.afterClusterTime в команду; без сессии — не должен.
//       - PG-контраст (опционально, если задан PG_DSN): один живой факт из
//         compose/postgres.yml — wal_level образа postgres:18 "из коробки"
//         НЕ logical, то есть логическая репликация PG требует явного
//         конфигурирования, в отличие от oplog MongoDB, который пишется
//         безусловно на любом узле replica set сразу после rs.initiate().
//         Полноценный стенд publication/subscription — ЗА РАМКАМИ этого
//         стенда (граница на возможную отдельную статью про PG-репликацию).
//  2. "failover-write" — вызывается demo-скриптом ПОСЛЕ того, как
//     скрипт сам (docker stop контейнера прежнего primary + polling
//     db.hello().isWritablePrimary на выживших узлах) убедился, что новый
//     primary избран. Здесь только фиксируется факт: запись ПРОХОДИТ на
//     новый primary (единственный ассерт этой фазы) — реальное число
//     времени перевыборов замеряет и печатает сам demo-скрипт (это
//     инфраструктурное измерение — polling внешних контейнеров, не
//     доменная логика клиента).
//
// Внутренности multi-document транзакций (snapshot isolation + write
// concern у транзакции) этот стенд НЕ строит — есть отдельная серия
// «Транзакции и изоляция» (databases/transactions), граница обозначена
// здесь только упоминанием: причинная согласованность и readConcern —
// тот же понятийный аппарат (afterClusterTime/operationTime), что и у
// транзакций, но полноценный txn-стенд — вне периметра этой задачи.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const (
	dbName = "cookbook"

	// replicationDemoColl — коллекция для oplog-сценария: один вставленный
	// документ, одна ожидаемая oplog-запись.
	replicationDemoColl = "replication_demo"

	// wcLoadColl/wcLoadDocs/wcPayloadBytes — write concern нагрузка: серия
	// ОДИНОЧНЫХ insertOne (не bulk — bulk смазал бы разницу w:1 vs
	// w:majority, там ack на КАЖДУЮ запись, а не на батч).
	wcLoadColl     = "wc_load"
	wcLoadDocs     = 200
	wcPayloadBytes = 200

	// causalDemoColl — read-your-writes сценарий.
	causalDemoColl = "causal_demo"

	// failoverDemoColl — единственная коллекция фазы "failover-write".
	failoverDemoColl = "failover_demo"
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
	// Стенд всегда запускается с cwd=replication/ (см.
	// ops/replication-demo.sh: `-w /app/replication` в контейнере
	// golang:1.25, где /app — весь каталог mongodb/), поэтому
	// ../dataset/manifest.json всегда рядом.
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

	phase := "core"
	if len(os.Args) > 1 && os.Args[1] != "" {
		phase = os.Args[1]
	}

	mongoURI := mongoconn.MustEnv("MONGO_URI")
	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	db := client.Database(dbName)

	switch phase {
	case "failover-write":
		failoverWritePhase(ctx, client, db)
		return
	case "core":
		// продолжение ниже.
	default:
		log.Fatalf(`неизвестная фаза %q (ожидается "core" или "failover-write")`, phase)
	}

	m := loadManifest()
	log.Printf("manifest: seed=%d users=%d products=%d orders=%d", m.Seed, m.Users, m.Products, m.Orders)

	usersColl := db.Collection("users")
	productsColl := db.Collection("products")
	ordersColl := db.Collection("orders")
	assertImportCounts(ctx, m, usersColl, productsColl, ordersColl)

	oplogScenario(ctx, client, db)
	writeConcernScenario(ctx, db)
	causalConsistencyScenario(ctx, mongoURI)
	pgContrastScenario(ctx)

	log.Println("готово (фаза core). Кластер остаётся здоровым для последующего failover-шага demo-скрипта.")
}

// assertImportCounts — та же проверка, что и в остальных стендах серии:
// счётчики импортированных коллекций должны совпасть с
// dataset/manifest.json (единственный источник правды по seed/counts).
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
	fmt.Printf("FIXTURE replication: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// -- Сценарий 1: oplog ------------------------------------------------------

// oplogScenario — вставляет один документ и читает породившуюся запись из
// local.oplog.rs НА PRIMARY (readpref.Primary() явно — oplog каждого узла
// это его собственный журнал применённых операций; интересует запись
// ИМЕННО на том узле, где произошла запись). Печатает структуру записи
// целиком (extended JSON) и проверяет ожидаемую форму (op="i", ns
// совпадает, поля ts/wall/o присутствуют).
func oplogScenario(ctx context.Context, client *mongo.Client, db *mongo.Database) {
	coll := db.Collection(replicationDemoColl)
	if err := coll.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("oplog: drop %s: %v", replicationDemoColl, err)
	}

	doc := bson.D{
		{Key: "marker", Value: "oplog-demo"},
		{Key: "seq", Value: int32(1)},
		{Key: "created_at", Value: time.Now()},
	}
	t0 := time.Now()
	res, err := coll.InsertOne(ctx, doc)
	if err != nil {
		log.Fatalf("oplog: insertOne: %v", err)
	}
	insertDur := time.Since(t0)
	insertedID, ok := res.InsertedID.(bson.ObjectID)
	if !ok {
		log.Fatalf("oplog: InsertedID неожиданного типа %T", res.InsertedID)
	}

	oplogColl := client.Database("local").Collection("oplog.rs", options.Collection().SetReadPreference(readpref.Primary()))
	ns := dbName + "." + replicationDemoColl
	filter := bson.D{
		{Key: "ns", Value: ns},
		{Key: "op", Value: "i"},
		{Key: "o._id", Value: insertedID},
	}
	var entry bson.D
	if err := oplogColl.FindOne(ctx, filter).Decode(&entry); err != nil {
		log.Fatalf("assert: не нашли запись в local.oplog.rs для только что вставленного документа (ns=%s _id=%s): %v", ns, insertedID.Hex(), err)
	}

	op, _ := bsonDGet(entry, "op").(string)
	entryNs, _ := bsonDGet(entry, "ns").(string)
	tsVal := bsonDGet(entry, "ts")
	wallVal := bsonDGet(entry, "wall")
	oVal := bsonDGet(entry, "o")

	extJSON, err := bson.MarshalExtJSON(entry, false, false)
	if err != nil {
		log.Fatalf("oplog: MarshalExtJSON записи: %v", err)
	}

	fmt.Printf("FIXTURE replication: oplog_insert_latency=%v oplog_ns=%s oplog_op=%s oplog_entry=%s\n", insertDur, entryNs, op, string(extJSON))

	if op != "i" {
		log.Fatalf(`assert: oplog-запись для insert должна иметь op="i", got %q`, op)
	}
	if entryNs != ns {
		log.Fatalf("assert: oplog-запись должна иметь ns=%q, got %q", ns, entryNs)
	}
	if tsVal == nil || wallVal == nil || oVal == nil {
		log.Fatalf("assert: oplog-запись должна содержать поля ts/wall/o одновременно, got ts=%v wall=%v o=%v", tsVal, wallVal, oVal)
	}
	log.Printf("assert OK: insert породил oplog-запись ожидаемого вида (op=%q ns=%q, latency=%v)", op, entryNs, insertDur)
}

// -- Сценарий 2: write concern w:1 vs w:majority -----------------------------

// writeConcernScenario — серия ОДИНОЧНЫХ insertOne (wcLoadDocs документов
// каждый вариант) с явным write concern w:1 и w:majority — реальные
// латентности. Ассерт "majority дороже" — МЯГКИЙ: на одном docker-хосте
// (все три узла на одной машине/сети) ack-round-trip до большинства узлов
// может утонуть в шуме против w:1 — тогда честно фиксируем это как
// оговорку, не роняем стенд (см. бриф: "record honestly if within noise on
// a single host").
func writeConcernScenario(ctx context.Context, db *mongo.Database) {
	base := db.Collection(wcLoadColl)
	if err := base.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("write concern: drop %s: %v", wcLoadColl, err)
	}

	w1Coll := db.Collection(wcLoadColl, options.Collection().SetWriteConcern(writeconcern.W1()))
	majColl := db.Collection(wcLoadColl, options.Collection().SetWriteConcern(writeconcern.Majority()))
	payload := strings.Repeat("x", wcPayloadBytes)

	w1Avg, w1Total := runWCLoad(ctx, w1Coll, "w1", payload)
	fmt.Printf("FIXTURE replication: write_concern=w1 docs=%d avg_latency=%v total_latency=%v\n", wcLoadDocs, w1Avg, w1Total)

	majAvg, majTotal := runWCLoad(ctx, majColl, "majority", payload)
	fmt.Printf("FIXTURE replication: write_concern=majority docs=%d avg_latency=%v total_latency=%v\n", wcLoadDocs, majAvg, majTotal)

	if majAvg > w1Avg {
		ratio := float64(majAvg) / float64(w1Avg)
		log.Printf("assert OK: write concern majority дороже w:1 (%.2fx): avg w1=%v avg majority=%v", ratio, w1Avg, majAvg)
	} else {
		log.Printf("честная оговорка (single-host, не роняет стенд): majority НЕ оказался измеримо дороже w:1 на этой топологии (avg w1=%v avg majority=%v) — три узла на одном docker-хосте дают суб-миллисекундный ack-round-trip между собой, разница тонет в шуме; сам механизм (ожидание подтверждения БОЛЬШИНСТВОМ узлов, а не только primary) от этого не перестаёт существовать — на реальной сети между разными датацентрами/AZ разница станет наблюдаемой", w1Avg, majAvg)
	}
}

func runWCLoad(ctx context.Context, coll *mongo.Collection, tag, payload string) (avg, total time.Duration) {
	t0 := time.Now()
	for i := 0; i < wcLoadDocs; i++ {
		doc := bson.D{
			{Key: "tag", Value: tag},
			{Key: "idx", Value: i},
			{Key: "payload", Value: payload},
		}
		if _, err := coll.InsertOne(ctx, doc); err != nil {
			log.Fatalf("write concern %s: insertOne[%d]: %v", tag, i, err)
		}
	}
	total = time.Since(t0)
	avg = total / time.Duration(wcLoadDocs)
	return avg, total
}

// -- Сценарий 3: read concern / причинная согласованность -------------------

// causalConsistencyScenario — ОСНОВНОЙ payoff стенда: причинно-
// согласованная сессия делает read-your-writes через secondary
// (readpref.Secondary()). Доказательство механизма — НЕ "повезло/не
// повезло с гонкой", а перехват СЫРОЙ команды find через
// *event.CommandMonitor на ВЫДЕЛЕННОМ клиенте (тот же приём, что и
// modeling/main.go scenarioReferencedLookup): у причинно-согласованной
// сессии драйвер ОБЯЗАН добавить readConcern.afterClusterTime в команду
// find — это гарантированное, детерминированное поведение клиента (не
// зависит от реальной задержки репликации), и именно ОНО заставляет
// secondary ДОЖДАТЬСЯ применения нужной операции из oplog, прежде чем
// ответить. Без сессии — такого поля в команде НЕ будет никогда.
//
// "Без причинной согласованности" в этом сценарии — ЧИСТО наблюдательная
// часть (см. package doc и бриф: "note what happens without it (may
// lag)"): на одном docker-хосте локальная репликация обычно успевает
// раньше, чем клиент делает независимый read, поэтому документ чаще всего
// ВСЁ РАВНО находится сразу — это НЕ опровергает риск гонки, это честная
// оговорка топологии одного хоста, зафиксированная как факт, а не ассерт.
func causalConsistencyScenario(ctx context.Context, mongoURI string) {
	var lastFindCmd bson.Raw
	monitor := &event.CommandMonitor{
		Started: func(_ context.Context, evt *event.CommandStartedEvent) {
			if evt.CommandName == "find" && evt.DatabaseName == dbName {
				lastFindCmd = evt.Command
			}
		},
	}
	clientOpts := options.Client().ApplyURI(mongoURI).SetMonitor(monitor)
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		log.Fatalf("causal: выделенный клиент с CommandMonitor: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("causal: ping выделенного клиента: %v", err)
	}

	db := client.Database(dbName)
	coll := db.Collection(causalDemoColl)
	if err := coll.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("causal: drop %s: %v", causalDemoColl, err)
	}
	secColl := db.Collection(causalDemoColl, options.Collection().SetReadPreference(readpref.Secondary()))

	// -- WITH причинно-согласованная сессия: read-your-writes через secondary.
	sess, err := client.StartSession(options.Session().SetCausalConsistency(true))
	if err != nil {
		log.Fatalf("causal: StartSession(causal=true): %v", err)
	}
	sessCtx := mongo.NewSessionContext(ctx, sess)

	t0 := time.Now()
	if _, err := coll.InsertOne(sessCtx, bson.D{{Key: "marker", Value: "causal-yes"}, {Key: "seq", Value: int32(1)}}); err != nil {
		log.Fatalf("causal: insertOne (with session): %v", err)
	}
	var withResult bson.M
	errWith := secColl.FindOne(sessCtx, bson.D{{Key: "marker", Value: "causal-yes"}}).Decode(&withResult)
	withDur := time.Since(t0)
	sess.EndSession(ctx)

	withFound := errWith == nil
	withHasAfterClusterTime := commandHasAfterClusterTime(lastFindCmd)

	fmt.Printf("FIXTURE replication: causal_with_session_found=%v causal_with_latency=%v causal_with_aftercluster_time_in_command=%v\n",
		withFound, withDur, withHasAfterClusterTime)

	if !withFound {
		log.Fatalf("assert: причинно-согласованная сессия ДОЛЖНА видеть свою же запись при чтении с secondary (read-your-writes), но FindOne не нашёл документ: %v", errWith)
	}
	if !withHasAfterClusterTime {
		log.Fatalf("assert: find-команда причинно-согласованной сессии ДОЛЖНА нести readConcern.afterClusterTime (механизм ожидания реплики секцией сервера) — в перехваченной команде поле отсутствует: %s", lastFindCmd.String())
	}
	log.Printf("assert OK: причинно-согласованная сессия увидела свою запись на secondary (%v), find-команда несла readConcern.afterClusterTime — реальный, а не предполагаемый механизм ожидания реплики", withDur)

	// -- БЕЗ причинной согласованности: независимый read сразу после записи.
	lastFindCmd = nil
	t0 = time.Now()
	if _, err := coll.InsertOne(ctx, bson.D{{Key: "marker", Value: "causal-no"}, {Key: "seq", Value: int32(1)}}); err != nil {
		log.Fatalf("causal: insertOne (without session): %v", err)
	}
	var withoutResult bson.M
	errWithout := secColl.FindOne(ctx, bson.D{{Key: "marker", Value: "causal-no"}}).Decode(&withoutResult)
	withoutDur := time.Since(t0)

	withoutFound := errWithout == nil
	withoutHasAfterClusterTime := commandHasAfterClusterTime(lastFindCmd)

	fmt.Printf("FIXTURE replication: causal_without_session_found_immediately=%v causal_without_latency=%v causal_without_aftercluster_time_in_command=%v\n",
		withoutFound, withoutDur, withoutHasAfterClusterTime)

	// Детерминированная (не гоночная) половина контраста: БЕЗ сессии
	// afterClusterTime в команде НИКОГДА не появляется — это гарантирует
	// сам драйвер (нет сессии — неоткуда взять operationTime), поэтому
	// это жёсткий ассерт, а не наблюдение.
	if withoutHasAfterClusterTime {
		log.Fatalf("assert: find-команда БЕЗ сессии НЕ должна нести readConcern.afterClusterTime (взяться неоткуда без сессии с причинной согласованностью), но поле присутствует: %s", lastFindCmd.String())
	}
	foundWord := "НЕ найден"
	if withoutFound {
		foundWord = "найден"
	}
	log.Printf("наблюдение (НЕ ассерт — честная оговорка брифа \"may lag\"): БЕЗ причинно-согласованной сессии find-команда afterClusterTime не несёт (подтверждено), документ на secondary сразу после записи %s за %v — на этом single-host docker-стенде локальная репликация обычно опережает независимый клиентский read; риск гонки от этого не исчезает, просто не каждый прогон её ловит",
		foundWord, withoutDur)
}

// commandHasAfterClusterTime проверяет наличие readConcern.afterClusterTime
// в сырой команде, перехваченной *event.CommandMonitor.
func commandHasAfterClusterTime(cmd bson.Raw) bool {
	if cmd == nil {
		return false
	}
	_, err := cmd.LookupErr("readConcern", "afterClusterTime")
	return err == nil
}

// -- Сценарий 4: PG-контраст (опционально) -----------------------------------

// pgContrastScenario — один живой факт из compose/postgres.yml (если
// PG_DSN задан): wal_level образа postgres:18 "из коробки". Полноценный
// стенд логической репликации (publication/subscription) — ЗА РАМКАМИ
// этой задачи (см. package doc); граница зафиксирована как честная
// оговорка, а не как невыполненный пункт брифа (бриф помечает PG-контраст
// опциональным).
func pgContrastScenario(ctx context.Context) {
	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		log.Println("PG_DSN не задан — PG-контраст пропущен (граница на возможную отдельную статью про PG-репликацию, см. package doc)")
		return
	}
	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pg contrast: pgxpool.New: %v", err)
	}
	defer pool.Close()

	var walLevel string
	if err := pool.QueryRow(ctx, "SHOW wal_level").Scan(&walLevel); err != nil {
		log.Fatalf("pg contrast: SHOW wal_level: %v", err)
	}
	var maxWalSenders string
	if err := pool.QueryRow(ctx, "SHOW max_wal_senders").Scan(&maxWalSenders); err != nil {
		log.Fatalf("pg contrast: SHOW max_wal_senders: %v", err)
	}

	fmt.Printf("FIXTURE replication: pg_wal_level=%s pg_max_wal_senders=%s\n", walLevel, maxWalSenders)

	if walLevel == "logical" {
		log.Printf("наблюдение: postgres:18 этого стенда уже сконфигурирован wal_level=logical (не значение образа по умолчанию)")
		return
	}
	log.Printf(`честная граница (реальный факт этой конфигурации, не общее правило PG): postgres:18 из compose/postgres.yml поднимается с wal_level=%q "из коробки" — логическая репликация PG (publication/subscription, декодирование WAL в построчные изменения) требует ЯВНОГО wal_level=logical (плюс перезапуск сервера) и НЕ включена по умолчанию. Контраст с MongoDB: oplog пишется БЕЗУСЛОВНО на любом узле replica set сразу после rs.initiate(), без отдельного флага — "встроено всегда" против "требует явного включения". Полноценный стенд publication/subscription — за рамками этой задачи (граница на возможную отдельную статью про PG-репликацию)`, walLevel)
}

// -- Фаза "failover-write" ---------------------------------------------------

// failoverWritePhase — вызывается demo-скриптом ПОСЛЕ того, как скрипт сам
// убедился (docker stop прежнего primary + polling db.hello() на выживших
// узлах), что новый primary уже избран. Единственная задача этой фазы —
// зафиксировать, что запись РЕАЛЬНО проходит на новый кластер: единственный
// ассерт — успешная вставка. hello().me печатается для видимости, какой
// узел обслужил запись.
func failoverWritePhase(ctx context.Context, client *mongo.Client, db *mongo.Database) {
	var hello bson.M
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		log.Fatalf("failover-write: hello(): %v", err)
	}
	primaryHost, _ := hello["me"].(string)

	coll := db.Collection(failoverDemoColl)
	doc := bson.D{{Key: "marker", Value: "post-failover"}, {Key: "at", Value: time.Now()}}
	t0 := time.Now()
	res, err := coll.InsertOne(ctx, doc)
	dur := time.Since(t0)

	fmt.Printf("FIXTURE replication: failover_write_primary=%s failover_write_success=%v failover_write_latency=%v\n", primaryHost, err == nil, dur)

	if err != nil {
		log.Fatalf("assert: запись ПОСЛЕ re-election должна пройти успешно, получена ошибка: %v", err)
	}
	if res.InsertedID == nil {
		log.Fatalf("assert: успешная запись должна вернуть InsertedID, получен nil")
	}
	log.Printf("assert OK: запись после re-election прошла успешно (primary=%s, latency=%v)", primaryHost, dur)
}

// -- служебное ----------------------------------------------------------------

func bsonDGet(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

func isNamespaceNotFound(err error) bool {
	// Drop() коллекции, которой ещё нет (первый прогон стенда на свежем
	// кластере) — не ошибка сценария, а идемпотентность повторного запуска
	// (тот же приём, что и в остальных стендах серии).
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 26 // NamespaceNotFound
	}
	return false
}
