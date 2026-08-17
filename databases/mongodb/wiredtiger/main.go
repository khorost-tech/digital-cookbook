// Command wiredtiger — стенд #2 серии "MongoDB: глубокое погружение":
// движок хранения WiredTiger вживую — cache (in-memory B-tree pages),
// checkpoint, journal (WAL-аналог) — на РЕАЛЬНОМ 3-узловом replica set
// (rs0), поверх уже импортированного датасета (Task 3).
//
// Честная оговорка, унаследованная от Task 3 (README/FIXTURES.md §1,
// growth_demo): у WiredTiger НЕТ MMAPv1-стиля "перемещения документа" —
// каждое обновление это read-modify-write B-tree страницы в кеше (MVCC),
// без padding/фиксированных слотов. Этот стенд смотрит на WiredTiger в его
// собственных терминах: cache (bytes currently in the cache / tracked dirty
// bytes / pages evicted), checkpoint (флаш dirty-страниц на диск), journal
// (write-ahead log, отдельный от checkpoint механизм durability).
//
// Сценарий (см. ../ops/wiredtiger-demo.sh для оркестрации инфраструктуры):
//  1. Проверяет счётчики импортированных коллекций против ../dataset/manifest.json
//     (та же дисциплина, что и modeling/main.go — единственный источник
//     правды по seed/counts).
//  2. Снимает serverStatus().wiredTiger ДО какой-либо нагрузки этого стенда
//     (snapshot "before").
//  3. Read-нагрузка: полный скан коллекции orders (200000 документов,
//     только _id+status — минимальная проекция) — ассерт: "pages requested
//     from the cache" в cache-секции обязано вырасти (чтение ВСЕГДА
//     запрашивает страницы у кеша, независимо от того, холодный он или
//     тёплый после mongoimport).
//  4. Write-нагрузка: bulk-вставка writeLoadDocs документов
//     (~writeLoadPayloadBytes байт полезной нагрузки каждый) в ОТДЕЛЬНУЮ
//     коллекцию wt_load (не трогает orders/users/products — те нужны
//     последующим стендам серии #3/#4 в исходном виде). Ассерт: dirty bytes
//     в кеше растут, журнал (log bytes written) растёт — оба подтверждены
//     ЖИВЫМ прогоном перед написанием этого файла (см. FIXTURES.md §2).
//  5. Принудительный checkpoint: db.adminCommand({fsync:1}) на admin БД.
//     Ассерт: счётчик успешных checkpoint'ов вырос, dirty bytes упали
//     (снова подтверждено живым прогоном).
//
// Все snapshot'ы serverStatus() снимаются с ЯВНЫМ readpref.Primary(): и
// read-, и write-нагрузка этого стенда всегда идёт на primary (стандартное
// поведение драйвера для записи + дефолтный ReadPreference=primary для
// чтения), поэтому метрики cache/checkpoint/log должны сниматься С ТОГО ЖЕ
// узла — иначе можно случайно увидеть состояние secondary, которое не имеет
// отношения к только что сгенерированной нагрузке (WiredTiger cache —
// per-node ресурс, не реплицируется).
//
// Ассерты — на КАЧЕСТВЕННЫЕ инварианты (рост/падение метрик), не на
// конкретные числа: числа стенд производит сам и печатает как факты
// (строки "FIXTURE wiredtiger: ..."), см. FIXTURES.md §2.
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

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const (
	dbName = "cookbook"

	// writeLoadDocs/writeLoadPayloadBytes/writeLoadBatch — размер write-
	// нагрузки. Подобрано эмпирически (см. коммит-сообщение/README): на
	// живом стенде (docker compose, replica-set.yml, 3 узла mongo:8.2.11) с
	// уже импортированным полным датасетом (Task 3: users=50000
	// products=5000 orders=200000) этот объём даёт НАБЛЮДАЕМЫЙ (не на грани
	// шума) рост tracked dirty bytes in the cache и log bytes written за
	// разумное время (десятки секунд), не упираясь в автоматический
	// checkpoint (интервал по умолчанию 60s) раньше, чем стенд успевает
	// снять snapshot "after_write_load".
	writeLoadDocs         = 100_000
	writeLoadPayloadBytes = 800
	writeLoadBatch        = 5000
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
	// Стенд всегда запускается с cwd=wiredtiger/ (см. ops/wiredtiger-demo.sh:
	// `-w /app/wiredtiger` в контейнере golang:1.25, где /app — весь каталог
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
	adminDB := client.Database("admin")
	usersColl := db.Collection("users")
	productsColl := db.Collection("products")
	ordersColl := db.Collection("orders")
	loadColl := db.Collection("wt_load")

	assertImportCounts(ctx, m, usersColl, productsColl, ordersColl)

	// wt_load — идемпотентность повторного прогона: если стенд запускался
	// раньше на этом же кластере и не был снесён, коллекция могла остаться
	// с предыдущими документами.
	if err := loadColl.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("drop wt_load: %v", err)
	}

	before := snapshotWT(ctx, adminDB)
	printSnapshot("before", before)

	// -- Read-нагрузка: полный скан orders (200000 документов) --------------
	readCount, readDur := readLoad(ctx, ordersColl)
	afterRead := snapshotWT(ctx, adminDB)
	printSnapshot("after_read_load", afterRead)
	fmt.Printf("FIXTURE wiredtiger: read_load_docs=%d read_load_latency=%v\n", readCount, readDur)

	if int(readCount) != m.Orders {
		log.Fatalf("assert: read-нагрузка должна пройти по всем заказам manifest.json, got %d != %d", readCount, m.Orders)
	}
	if afterRead.PagesRequested <= before.PagesRequested {
		log.Fatalf("assert: read-нагрузка должна увеличить 'pages requested from the cache' — before=%d after=%d", before.PagesRequested, afterRead.PagesRequested)
	}
	log.Printf("assert OK: read-нагрузка (%d документов, %v) увеличила pages requested from the cache: %d -> %d",
		readCount, readDur, before.PagesRequested, afterRead.PagesRequested)

	// -- Write-нагрузка: bulk-вставка в отдельную коллекцию wt_load --------
	insertedCount, writeDur := writeLoad(ctx, loadColl)
	afterWrite := snapshotWT(ctx, adminDB)
	printSnapshot("after_write_load", afterWrite)
	fmt.Printf("FIXTURE wiredtiger: write_load_docs=%d write_load_payload_bytes=%d write_load_latency=%v\n",
		insertedCount, writeLoadPayloadBytes, writeDur)

	if insertedCount != writeLoadDocs {
		log.Fatalf("assert: write-нагрузка должна вставить ровно writeLoadDocs документов, got %d != %d", insertedCount, writeLoadDocs)
	}
	if afterWrite.DirtyBytes <= afterRead.DirtyBytes {
		log.Fatalf("assert: 'tracked dirty bytes in the cache' должны вырасти под write-нагрузкой — after_read=%d after_write=%d", afterRead.DirtyBytes, afterWrite.DirtyBytes)
	}
	if afterWrite.LogBytesWritten <= afterRead.LogBytesWritten {
		log.Fatalf("assert: журнал ('log bytes written') должен расти при записи — after_read=%d after_write=%d", afterRead.LogBytesWritten, afterWrite.LogBytesWritten)
	}
	log.Printf("assert OK: write-нагрузка (%d документов, %v) подняла dirty bytes (%d -> %d) и журнал (log bytes written: %d -> %d)",
		insertedCount, writeDur, afterRead.DirtyBytes, afterWrite.DirtyBytes, afterRead.LogBytesWritten, afterWrite.LogBytesWritten)

	// -- Принудительный checkpoint: db.adminCommand({fsync:1}) -------------
	t0 := time.Now()
	if err := adminDB.RunCommand(ctx, bson.D{{Key: "fsync", Value: 1}}).Err(); err != nil {
		log.Fatalf("adminCommand({fsync:1}): %v", err)
	}
	fsyncDur := time.Since(t0)
	afterCheckpoint := snapshotWT(ctx, adminDB)
	printSnapshot("after_checkpoint", afterCheckpoint)
	fmt.Printf("FIXTURE wiredtiger: fsync_latency=%v\n", fsyncDur)

	if afterCheckpoint.CheckpointsSucceed <= afterWrite.CheckpointsSucceed {
		log.Fatalf("assert: 'total succeed number of checkpoints' должен вырасти после fsync:1 — after_write=%d after_checkpoint=%d",
			afterWrite.CheckpointsSucceed, afterCheckpoint.CheckpointsSucceed)
	}
	if afterCheckpoint.DirtyBytes >= afterWrite.DirtyBytes {
		log.Fatalf("assert: 'tracked dirty bytes in the cache' должны упасть после checkpoint — after_write=%d after_checkpoint=%d",
			afterWrite.DirtyBytes, afterCheckpoint.DirtyBytes)
	}
	log.Printf("assert OK: checkpoint выполнился (checkpoints succeed: %d -> %d, %v), dirty bytes упали: %d -> %d",
		afterWrite.CheckpointsSucceed, afterCheckpoint.CheckpointsSucceed, fsyncDur, afterWrite.DirtyBytes, afterCheckpoint.DirtyBytes)

	log.Println("готово.")
}

// assertImportCounts — та же проверка, что и в modeling/main.go: счётчики
// импортированных коллекций должны совпасть с dataset/manifest.json
// (единственный источник правды по seed/counts).
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
	fmt.Printf("FIXTURE wiredtiger: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// wtStats — подмножество полей db.serverStatus().wiredTiger, реально
// существующих в этой БД (проверено живым прогоном mongosh на mongo:8.2.11
// перед написанием этого файла, см. отчёт Task 4 / FIXTURES.md §2) и
// нужных сценарию этого стенда: cache (объём в кеше, dirty-байты,
// вытеснение страниц, счётчик обращений к кешу), log/journal (байты,
// записанные в журнал) и checkpoint (успешные checkpoint'ы).
type wtStats struct {
	BytesInCache       int64
	DirtyBytes         int64
	ModifiedEvicted    int64
	UnmodifiedEvicted  int64
	PagesRequested     int64
	LogBytesWritten    int64
	CheckpointsSucceed int64
}

// snapshotWT снимает db.serverStatus().wiredTiger ЯВНО с primary
// (readpref.Primary()) — WiredTiger cache/checkpoint/log это ресурсы
// КОНКРЕТНОГО узла, не реплицируются; и read-, и write-нагрузка этого
// стенда всегда идут на primary, поэтому снимать метрики нужно с того же
// узла, иначе можно случайно замерить secondary, никак не связанный с
// только что сгенерированной нагрузкой.
func snapshotWT(ctx context.Context, adminDB *mongo.Database) wtStats {
	var status bson.M
	cmd := bson.D{{Key: "serverStatus", Value: 1}}
	opts := options.RunCmd().SetReadPreference(readpref.Primary())
	if err := adminDB.RunCommand(ctx, cmd, opts).Decode(&status); err != nil {
		log.Fatalf("serverStatus: %v", err)
	}
	// Внимание: вложенные BSON-документы декодируются драйвером в поле типа
	// `any` (значение map bson.M) КАК bson.D (упорядоченный список пар), НЕ
	// bson.M — это поведение по умолчанию и для v1, и для v2 драйвера при
	// декодировании в interface{}/map[string]any. Поэтому секции
	// cache/log/checkpoint приводим к bson.D и достаём поля через bsonDGet,
	// а не через прямой доступ по ключу карты.
	wtRaw, ok := status["wiredTiger"]
	if !ok {
		log.Fatalf("serverStatus: секция wiredTiger отсутствует в ответе")
	}
	wt, ok := wtRaw.(bson.D)
	if !ok {
		log.Fatalf("serverStatus: секция wiredTiger неожиданного типа %T", wtRaw)
	}
	cache, _ := bsonDGet(wt, "cache").(bson.D)
	logSec, _ := bsonDGet(wt, "log").(bson.D)
	ckpt, _ := bsonDGet(wt, "checkpoint").(bson.D)

	return wtStats{
		BytesInCache:       toInt64(bsonDGet(cache, "bytes currently in the cache")),
		DirtyBytes:         toInt64(bsonDGet(cache, "tracked dirty bytes in the cache")),
		ModifiedEvicted:    toInt64(bsonDGet(cache, "modified pages evicted")),
		UnmodifiedEvicted:  toInt64(bsonDGet(cache, "unmodified pages evicted")),
		PagesRequested:     toInt64(bsonDGet(cache, "pages requested from the cache")),
		LogBytesWritten:    toInt64(bsonDGet(logSec, "log bytes written")),
		CheckpointsSucceed: toInt64(bsonDGet(ckpt, "total succeed number of checkpoints")),
	}
}

// bsonDGet ищет значение по ключу в bson.D (линейный поиск — секции
// serverStatus содержат десятки-сотни полей, но снимаются считанные разы за
// прогон стенда, производительность не критична).
func bsonDGet(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// toInt64 — служебная конвертация числовых полей serverStatus(): драйвер
// декодирует BSON-числа в bson.M как int32/int64/float64 в зависимости от
// того, как именно сервер сериализовал конкретное поле; поля этого стенда
// (счётчики байт/страниц) всегда неотрицательные целые, поэтому единый
// конвертер без потери точности для интересующего нас диапазона.
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

func printSnapshot(phase string, s wtStats) {
	fmt.Printf("FIXTURE wiredtiger: phase=%s bytes_in_cache=%d dirty_bytes=%d modified_evicted=%d unmodified_evicted=%d pages_requested=%d log_bytes_written=%d checkpoints_succeed=%d\n",
		phase, s.BytesInCache, s.DirtyBytes, s.ModifiedEvicted, s.UnmodifiedEvicted, s.PagesRequested, s.LogBytesWritten, s.CheckpointsSucceed)
}

type orderMin struct {
	ID     bson.ObjectID `bson:"_id"`
	Status string        `bson:"status"`
}

// readLoad — полный скан коллекции orders (минимальная проекция _id+status)
// — read-нагрузка, единственная цель которой заставить WiredTiger
// действительно обращаться к кешу за каждой B-tree страницей коллекции
// (курсор идёт по всей коллекции без индекса-покрытия только по
// проекции), вне зависимости от того, тёплый кеш (данные только что
// импортированы mongoimport'ом) или холодный.
func readLoad(ctx context.Context, ordersColl *mongo.Collection) (count int64, dur time.Duration) {
	t0 := time.Now()
	proj := options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}, {Key: "status", Value: 1}})
	cur, err := ordersColl.Find(ctx, bson.D{}, proj)
	if err != nil {
		log.Fatalf("read-load Find(orders): %v", err)
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var o orderMin
		if err := cur.Decode(&o); err != nil {
			log.Fatalf("read-load decode: %v", err)
		}
		count++
	}
	if err := cur.Err(); err != nil {
		log.Fatalf("read-load cursor: %v", err)
	}
	return count, time.Since(t0)
}

type loadDoc struct {
	Idx     int    `bson:"idx"`
	Payload string `bson:"payload"`
}

// writeLoad — bulk-вставка writeLoadDocs документов (~writeLoadPayloadBytes
// байт payload каждый) в коллекцию loadColl, батчами writeLoadBatch (драйвер
// и так режет InsertMany по лимиту сервера, но явный батч даёт стабильную,
// воспроизводимую форму нагрузки и предсказуемый прогресс в логах).
func writeLoad(ctx context.Context, loadColl *mongo.Collection) (inserted int, dur time.Duration) {
	payload := strings.Repeat("x", writeLoadPayloadBytes)
	t0 := time.Now()
	for start := 0; start < writeLoadDocs; start += writeLoadBatch {
		end := start + writeLoadBatch
		if end > writeLoadDocs {
			end = writeLoadDocs
		}
		docs := make([]any, 0, end-start)
		for i := start; i < end; i++ {
			docs = append(docs, loadDoc{Idx: i, Payload: payload})
		}
		res, err := loadColl.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
		if err != nil {
			log.Fatalf("write-load InsertMany[%d:%d]: %v", start, end, err)
		}
		inserted += len(res.InsertedIDs)
	}
	return inserted, time.Since(t0)
}

func isNamespaceNotFound(err error) bool {
	// Drop() коллекции, которой ещё нет (первый прогон стенда на свежем
	// кластере) — не ошибка сценария, а идемпотентность повторного запуска
	// (тот же приём, что и modeling/main.go).
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 26 // NamespaceNotFound
	}
	return false
}
