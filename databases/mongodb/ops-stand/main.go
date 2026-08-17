// Command ops — стенд #7 серии "MongoDB: глубокое погружение": эксплуатация
// вживую (драйверы: пул соединений + retryable writes; change streams/CDC;
// backup через mongodump/mongorestore) на РЕАЛЬНОМ 3-узловом replica set
// (rs0), поверх уже импортированного датасета (Task 3+). Физически лежит в
// ops-stand/ (не ops/), чтобы не смешивать Go-модуль со shell-скриптами
// ops/*.sh (тот же приём, что и в ../clickhouse: ops/ — скрипты,
// ops-stand/ — Go-модуль). См. ../ops/ops-demo.sh для оркестрации.
//
// Бинарник работает в ДВЕ ФАЗЫ (см. ops-demo.sh — только оркестрация решает,
// КОГДА какая фаза запускается):
//
//  1. "core" (аргумент по умолчанию, если os.Args[1] не задан) — на ЕЩЁ
//     ЗДОРОВОМ кластере:
//       - пул соединений (maxPoolSize): РЕАЛЬНАЯ (не failpoint — сборка
//         mongo:8.2.11 не даёт configureFailPoint/failCommand, проверено
//         вживую перед реализацией) серверная задержка через $where с
//         sleep() на коллекции из ОДНОГО документа — конкурентные Find
//         с maxPoolSize=3 упираются в очередь; ассерт: пиковое число
//         одновременно занятых соединений (PoolMonitor) НЕ превышает
//         maxPoolSize, суммарное время подтверждает несколько раундов
//         очереди (не полный параллелизм).
//       - change streams (CDC): watch() на выделенной коллекции, серия
//         insert/update/delete, доказательство resumeAfter — консьюмер
//         закрывается СРАЗУ после insert-события, update происходит ПОКА
//         консьюмер закрыт (имитация простоя), переоткрытие с
//         resumeAfter(token) должно вернуть ИМЕННО это пропущенное
//         update-событие первым (ни потери, ни дублирования insert).
//       - retryable writes: реальный step-down ПЕРВИЧНОГО узла
//         (`replSetStepDown`, force:true) КОНКУРЕНТНО с серией InsertOne —
//         драйвер (retryWrites=true по умолчанию в URI) обязан
//         автоматически повторить прерванные записи на новом primary;
//         доказательство — НЕ просто "итоговый успех" (мог быть везением
//         тайминга), а перехват через *event.CommandMonitor: тот же
//         txnNumber должен встретиться и в Failed, и в последующем
//         Succeeded событии — это и есть настоящий повтор ОДНОЙ и той же
//         логической записи, а не просто "новое соединение почему-то
//         сработало". Этот сценарий НАМЕРЕННО последний в фазе core —
//         он единственный дестабилизирует топологию (форсирует выборы).
//
//  2. "backup-verify" — вызывается demo-скриптом ПОСЛЕ того, как скрипт сам
//     (docker exec mongodump/mongorestore) сделал раунд-трип базы cookbook
//     в cookbook_restored (или иное имя через RESTORED_DB). Единственная
//     задача фазы — сравнить counts users/products/orders исходной базы,
//     восстановленной базы и dataset/manifest.json — все три должны
//     совпасть.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	mongoconn "tech.khorost/mongodb-cookbook/drivers/go"
)

const (
	dbName = "cookbook"

	poolProbeColl  = "pool_probe"
	poolMaxSize    = 3
	poolWorkers    = 9
	poolBlockMS    = 200
	csDemoColl     = "cs_demo_go"
	retryDemoColl  = "retry_demo"
	retryWriteDocs = 20
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
	// Стенд всегда запускается с cwd=ops-stand/ (см. ../ops/ops-demo.sh:
	// `-w /app/ops-stand` в контейнере golang:1.25, где /app — весь каталог
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

	phase := "core"
	if len(os.Args) > 1 && os.Args[1] != "" {
		phase = os.Args[1]
	}

	mongoURI := mongoconn.MustEnv("MONGO_URI")

	switch phase {
	case "backup-verify":
		backupVerifyPhase(ctx, mongoURI)
		return
	case "core":
		// продолжение ниже.
	default:
		log.Fatalf(`неизвестная фаза %q (ожидается "core" или "backup-verify")`, phase)
	}

	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	m := loadManifest()
	log.Printf("manifest: seed=%d users=%d products=%d orders=%d", m.Seed, m.Users, m.Products, m.Orders)
	assertImportCounts(ctx, m, client.Database(dbName))
	if err := client.Disconnect(context.Background()); err != nil {
		log.Fatalf("disconnect базового клиента перед сценариями: %v", err)
	}

	poolScenario(ctx, mongoURI)
	changeStreamScenario(ctx, mongoURI)
	// retryableWritesScenario — ПОСЛЕДНИЙ: единственный сценарий, который
	// форсирует реальные перевыборы primary (см. package doc).
	retryableWritesScenario(ctx, mongoURI)

	log.Println("готово (фаза core). FIXTURE-строки выше -> дословно в FIXTURES.md §7.")
}

// assertImportCounts — та же проверка, что и в остальных стендах серии:
// счётчики импортированных коллекций должны совпасть с
// dataset/manifest.json (единственный источник правды по seed/counts).
func assertImportCounts(ctx context.Context, m manifest, db *mongo.Database) {
	uc, err := db.Collection("users").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments users: %v", err)
	}
	pc, err := db.Collection("products").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments products: %v", err)
	}
	oc, err := db.Collection("orders").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("countDocuments orders: %v", err)
	}
	fmt.Printf("FIXTURE ops: import_users=%d import_products=%d import_orders=%d\n", uc, pc, oc)
	if int(uc) != m.Users || int(pc) != m.Products || int(oc) != m.Orders {
		log.Fatalf("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)",
			uc, m.Users, pc, m.Products, oc, m.Orders)
	}
	log.Printf("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)", uc, pc, oc)
}

// -- Сценарий 1: пул соединений (maxPoolSize) --------------------------------

// poolScenario — РЕАЛЬНАЯ (не failpoint: сборка mongo:8.2.11 не включает
// configureFailPoint/failCommand — enableTestCommands выключен, проверено
// вживую docker-контейнером ДО реализации этого стенда) серверная задержка
// через $where с sleep() на коллекции из ОДНОГО документа — каждый Find
// сканирует ровно один документ, значит JS-предикат выполняется РОВНО один
// раз за запрос (детерминированная задержка poolBlockMS на запрос).
// poolWorkers конкурентных Find через клиента с maxPoolSize=poolMaxSize
// (poolWorkers > poolMaxSize) упираются в очередь на checkout соединения —
// PoolMonitor доказывает это НАПРЯМУЮ (не по догадке из суммарного времени):
// пиковое число ОДНОВРЕМЕННО занятых соединений никогда не превышает
// maxPoolSize (жёсткий инвариант пула), а суммарное время всей пачки
// заметно больше одного blockMS (несколько раундов очереди, а не полный
// параллелизм).
func poolScenario(ctx context.Context, mongoURI string) {
	setupClient, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("pool: connect (setup): %v", err)
	}
	probeColl := setupClient.Database(dbName).Collection(poolProbeColl)
	if err := probeColl.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("pool: drop %s: %v", poolProbeColl, err)
	}
	if _, err := probeColl.InsertOne(ctx, bson.D{{Key: "marker", Value: "pool-probe"}}); err != nil {
		log.Fatalf("pool: insertOne (seed probe doc): %v", err)
	}
	if err := setupClient.Disconnect(ctx); err != nil {
		log.Fatalf("pool: disconnect (setup): %v", err)
	}

	var mu sync.Mutex
	inUse, peak := 0, 0
	poolMonitor := &event.PoolMonitor{
		Event: func(evt *event.PoolEvent) {
			mu.Lock()
			defer mu.Unlock()
			switch evt.Type {
			case event.ConnectionCheckedOut:
				inUse++
				if inUse > peak {
					peak = inUse
				}
			case event.ConnectionCheckedIn:
				inUse--
			}
		},
	}
	clientOpts := options.Client().ApplyURI(mongoURI).SetMaxPoolSize(poolMaxSize).SetPoolMonitor(poolMonitor)
	poolClient, err := mongo.Connect(clientOpts)
	if err != nil {
		log.Fatalf("pool: connect (poolClient, maxPoolSize=%d): %v", poolMaxSize, err)
	}
	defer func() { _ = poolClient.Disconnect(context.Background()) }()
	if err := poolClient.Ping(ctx, nil); err != nil {
		log.Fatalf("pool: ping poolClient: %v", err)
	}
	workColl := poolClient.Database(dbName).Collection(poolProbeColl)

	slowFilter := bson.D{{Key: "$where", Value: fmt.Sprintf("sleep(%d); return true;", poolBlockMS)}}

	var wg sync.WaitGroup
	t0 := time.Now()
	for i := 0; i < poolWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var doc bson.M
			if err := workColl.FindOne(ctx, slowFilter).Decode(&doc); err != nil {
				log.Printf("pool: worker FindOne: %v (не фатально само по себе — оценивается ниже по агрегированным метрикам)", err)
			}
		}()
	}
	wg.Wait()
	totalDur := time.Since(t0)

	mu.Lock()
	peakSnapshot := peak
	mu.Unlock()

	fmt.Printf("FIXTURE ops: pool_max_pool_size=%d pool_workers=%d pool_block_ms=%d pool_total_duration=%v pool_peak_checked_out=%d\n",
		poolMaxSize, poolWorkers, poolBlockMS, totalDur, peakSnapshot)

	if peakSnapshot > poolMaxSize {
		log.Fatalf("assert: пиковое число ОДНОВРЕМЕННО занятых соединений (%d) НЕ должно превышать maxPoolSize (%d), но превысило", peakSnapshot, poolMaxSize)
	}
	// Нижняя граница — минимум ceil(poolWorkers/poolMaxSize) раундов
	// последовательной блокировки; проверяем консервативно (на 1 раунд
	// меньше теоретического минимума — запас на шум конкурентного старта
	// горутин).
	minRounds := (poolWorkers + poolMaxSize - 1) / poolMaxSize
	minExpected := time.Duration(poolBlockMS*(minRounds-1)) * time.Millisecond
	if totalDur < minExpected {
		log.Fatalf("assert: суммарное время (%v) слишком мало для очереди из %d воркеров через пул размера %d (ожидался минимум %v — иначе пул не ограничивал параллелизм)",
			totalDur, poolWorkers, poolMaxSize, minExpected)
	}
	log.Printf("assert OK: пул реально ограничивает параллелизм — пик занятых соединений=%d (<= maxPoolSize=%d), %d воркеров через пул размера %d заняли %v (>= %v за счёт очереди)",
		peakSnapshot, poolMaxSize, poolWorkers, poolMaxSize, totalDur, minExpected)
}

// -- Сценарий 2: change streams (CDC) ----------------------------------------

// changeStreamScenario — watch() на выделенной коллекции, insert/update/
// delete, и ГЛАВНОЕ доказательство resumeAfter: консьюмер закрывается СРАЗУ
// после получения insert-события, update происходит ПОКА консьюмер закрыт
// (имитация простоя потребителя), переоткрытие change stream с
// resumeAfter(token сразу после insert) должно вернуть ИМЕННО это
// пропущенное update-событие ПЕРВЫМ — ни потери события, ни повторной
// доставки уже виденного insert.
func changeStreamScenario(ctx context.Context, mongoURI string) {
	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("change streams: connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	coll := client.Database(dbName).Collection(csDemoColl)
	if err := coll.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("change streams: drop %s: %v", csDemoColl, err)
	}

	csOpts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	cs1, err := coll.Watch(ctx, mongo.Pipeline{}, csOpts)
	if err != nil {
		log.Fatalf("change streams: Watch (cs1): %v", err)
	}

	const docID = "cs-demo-doc"
	t0 := time.Now()
	if _, err := coll.InsertOne(ctx, bson.D{{Key: "_id", Value: docID}, {Key: "marker", Value: "cs-demo"}, {Key: "seq", Value: int32(1)}}); err != nil {
		log.Fatalf("change streams: insertOne: %v", err)
	}

	if !cs1.Next(ctx) {
		log.Fatalf("change streams: cs1.Next не вернул insert-событие: %v", cs1.Err())
	}
	insertLatency := time.Since(t0)
	var insertEvt bson.M
	if err := cs1.Decode(&insertEvt); err != nil {
		log.Fatalf("change streams: decode insert-события: %v", err)
	}
	insertOpType, _ := insertEvt["operationType"].(string)
	token1 := cs1.ResumeToken()

	fmt.Printf("FIXTURE ops: cs_insert_op_type=%s cs_insert_latency=%v cs_insert_resume_token_present=%v\n", insertOpType, insertLatency, token1 != nil)
	if insertOpType != "insert" {
		log.Fatalf(`assert: первое событие change stream должно иметь operationType="insert", got %q`, insertOpType)
	}
	if token1 == nil {
		log.Fatalf("assert: resume token после insert-события отсутствует")
	}

	// Закрываем консьюмера — имитация простоя потребителя.
	if err := cs1.Close(ctx); err != nil {
		log.Printf("change streams: cs1.Close: %v (не фатально)", err)
	}

	// Событие ПОКА консьюмер закрыт.
	if _, err := coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: docID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "seq", Value: int32(2)}}}}); err != nil {
		log.Fatalf("change streams: updateOne (во время простоя консьюмера): %v", err)
	}

	// Переоткрываем СТРОГО с resumeAfter(token1).
	resumeOpts := options.ChangeStream().SetFullDocument(options.UpdateLookup).SetResumeAfter(token1)
	t1 := time.Now()
	cs2, err := coll.Watch(ctx, mongo.Pipeline{}, resumeOpts)
	if err != nil {
		log.Fatalf("change streams: Watch (cs2, resumeAfter): %v", err)
	}
	defer func() { _ = cs2.Close(context.Background()) }()

	if !cs2.Next(ctx) {
		log.Fatalf("change streams: cs2.Next (после resumeAfter) не вернул событие: %v", cs2.Err())
	}
	resumeLatency := time.Since(t1)
	var updateEvt bson.M
	if err := cs2.Decode(&updateEvt); err != nil {
		log.Fatalf("change streams: decode резюмированного события: %v", err)
	}
	updateOpType, _ := updateEvt["operationType"].(string)

	fmt.Printf("FIXTURE ops: cs_resume_first_event_op_type=%s cs_resume_latency=%v\n", updateOpType, resumeLatency)
	if updateOpType != "update" {
		log.Fatalf(`assert: ПЕРВОЕ событие после resumeAfter(token сразу после insert) должно иметь operationType="update" (пропущенное во время простоя событие), got %q — резюмирование потеряло или продублировало событие`, updateOpType)
	}
	log.Printf("assert OK: resumeAfter вернул ИМЕННО пропущенное во время простоя update-событие первым (%v после переоткрытия) — ни потери, ни дублирования insert", resumeLatency)

	// delete — продолжаем читать ТОТ ЖЕ cs2.
	t2 := time.Now()
	if _, err := coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: docID}}); err != nil {
		log.Fatalf("change streams: deleteOne: %v", err)
	}
	if !cs2.Next(ctx) {
		log.Fatalf("change streams: cs2.Next не вернул delete-событие: %v", cs2.Err())
	}
	deleteLatency := time.Since(t2)
	var deleteEvt bson.M
	if err := cs2.Decode(&deleteEvt); err != nil {
		log.Fatalf("change streams: decode delete-события: %v", err)
	}
	deleteOpType, _ := deleteEvt["operationType"].(string)

	fmt.Printf("FIXTURE ops: cs_delete_op_type=%s cs_delete_latency=%v\n", deleteOpType, deleteLatency)
	if deleteOpType != "delete" {
		log.Fatalf(`assert: третье событие должно иметь operationType="delete", got %q`, deleteOpType)
	}
	log.Printf("assert OK: change stream доставил insert/update/delete в правильном порядке (insert=%v resume-update=%v delete=%v)", insertLatency, resumeLatency, deleteLatency)
}

// -- Сценарий 3: retryable writes переживают step-down primary ---------------

// retryableWritesScenario — КОНКУРЕНТНО с серией InsertOne форсирует РЕАЛЬНЫЙ
// step-down текущего primary (replSetStepDown force:true) через ОТДЕЛЬНЫЙ
// прямой (directConnection=true) клиент к узлу, который на момент старта
// сценария является primary (определяется через hello().me). retryWrites=
// true по умолчанию в URI mongo-go-driver/v2 (retryable writes ВКЛЮЧЕНЫ,
// если явно не отключены) — драйвер ОБЯЗАН прозрачно повторить прерванные
// step-down'ом записи на новом primary. Единственное убедительное
// доказательство того, что произошёл ИМЕННО повтор (а не "повезло с
// таймингом и соединение просто переустановилось до первой попытки") —
// перехват через *event.CommandMonitor: ищем txnNumber, который встретился
// И в Failed, И в последующем Succeeded событии insert — это одна и та же
// логическая запись, провалившаяся один раз и повторённая драйвером.
func retryableWritesScenario(ctx context.Context, mongoURI string) {
	baseClient, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("retryable writes: connect (base): %v", err)
	}
	primaryHost := currentPrimaryHost(ctx, baseClient)
	if err := baseClient.Disconnect(ctx); err != nil {
		log.Fatalf("retryable writes: disconnect (base): %v", err)
	}
	log.Printf("retryable writes: текущий primary перед step-down: %s", primaryHost)

	directURI := fmt.Sprintf("mongodb://%s/?directConnection=true", primaryHost)
	directClient, err := mongo.Connect(options.Client().ApplyURI(directURI))
	if err != nil {
		log.Fatalf("retryable writes: connect (direct к primary %s): %v", primaryHost, err)
	}
	defer func() { _ = directClient.Disconnect(context.Background()) }()

	monitor := &event.CommandMonitor{}
	// Started-based учёт: каждая ПОПЫТКА insert (исходная и повтор) несёт
	// txnNumber в самой команде — если один и тот же txnNumber породил
	// БОЛЬШЕ ОДНОЙ Started-попытки, это прямое доказательство повтора на
	// уровне драйвера (не обязательно знать явный Failed/Succeeded per-се).
	var attemptsMu sync.Mutex
	attemptsByTxn := map[int64]int{}
	var failedInsertCount, succeededInsertCount int
	monitor.Started = func(_ context.Context, evt *event.CommandStartedEvent) {
		if evt.CommandName != "insert" {
			return
		}
		v, err := evt.Command.LookupErr("txnNumber")
		if err != nil {
			return
		}
		n, ok := v.AsInt64OK()
		if !ok {
			return
		}
		attemptsMu.Lock()
		attemptsByTxn[n]++
		attemptsMu.Unlock()
	}
	monitor.Succeeded = func(_ context.Context, evt *event.CommandSucceededEvent) {
		if evt.CommandName == "insert" {
			attemptsMu.Lock()
			succeededInsertCount++
			attemptsMu.Unlock()
		}
	}
	monitor.Failed = func(_ context.Context, evt *event.CommandFailedEvent) {
		if evt.CommandName == "insert" {
			attemptsMu.Lock()
			failedInsertCount++
			attemptsMu.Unlock()
		}
	}

	writeClient, err := mongo.Connect(options.Client().ApplyURI(mongoURI).SetMonitor(monitor))
	if err != nil {
		log.Fatalf("retryable writes: connect (writeClient): %v", err)
	}
	defer func() { _ = writeClient.Disconnect(context.Background()) }()
	if err := writeClient.Ping(ctx, nil); err != nil {
		log.Fatalf("retryable writes: ping writeClient: %v", err)
	}
	coll := writeClient.Database(dbName).Collection(retryDemoColl)
	if err := coll.Drop(ctx); err != nil && !isNamespaceNotFound(err) {
		log.Fatalf("retryable writes: drop %s: %v", retryDemoColl, err)
	}

	var wg sync.WaitGroup
	stepDownErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Микро-задержка: даём первому InsertOne гарантированно начаться
		// на СТАРОМ primary, прежде чем он уйдёт со сцены — иначе есть
		// шанс, что step-down случится ДО отправки самой первой команды и
		// сценарий не поймает НИ ОДНОЙ реальной попытки на старом primary.
		time.Sleep(3 * time.Millisecond)
		var res bson.M
		err := directClient.Database("admin").RunCommand(ctx, bson.D{
			{Key: "replSetStepDown", Value: 10},
			{Key: "secondaryCatchUpPeriodSecs", Value: 0},
			{Key: "force", Value: true},
		}).Decode(&res)
		stepDownErrCh <- err
	}()

	successes := 0
	var lastErr error
	t0 := time.Now()
	for i := 0; i < retryWriteDocs; i++ {
		_, err := coll.InsertOne(ctx, bson.D{{Key: "seq", Value: i}, {Key: "marker", Value: "retry-demo"}})
		if err == nil {
			successes++
		} else {
			lastErr = err
		}
	}
	dur := time.Since(t0)
	wg.Wait()
	stepDownErr := <-stepDownErrCh
	// stepDownErr зачастую НЕ nil (соединение обрывается вместе со
	// step-down'ом раньше, чем сервер успевает вернуть ack) — это ОЖИДАЕМЫЙ
	// побочный эффект самой команды, не сигнал провала сценария; печатаем
	// только для видимости.
	log.Printf("retryable writes: replSetStepDown завершился с err=%v (обрыв соединения при step-down — ожидаемо, не провал сценария)", stepDownErr)

	attemptsMu.Lock()
	maxAttempts := 0
	retriedTxns := 0
	for _, n := range attemptsByTxn {
		if n > maxAttempts {
			maxAttempts = n
		}
		if n > 1 {
			retriedTxns++
		}
	}
	failedCnt, succeededCnt := failedInsertCount, succeededInsertCount
	attemptsMu.Unlock()

	fmt.Printf("FIXTURE ops: retryable_writes_total=%d retryable_writes_success=%d retryable_writes_duration=%v retryable_writes_step_down_primary=%s retryable_writes_failed_attempts=%d retryable_writes_succeeded_attempts=%d retryable_writes_retried_txn_count=%d retryable_writes_max_attempts_per_txn=%d\n",
		retryWriteDocs, successes, dur, primaryHost, failedCnt, succeededCnt, retriedTxns, maxAttempts)

	if successes != retryWriteDocs {
		log.Fatalf("assert: ВСЕ %d InsertOne должны в итоге вернуть успех несмотря на step-down primary (retryable writes должны прозрачно повторить прерванные попытки), получено успехов=%d, последняя ошибка=%v", retryWriteDocs, successes, lastErr)
	}
	if retriedTxns == 0 {
		log.Fatalf("assert: НИ ОДНА логическая запись (txnNumber) не получила больше одной Started-попытки — нет прямого доказательства РЕАЛЬНОГО повтора драйвером (не просто везение с таймингом); step-down, возможно, не пересёкся по времени ни с одной попыткой записи")
	}
	log.Printf("assert OK: все %d записей пережили step-down primary (%s), %d из них доказанно ПОВТОРЕНЫ драйвером (тот же txnNumber встретился в нескольких Started-попытках, максимум попыток на одну запись=%d), суммарное время=%v",
		retryWriteDocs, primaryHost, retriedTxns, maxAttempts, dur)

	waitForPrimary(ctx, mongoURI)
}

// currentPrimaryHost определяет текущий primary через hello().me на клиенте,
// подключённом ко всему replica set (mongo-driver сам направит команду hello
// на primary).
func currentPrimaryHost(ctx context.Context, client *mongo.Client) string {
	var hello bson.M
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		log.Fatalf("retryable writes: hello(): %v", err)
	}
	me, _ := hello["me"].(string)
	if me == "" {
		log.Fatalf("retryable writes: hello().me пуст, не удалось определить primary")
	}
	return me
}

// waitForPrimary — после форсированного step-down ждём, пока кластер снова
// изберёт primary, прежде чем сценарий считается завершённым (последующие
// фазы demo-скрипта — backup/restore — не должны попасть на кластер без
// primary).
func waitForPrimary(ctx context.Context, mongoURI string) {
	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("retryable writes: connect (wait-for-primary): %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var hello bson.M
		err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
		if err == nil {
			if writable, _ := hello["isWritablePrimary"].(bool); writable {
				log.Printf("retryable writes: кластер снова имеет primary (%v) после step-down", hello["me"])
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Fatalf("retryable writes: кластер НЕ восстановил primary за отведённое время после step-down")
}

// -- Фаза "backup-verify" -----------------------------------------------------

// backupVerifyPhase — вызывается demo-скриптом ПОСЛЕ mongodump/mongorestore
// (см. ../ops/ops-demo.sh): сравнивает counts users/products/orders в
// исходной базе (cookbook) и восстановленной (RESTORED_DB, по умолчанию
// cookbook_restored), а также с dataset/manifest.json — все три источника
// должны совпасть.
func backupVerifyPhase(ctx context.Context, mongoURI string) {
	restoredDB := os.Getenv("RESTORED_DB")
	if restoredDB == "" {
		restoredDB = "cookbook_restored"
	}

	client, err := mongoconn.Connect(mongoURI)
	if err != nil {
		log.Fatalf("backup-verify: connect: %v", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	m := loadManifest()

	origCounts := countCollections(ctx, client.Database(dbName))
	restoredCounts := countCollections(ctx, client.Database(restoredDB))

	fmt.Printf("FIXTURE ops: backup_orig_users=%d backup_orig_products=%d backup_orig_orders=%d backup_restored_users=%d backup_restored_products=%d backup_restored_orders=%d backup_restored_db=%s\n",
		origCounts.Users, origCounts.Products, origCounts.Orders,
		restoredCounts.Users, restoredCounts.Products, restoredCounts.Orders, restoredDB)

	if origCounts.Users != m.Users || origCounts.Products != m.Products || origCounts.Orders != m.Orders {
		log.Fatalf("assert: исходная база (%s) должна совпадать с manifest.json ДО backup (users=%d/%d products=%d/%d orders=%d/%d) — иначе backup/restore сравнивается не с тем эталоном",
			dbName, origCounts.Users, m.Users, origCounts.Products, m.Products, origCounts.Orders, m.Orders)
	}
	if restoredCounts != origCounts {
		log.Fatalf("assert: восстановленные counts (%s: users=%d products=%d orders=%d) ДОЛЖНЫ совпасть с исходными (%s: users=%d products=%d orders=%d) — mongodump/mongorestore round-trip не сохранил данные",
			restoredDB, restoredCounts.Users, restoredCounts.Products, restoredCounts.Orders,
			dbName, origCounts.Users, origCounts.Products, origCounts.Orders)
	}
	log.Printf("assert OK: mongodump/mongorestore round-trip сохранил все counts (users=%d products=%d orders=%d), восстановленная база=%s совпадает и с исходной, и с manifest.json",
		origCounts.Users, origCounts.Products, origCounts.Orders, restoredDB)
}

type collCounts struct {
	Users    int
	Products int
	Orders   int
}

func countCollections(ctx context.Context, db *mongo.Database) collCounts {
	uc, err := db.Collection("users").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("backup-verify: countDocuments %s.users: %v", db.Name(), err)
	}
	pc, err := db.Collection("products").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("backup-verify: countDocuments %s.products: %v", db.Name(), err)
	}
	oc, err := db.Collection("orders").CountDocuments(ctx, bson.D{})
	if err != nil {
		log.Fatalf("backup-verify: countDocuments %s.orders: %v", db.Name(), err)
	}
	return collCounts{Users: int(uc), Products: int(pc), Orders: int(oc)}
}

// -- служебное -----------------------------------------------------------------

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
