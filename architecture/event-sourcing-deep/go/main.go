// loadgen стенда event-sourcing-deep.
//
// Считает не «сколько выжали», а долю конфликтов версий и распределение
// задержек. Каждый воркер держит своё соединение на всю команду, включая
// повторы, — иначе повтор уйдёт на другое соединение и замер поедет.
//
// -mode задаёт РЕАЛЬНУЮ диспетчеризацию, а не просто метку в выводе:
// каждый режим ведёт в свою функцию, неизвестный режим — это die(), а не
// тихий фолбэк на поведение по умолчанию. Опечатка в имени режима должна
// падать, а не давать правдоподобный, но не тот замер.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kurrent-io/KurrentDB-Client-Go/kurrentdb"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isDeadlineOrCanceled — обрыв по истечению времени прогона, исход
// НЕИЗВЕСТЕН. У KurrentDB такой обрыв на границе дедлайна иногда приходит
// НЕ как context.DeadlineExceeded (errors.Is не срабатывает), а как «сырая»
// gRPC-ошибка со статусом DeadlineExceeded/Canceled — клиент не всегда
// успевает завернуть её в *kurrentdb.Error. Проверяем оба пути, иначе часть
// обрывов на границе дедлайна ошибочно попадает в errors, а не в
// deadline_or_unknown.
func isDeadlineOrCanceled(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	code := status.Code(err)
	return code == codes.DeadlineExceeded || code == codes.Canceled
}

type Result struct {
	Mode      string  `json:"mode"`
	Workers   int     `json:"workers"`
	Ops       int64   `json:"ops"`
	Errors    int64   `json:"errors"`
	Conflicts int64   `json:"conflicts"`
	P50ms     float64 `json:"p50_ms"`
	P95ms     float64 `json:"p95_ms"`
	P99ms     float64 `json:"p99_ms"`
	// Оборванные истечением времени прогона: исход НЕИЗВЕСТЕН — часть могла
	// зафиксироваться, не дойдя ответом до клиента. Классифицируем по ошибке
	// через errors.Is, а не по факту «контекст уже истёк».
	DeadlineOrUnknown int64 `json:"deadline_or_unknown"`
}

// EngineTiming — результат точечного чтения ОДНОГО агрегата у одного
// движка.
//
// RecordsScanned — ЧЕСТНО измерено только у Kafka (ReadStreamKafka считает
// каждую запись, прошедшую через consumeAllAvailable, включая чужие ключи,
// до фильтра по streamID — см. go/kafkastore.go). У Postgres и KurrentDB
// сервер отбирает событие по stream_id/streamID НА СВОЕЙ СТОРОНЕ (запрос
// `WHERE stream_id = $1` с индексом у Postgres, чтение конкретного стрима у
// KurrentDB) — клиентский код здесь принципиально не видит отброшенных
// строк, потому что сервер их и не присылает. Поэтому для Postgres и
// KurrentDB RecordsScanned = -1 (сентинел «не измерено на этой стороне»),
// а не подставное значение, равное RecordsMatched: подставлять сюда
// RecordsMatched выглядело бы как измерение «просмотрено = подошло», хотя
// это НЕ было измерено — это следствие устройства запроса, не результат
// подсчёта. RecordsMatched у всех трёх — реальное число событий агрегата.
type EngineTiming struct {
	ElapsedMs      float64 `json:"elapsed_ms"`
	RecordsScanned int     `json:"records_scanned"`
	RecordsMatched int     `json:"records_matched"`
	BalanceLiters  float64 `json:"balance_liters"`
}

type PointReadResult struct {
	Mode     string       `json:"mode"`
	Stream   string       `json:"stream"`
	Postgres EngineTiming `json:"postgres"`
	Kurrent  EngineTiming `json:"kurrent"`
	Kafka    EngineTiming `json:"kafka"`
}

// CatchupTiming — цена «догона» истории (CatchupMs/CatchupEvents) и цена
// перехода в реальное время (LiveDetectMs: сколько прошло от подтверждения
// записи ОДНОГО нового события до его наблюдения читателем/подпиской).
// PrefillEvents — сколько событий было записано ДО замера догона; величина
// фиксирована в коде (catchupPrefill), а не подобрана под красивое число.
type CatchupTiming struct {
	PrefillEvents int     `json:"prefill_events"`
	CatchupMs     float64 `json:"catchup_ms"`
	CatchupEvents int     `json:"catchup_events"`
	LiveDetectMs  float64 `json:"live_detect_ms"`
}

type CatchupResult struct {
	Mode     string        `json:"mode"`
	Postgres CatchupTiming `json:"postgres"`
	Kurrent  CatchupTiming `json:"kurrent"`
	Kafka    CatchupTiming `json:"kafka"`
}

// catchupPrefill — сколько исторических событий пишем перед замером догона.
// Фиксировано в коде: число одно и то же для всех трёх движков в одном
// запуске -mode catchup-compare.
const catchupPrefill = 500

func main() {
	dsn := flag.String("dsn", os.Getenv("PG_DSN"), "строка подключения к Postgres")
	esdbURL := flag.String("esdb", os.Getenv("ESDB_URL"), "строка подключения к KurrentDB")
	kafkaBroker := flag.String("kafka", os.Getenv("KAFKA_BROKER"), "адрес брокера Kafka")
	mode := flag.String("mode", "", "postgres-append | kurrent-append | kafka-append | "+
		"point-read-compare | catchup-compare | append-coarse | append-fine | project | "+
		"versioning-read | assert-fresh | rebuild-bluegreen")
	workers := flag.Int("workers", 8, "число воркеров (для *-append)")
	streams := flag.Int("streams", 1, "сколько разных стримов делят воркеры (append и point-read-compare)")
	duration := flag.Duration("duration", 30*time.Second, "длительность прогона")
	// streamPrefix — префикс имени стрима: streamID = prefix + индекс
	// (0..streams-1). По умолчанию "tank-" — обратная совместимость с
	// append-coarse/append-fine (профиль 02) и project (профиль 03), которые
	// сами не передают этот флаг и полагаются на TRUNCATE перед прогоном, а
	// не на уникальность имени. Профиль 01 передаёт СВОЙ уникальный префикс
	// на КАЖДЫЙ запуск скрипта — см. -mode assert-fresh и комментарий в
	// scripts/01-store.sh, «равный старт».
	streamPrefix := flag.String("stream-prefix", "tank-", "префикс имени стрима (streamID = prefix + индекс)")
	// topic — топик Kafka для kafka-append/point-read-compare. По умолчанию
	// kafkaEventsTopic ("es-events") — общий на все прогоны, которые не
	// передают -topic явно. Профиль 01 передаёт уникальный топик на каждый
	// запуск скрипта по той же причине, что и -stream-prefix.
	topic := flag.String("topic", kafkaEventsTopic, "топик Kafka (kafka-append, point-read-compare, assert-fresh)")
	flag.Parse()

	switch *mode {
	case "postgres-append":
		runPostgresAppend(*dsn, *workers, *streams, *duration, *mode, *streamPrefix)
	// append-coarse/append-fine — режимы задачи 2 (профиль 02-design.sh):
	// поведение всегда было постгресовым, менялось только число стримов
	// через -streams. Сохраняем совместимость: они и раньше были только
	// меткой в выводе, но должны продолжать РАБОТАТЬ после появления
	// настоящей диспетчеризации, иначе профиль 02 перестанет собираться.
	case "append-coarse", "append-fine":
		runPostgresAppend(*dsn, *workers, *streams, *duration, *mode, *streamPrefix)
	case "kurrent-append":
		runKurrentAppend(*esdbURL, *workers, *streams, *duration, *streamPrefix)
	case "kafka-append":
		runKafkaAppend(*kafkaBroker, *workers, *streams, *duration, *streamPrefix, *topic)
	case "point-read-compare":
		runPointReadCompare(*dsn, *esdbURL, *kafkaBroker, *duration, *streamPrefix, *topic)
	case "catchup-compare":
		runCatchupCompare(*dsn, *esdbURL, *kafkaBroker, *duration)
	case "project":
		runProject(*dsn, *duration)
	case "versioning-read":
		runVersioningRead(*dsn)
	case "assert-fresh":
		runAssertFresh(*dsn, *esdbURL, *kafkaBroker, *streamPrefix, *topic, *streams)
	case "rebuild-bluegreen":
		runRebuildBlueGreen(*dsn, *workers, *streams, *duration, *streamPrefix)
	default:
		die("неизвестный режим -mode=%q; ожидается один из: postgres-append, "+
			"kurrent-append, kafka-append, point-read-compare, catchup-compare, "+
			"append-coarse, append-fine, project, versioning-read, assert-fresh, "+
			"rebuild-bluegreen", *mode)
	}
}

// ===== A. append с ожидаемой версией =====

func runPostgresAppend(dsn string, workers, streams int, duration time.Duration, modeLabel, streamPrefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		die("разбор dsn: %v", err)
	}
	cfg.MaxConns = int32(workers)
	cfg.MinConns = int32(workers)

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		die("пул: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		die("Postgres не отвечает: %v", err)
	}

	// Регистрируем стримы заранее, чтобы замер не мерил регистрацию.
	for s := 0; s < streams; s++ {
		id := fmt.Sprintf("%s%d", streamPrefix, s)
		if err := AppendEvents(context.Background(), pool, id, 0,
			[]Event{{Type: "TankRegistered"}, {Type: "OxygenAdded", Liters: 1e9}}); err != nil &&
			!errors.Is(err, ErrVersionConflict) {
			die("подготовка стрима %s: %v", id, err)
		}
	}

	var (
		mu       sync.Mutex
		lat      []float64
		ops      int64
		errCnt   int64
		conflict int64
		deadline int64
		wg       sync.WaitGroup
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			for ctx.Err() == nil {
				streamID := fmt.Sprintf("%s%d", streamPrefix, rnd.Intn(streams))
				start := time.Now()
				err := oneCommandPostgres(ctx, pool, streamID)
				elapsed := time.Since(start).Seconds() * 1000

				mu.Lock()
				switch {
				case err == nil:
					ops++
					lat = append(lat, elapsed)
				case errors.Is(err, ErrVersionConflict):
					conflict++
				case isDeadlineOrCanceled(err):
					deadline++
				default:
					errCnt++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	sort.Float64s(lat)
	out := Result{
		Mode: modeLabel, Workers: workers, Ops: ops, Errors: errCnt,
		Conflicts: conflict, DeadlineOrUnknown: deadline,
		P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95), P99ms: pct(lat, 0.99),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
	if ops == 0 {
		die("ноль успешных операций — мерить нечего")
	}
}

// oneCommandPostgres — прочитать стрим, решить, записать с ожидаемой версией.
// Ретраев здесь НЕТ намеренно: конфликт версии — это предмет замера,
// и гасить его повтором значило бы прятать измеряемое явление.
func oneCommandPostgres(ctx context.Context, pool *pgxpool.Pool, streamID string) error {
	tank, version, err := LoadStream(ctx, pool, streamID)
	if err != nil {
		return err
	}
	evs, err := tank.Decide(Command{Type: "ConsumeOxygen", Liters: 1})
	if err != nil {
		return err
	}
	return AppendEvents(ctx, pool, streamID, version, evs)
}

func runKurrentAppend(esdbURL string, workers, streams int, duration time.Duration, streamPrefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	client, err := NewKurrentClient(esdbURL)
	if err != nil {
		die("KurrentDB: разбор строки подключения: %v", err)
	}
	defer client.Close()

	for s := 0; s < streams; s++ {
		id := fmt.Sprintf("%s%d", streamPrefix, s)
		if err := AppendKurrent(context.Background(), client, id, -1,
			[]Event{{Type: "TankRegistered"}, {Type: "OxygenAdded", Liters: 1e9}}); err != nil &&
			!errors.Is(err, ErrVersionConflict) {
			die("KurrentDB: подготовка стрима %s: %v", id, err)
		}
	}

	var (
		mu       sync.Mutex
		lat      []float64
		ops      int64
		errCnt   int64
		conflict int64
		deadline int64
		wg       sync.WaitGroup
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			for ctx.Err() == nil {
				streamID := fmt.Sprintf("%s%d", streamPrefix, rnd.Intn(streams))
				start := time.Now()
				err := oneCommandKurrent(ctx, client, streamID)
				elapsed := time.Since(start).Seconds() * 1000

				mu.Lock()
				switch {
				case err == nil:
					ops++
					lat = append(lat, elapsed)
				case errors.Is(err, ErrVersionConflict):
					conflict++
				case isDeadlineOrCanceled(err):
					deadline++
				default:
					errCnt++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	sort.Float64s(lat)
	out := Result{
		Mode: "kurrent-append", Workers: workers, Ops: ops, Errors: errCnt,
		Conflicts: conflict, DeadlineOrUnknown: deadline,
		P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95), P99ms: pct(lat, 0.99),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
	if ops == 0 {
		die("ноль успешных операций — мерить нечего")
	}
}

func oneCommandKurrent(ctx context.Context, c *kurrentdb.Client, streamID string) error {
	tank, version, err := LoadKurrent(ctx, c, streamID)
	if err != nil {
		return err
	}
	evs, err := tank.Decide(Command{Type: "ConsumeOxygen", Liters: 1})
	if err != nil {
		return err
	}
	return AppendKurrent(ctx, c, streamID, version, evs)
}

// runKafkaAppend — попытка того же сценария на Kafka. Никакого
// read-decide-write здесь НЕТ: producer API не даёт условной записи по
// версии ключа, поэтому и читать перед записью незачем — сверяться не с
// чем. Воркеры просто шлют события вслепую; Conflicts всегда 0 — это не
// «конфликтов не было», а «эта операция здесь отсутствует».
func runKafkaAppend(brokers string, workers, streams int, duration time.Duration, streamPrefix, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	cl, err := NewKafkaProducerClient(brokers, topic)
	if err != nil {
		die("Kafka: %v", err)
	}
	defer cl.Close()

	var (
		mu       sync.Mutex
		lat      []float64
		ops      int64
		errCnt   int64
		deadline int64
		wg       sync.WaitGroup
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			for ctx.Err() == nil {
				streamID := fmt.Sprintf("%s%d", streamPrefix, rnd.Intn(streams))
				start := time.Now()
				err := AppendKafka(ctx, cl, topic, streamID, Event{Type: "OxygenAdded", Liters: 1})
				elapsed := time.Since(start).Seconds() * 1000

				mu.Lock()
				switch {
				case err == nil:
					ops++
					lat = append(lat, elapsed)
				case isDeadlineOrCanceled(err):
					deadline++
				default:
					errCnt++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	sort.Float64s(lat)
	out := Result{
		Mode: "kafka-append", Workers: workers, Ops: ops, Errors: errCnt,
		Conflicts: 0, DeadlineOrUnknown: deadline,
		P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95), P99ms: pct(lat, 0.99),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
	if ops == 0 {
		die("ноль успешных операций — мерить нечего")
	}
}

// runAssertFresh — явная проверка «равного старта» перед раундом 1
// профиля 01: история представительного стрима (streamPrefix+"0") у
// Postgres и KurrentDB и топика Kafka обязана быть ПУСТОЙ на момент этой
// проверки. Раньше «равный старт» был ПРЕМИСОЙ (заявленной в комментарии
// 01-store.sh), а не проверенным фактом: KurrentDB и Kafka не сбрасывались
// вообще (Postgres сбрасывался TRUNCATE, но и это защищало только от чужой
// истории, не от повторного запуска того же скрипта). Арифметика самих
// фикстур это опровергала: при равномерном распределении по 64 стримам на
// streamPrefix+"0" должно приходиться примерно ops/64, а KurrentDB и Kafka
// показывали избыток в 23-33% — история от прошлых прогонов накапливалась
// молча. Исправление в две части: (1) 01-store.sh теперь генерирует
// уникальный -stream-prefix/-topic на КАЖДЫЙ собственный запуск (см. также
// -mode catchup-compare, где такая уникальность уже была с самого начала);
// (2) эта функция ПРОВЕРЯЕТ пустоту делом, а не полагается только на
// уникальность имени — если где-то в цепочке (коллизия имени, ошибка в
// скрипте, повторный запуск с тем же -stream-prefix вручную) уникальность
// не сработала, -mode assert-fresh обязан упасть, а не молча продолжить.
//
// Проверяется ОДИН представительный стрим (streamPrefix+"0" у Postgres и
// KurrentDB), не все streams — при криптографически различающемся префиксе
// (наносекундный таймстамп) вероятность того, что ИМЕННО этот стрим занят,
// а остальные 63 нет, равна вероятности того, что префикс НЕ уникален
// вовсе (коллизия таймстампа задевает весь префикс, а не отдельный индекс
// внутри него) — проверка одного представителя эквивалентна проверке всех
// по сути защищаемого свойства (уникальность префикса), не полноте
// перебора. У Kafka проверяется топик целиком (partition-уровня ключа там
// нет смысла выделять — вся история топика идёт в одну проверку).
func runAssertFresh(dsn, esdbURL, kafkaBroker, streamPrefix, topic string, streams int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	target := streamPrefix + "0"

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		die("assert-fresh: Postgres: %v", err)
	}
	defer pool.Close()
	var pgCount int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE stream_id LIKE $1`, streamPrefix+"%").Scan(&pgCount); err != nil {
		die("assert-fresh: Postgres: подсчёт истории префикса %q: %v", streamPrefix, err)
	}
	if pgCount != 0 {
		die("assert-fresh: Postgres: у префикса %q уже есть история (%d строк) — равный старт нарушен, "+
			"это НЕ новый уникальный прогон", streamPrefix, pgCount)
	}

	esClient, err := NewKurrentClient(esdbURL)
	if err != nil {
		die("assert-fresh: KurrentDB: %v", err)
	}
	defer esClient.Close()
	_, esVersion, err := LoadKurrent(ctx, esClient, target)
	if err != nil {
		die("assert-fresh: KurrentDB: чтение стрима %q: %v", target, err)
	}
	if esVersion != -1 {
		die("assert-fresh: KurrentDB: стрим %q уже существует (ревизия %d) — равный старт нарушен", target, esVersion)
	}

	kafkaClient, err := NewKafkaConsumerClient(kafkaBroker, topic)
	if err != nil {
		die("assert-fresh: Kafka: %v", err)
	}
	defer kafkaClient.Close()
	records, _, err := consumeAllAvailable(ctx, kafkaClient)
	if err != nil {
		die("assert-fresh: Kafka: чтение топика %q: %v", topic, err)
	}
	if len(records) != 0 {
		die("assert-fresh: Kafka: топик %q уже содержит %d записей — равный старт нарушен", topic, len(records))
	}

	fmt.Fprintf(os.Stderr, "loadgen: assert-fresh: OK — стрим %q (Postgres/KurrentDB) и топик %q (Kafka) пусты, равный старт подтверждён\n", target, topic)
}

// ===== B. точечное чтение одного агрегата =====

func runPointReadCompare(dsn, esdbURL, kafkaBroker string, duration time.Duration, streamPrefix, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	target := streamPrefix + "0"

	pgPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		die("Postgres: %v", err)
	}
	defer pgPool.Close()
	t0 := time.Now()
	tank, version, err := LoadStream(ctx, pgPool, target)
	pgElapsed := time.Since(t0)
	if err != nil {
		die("Postgres point-read: %v", err)
	}
	pgEvents := int(version)
	// RecordsScanned: -1 — не измерено (не подставлено равным RecordsMatched,
	// см. комментарий EngineTiming выше). `SELECT ... WHERE stream_id = $1`
	// отбирает строки на стороне Postgres — клиентский код не видит того,
	// что сервер отбросил до отправки.
	pgResult := EngineTiming{ElapsedMs: ms(pgElapsed), RecordsScanned: -1, RecordsMatched: pgEvents, BalanceLiters: tank.Liters}

	esClient, err := NewKurrentClient(esdbURL)
	if err != nil {
		die("KurrentDB: %v", err)
	}
	defer esClient.Close()
	t1 := time.Now()
	esTank, esVersion, err := LoadKurrent(ctx, esClient, target)
	esElapsed := time.Since(t1)
	if err != nil {
		die("KurrentDB point-read: %v", err)
	}
	esEvents := 0
	if esVersion >= 0 {
		esEvents = int(esVersion) + 1
	}
	// RecordsScanned: -1 — не измерено, той же причине, что у Postgres:
	// KurrentDB отбирает события конкретного стрима на своей стороне.
	esResult := EngineTiming{ElapsedMs: ms(esElapsed), RecordsScanned: -1, RecordsMatched: esEvents, BalanceLiters: esTank.Liters}

	kafkaClient, err := NewKafkaConsumerClient(kafkaBroker, topic)
	if err != nil {
		die("Kafka: %v", err)
	}
	defer kafkaClient.Close()
	kTank, scanned, matched, kElapsed, err := ReadStreamKafka(ctx, kafkaClient, target)
	if err != nil {
		die("Kafka point-read: %v", err)
	}
	kResult := EngineTiming{ElapsedMs: ms(kElapsed), RecordsScanned: scanned, RecordsMatched: matched, BalanceLiters: kTank.Liters}

	out := PointReadResult{Mode: "point-read-compare", Stream: target, Postgres: pgResult, Kurrent: esResult, Kafka: kResult}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
}

// ===== C. catch-up-подписка =====

func runCatchupCompare(dsn, esdbURL, kafkaBroker string, duration time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	// Уникальное имя стрима/топика на каждый запуск — снимает необходимость
	// чистить состояние вручную перед замером: движок гарантированно видит
	// только то, что мы сами сейчас туда напишем.
	runID := time.Now().UnixNano()

	pgTiming, err := catchupPostgres(ctx, dsn, fmt.Sprintf("catchup-demo-pg-%d", runID))
	if err != nil {
		die("Postgres catchup: %v", err)
	}
	esTiming, err := catchupKurrent(ctx, esdbURL, fmt.Sprintf("catchup-demo-es-%d", runID))
	if err != nil {
		die("KurrentDB catchup: %v", err)
	}
	kTiming, err := catchupKafka(ctx, kafkaBroker, fmt.Sprintf("es-catchup-demo-%d", runID))
	if err != nil {
		die("Kafka catchup: %v", err)
	}

	out := CatchupResult{Mode: "catchup-compare", Postgres: pgTiming, Kurrent: esTiming, Kafka: kTiming}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
}

// catchupPostgres — у Postgres нет нативной push-подписки. «Догон» — это
// обычное чтение стрима; «переход в реальное время» приходится строить
// поллингом. Первый SELECT идёт сразу после подтверждения записи, пауза 2 мс —
// только если событие ещё не видно; поэтому live_detect_ms здесь — по сути
// чтение своей записи одним запросом, а не задержка независимой подписки.
func catchupPostgres(ctx context.Context, dsn, streamID string) (CatchupTiming, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return CatchupTiming{}, err
	}
	defer pool.Close()

	for i := 0; i < catchupPrefill; i++ {
		if err := AppendEvents(ctx, pool, streamID, int64(i), []Event{{Type: "OxygenAdded", Liters: 1}}); err != nil {
			return CatchupTiming{}, fmt.Errorf("предзаполнение: %w", err)
		}
	}

	t0 := time.Now()
	_, version, err := LoadStream(ctx, pool, streamID)
	catchupElapsed := time.Since(t0)
	if err != nil {
		return CatchupTiming{}, fmt.Errorf("догон: %w", err)
	}

	if err := AppendEvents(ctx, pool, streamID, version, []Event{{Type: "OxygenAdded", Liters: 1}}); err != nil {
		return CatchupTiming{}, fmt.Errorf("живая запись: %w", err)
	}
	appendReturned := time.Now()

	target := version + 1
	const pollEvery = 2 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return CatchupTiming{}, fmt.Errorf("поллинг живого события не дождался за отведённое время: %w", ctx.Err())
		}
		var seen int64
		row := pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(version), 0) FROM events WHERE stream_id = $1`, streamID)
		if err := row.Scan(&seen); err != nil {
			return CatchupTiming{}, err
		}
		if seen >= target {
			break
		}
		time.Sleep(pollEvery)
	}
	liveElapsed := time.Since(appendReturned)

	return CatchupTiming{
		PrefillEvents: catchupPrefill,
		CatchupMs:     ms(catchupElapsed),
		CatchupEvents: int(version),
		LiveDetectMs:  ms(liveElapsed),
	}, nil
}

// catchupKurrent — нативная catch-up-подписка: сервер сам присылает
// событие CaughtUp, когда переходит с чтения истории на реальное время.
func catchupKurrent(ctx context.Context, esdbURL, streamID string) (CatchupTiming, error) {
	client, err := NewKurrentClient(esdbURL)
	if err != nil {
		return CatchupTiming{}, err
	}
	defer client.Close()

	for i := 0; i < catchupPrefill; i++ {
		if err := AppendKurrent(ctx, client, streamID, int64(i)-1, []Event{{Type: "OxygenAdded", Liters: 1}}); err != nil {
			return CatchupTiming{}, fmt.Errorf("предзаполнение: %w", err)
		}
	}

	t0 := time.Now()
	sub, err := client.SubscribeToStream(ctx, streamID, kurrentdb.SubscribeToStreamOptions{From: kurrentdb.Start{}})
	if err != nil {
		return CatchupTiming{}, fmt.Errorf("подписка: %w", err)
	}
	defer sub.Close()

	var catchupElapsed time.Duration
	eventsSeen := 0
	caughtUp := false
	for !caughtUp {
		evt := sub.Recv()
		switch {
		case evt.SubscriptionDropped != nil:
			return CatchupTiming{}, fmt.Errorf("подписка оборвалась во время догона: %w", evt.SubscriptionDropped.Error)
		case evt.EventAppeared != nil:
			eventsSeen++
		case evt.CaughtUp != nil:
			catchupElapsed = time.Since(t0)
			caughtUp = true
		}
	}

	if err := AppendKurrent(ctx, client, streamID, int64(catchupPrefill)-1, []Event{{Type: "OxygenAdded", Liters: 1}}); err != nil {
		return CatchupTiming{}, fmt.Errorf("живая запись: %w", err)
	}
	appendReturned := time.Now()

	for {
		evt := sub.Recv()
		if evt.SubscriptionDropped != nil {
			return CatchupTiming{}, fmt.Errorf("подписка оборвалась во время ожидания живого события: %w", evt.SubscriptionDropped.Error)
		}
		if evt.EventAppeared != nil {
			break
		}
	}
	liveElapsed := time.Since(appendReturned)

	return CatchupTiming{
		PrefillEvents: catchupPrefill,
		CatchupMs:     ms(catchupElapsed),
		CatchupEvents: eventsSeen,
		LiveDetectMs:  ms(liveElapsed),
	}, nil
}

// catchupKafka — у Kafka нет понятия «подписка на стрим», только консьюмер
// топика/партиции. «Догон» строим через consumeAllAvailable (короткие
// опросы до тишины), «реальное время» — через consumeOne (блокирующее
// ожидание следующей записи).
func catchupKafka(ctx context.Context, brokers, topic string) (CatchupTiming, error) {
	producer, err := NewKafkaProducerClient(brokers, topic)
	if err != nil {
		return CatchupTiming{}, err
	}
	defer producer.Close()

	for i := 0; i < catchupPrefill; i++ {
		payload, _ := json.Marshal(Event{Type: "OxygenAdded", Liters: 1})
		rec := &kgo.Record{Topic: topic, Key: []byte("catchup"), Value: payload}
		if err := producer.ProduceSync(ctx, rec).FirstErr(); err != nil {
			return CatchupTiming{}, fmt.Errorf("предзаполнение: %w", err)
		}
	}

	consumer, err := NewKafkaConsumerClient(brokers, topic)
	if err != nil {
		return CatchupTiming{}, err
	}
	defer consumer.Close()

	records, catchupElapsed, err := consumeAllAvailable(ctx, consumer)
	if err != nil {
		return CatchupTiming{}, fmt.Errorf("догон: %w", err)
	}

	payload, _ := json.Marshal(Event{Type: "OxygenAdded", Liters: 1})
	rec := &kgo.Record{Topic: topic, Key: []byte("catchup"), Value: payload}
	if err := producer.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return CatchupTiming{}, fmt.Errorf("живая запись: %w", err)
	}
	appendReturned := time.Now()

	if _, err := consumeOne(ctx, consumer); err != nil {
		return CatchupTiming{}, fmt.Errorf("ожидание живой записи: %w", err)
	}
	liveElapsed := time.Since(appendReturned)

	return CatchupTiming{
		PrefillEvents: catchupPrefill,
		CatchupMs:     ms(catchupElapsed),
		CatchupEvents: len(records),
		LiveDetectMs:  ms(liveElapsed),
	}, nil
}

// ===== D. проектор с чекпоинтом =====

// projectorName — единственный проектор в этом стенде: свёртка баланса
// по всем стримам. Задаче 6 нужен ровно один, поэтому имя не вынесено во флаг.
const projectorName = "balances"

// projectBatchLimit — размер пачки за одну транзакцию ProjectBatch.
// Фиксирован в коде: чем крупнее пачка, тем реже сдвигается чекпоинт и тем
// грубее «середина» обрыва по числу событий (но не по числу транзакций).
const projectBatchLimit = 500

// runProject крутит ProjectBatch в цикле, пока не истечёт duration или пока
// пачка не станет пустой (события кончились). Каждый вызов ProjectBatch
// получает СВОЙ context.Background(), а не общий ctx с таймаутом: это
// осознанный выбор, а не недосмотр. Транзакция внутри ProjectBatch либо
// целиком коммитится, либо целиком откатывается — Postgres не умеет
// закоммитить её наполовину, и обрывать её посреди работы средствами Go
// нечем, не убивая сам процесс. Поэтому "обрыв на середине" здесь получается
// НЕ разрывом одной транзакции, а тем, что общий -duration истекает МЕЖДУ
// вызовами ProjectBatch: часть пачек успевает закоммититься, часть —
// вообще не начаться. Это честно проверяет то, что заявлено в задаче
// (чекпоинт и применение в одной транзакции переживают обрыв процесса без
// потерь и дублей), но НЕ проверяет обрыв ВНУТРИ одной пачки — такой обрыв
// проверяется не логикой Go, а гарантией атомарности транзакции Postgres,
// и это разные вещи.
func runProject(dsn string, duration time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		die("пул: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		die("Postgres не отвечает: %v", err)
	}

	var (
		total int64
		lat   []float64
	)
	for ctx.Err() == nil {
		start := time.Now()
		n, err := ProjectBatch(context.Background(), pool, projectorName, projectBatchLimit)
		if err != nil {
			die("ProjectBatch: %v", err)
		}
		elapsed := time.Since(start).Seconds() * 1000
		if n == 0 {
			break // события кончились — крутить дальше нечего
		}
		total += int64(n)
		lat = append(lat, elapsed)
	}

	sort.Float64s(lat)
	out := Result{
		Mode: "project", Workers: 1, Ops: total,
		P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95), P99ms: pct(lat, 0.99),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
}

// ===== E. цена upcasting на чтении =====

// versioningStreamID — единственный стрим профиля 04. Скрипт наполняет его
// заранее (SQL, не через AppendEvents) либо целиком в форме v3 (schema_ver=3,
// upcasting не нужен), либо целиком в форме v1 (schema_ver=1, каждое событие
// проходит два шага цепочки). Стрим один и тот же — вариант задаётся тем,
// ЧЕМ он наполнен перед запуском, а не числом стримов или воркеров.
const versioningStreamID = "tank"

// versioningExpectedUnit — единица измерения, которую скрипт кладёт в КАЖДОЕ
// засеянное событие (см. reset_and_seed_v3/reset_and_seed_v1 в
// scripts/04-versioning.sh). Постусловие сверяет Unit по ЗНАЧЕНИЮ с этой
// константой, а не по факту "поле непустое": пустая строка или любая другая
// непустая строка одинаково прошли бы проверку "не пусто", но означали бы
// испорченное значение.
const versioningExpectedUnit = "liters"

// VersioningResult — итог одного прогона versioning-read. SumAmount — свёртка
// (сумма Amount по всем событиям стрима после Upcast) и BadUnitEvents —
// число событий, чей Unit после Upcast НЕ равен versioningExpectedUnit —
// вместе составляют постусловие профиля 04: свёртка суммы и свёртка
// единицы измерения v3-стрима и v1-стрима обязаны совпасть с ожидаемым
// значением, иначе upcasting меняет смысл события, а не просто стоит
// времени. Одной только SumAmount недостаточно: шаг v1->v2 мог бы
// подставлять в Unit пустую строку или произвольный мусор вместо "liters",
// и сумма Amount от этого не изменилась бы — расхождение осталось бы
// незамеченным. FetchMs — сеть/Postgres (одинаково дорого в обоих
// вариантах, интереса не представляет). UpcastMs — то, что мы на самом деле
// меряем: цикл Upcast над УЖЕ выгруженными в память строками, без единого
// обращения к сети внутри замеряемого участка.
type VersioningResult struct {
	Mode          string  `json:"mode"`
	Stream        string  `json:"stream"`
	Events        int     `json:"events"`
	SumAmount     float64 `json:"sum_amount"`
	BadUnitEvents int     `json:"bad_unit_events"`
	FetchMs       float64 `json:"fetch_ms"`
	UpcastMs      float64 `json:"upcast_ms"`
}

// runVersioningRead выгружает стрим целиком в память (это FetchMs — сетевой
// круговорот к Postgres, у v3- и v1-стрима он одной природы и не является
// предметом замера), а ЗАТЕМ отдельным замером (UpcastMs) поднимает КАЖДОЕ
// событие до v3 через Upcast(payload, schemaVer), где schemaVer — то, что
// реально лежит в колонке schema_ver этой строки, а не предположение.
// Разделение обязательно: если мерить читай-и-поднимай одним таймером,
// сетевой круговорот (сотни мс на 200000 строк) забивает собой разницу в
// работе Upcast (десятки-сотни МКС операций над map) — так и было в первой
// версии замера, отношение v1/v3 гуляло 0.95-1.10 и один раз показало v1
// быстрее v3. Это НЕ бессмысленно, а объясняется конкурирующим эффектом:
// v1-payload короче v3-payload на одно поле (см. scripts/04-versioning.sh,
// «Граница вывода»), и экономия на разборе более короткой строки JSON может
// перекрывать две дополнительные map-операции цепочки. Изолированный
// микробенчмарк ниже (BenchmarkUpcastV1Chain/BenchmarkUpcastV3NoOp,
// go/upcast_test.go) убирает этот конкурирующий эффект фиксированной длиной
// входа и показывает цепочку дороже по аллокациям устойчиво.
func runVersioningRead(dsn string) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		die("пул: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		die("Postgres не отвечает: %v", err)
	}

	t0 := time.Now()
	rows, err := pool.Query(ctx,
		`SELECT schema_ver, data FROM events WHERE stream_id = $1 ORDER BY version`,
		versioningStreamID)
	if err != nil {
		die("versioning-read: запрос: %v", err)
	}

	type raw struct {
		schemaVer int
		payload   []byte
	}
	var batch []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.schemaVer, &r.payload); err != nil {
			rows.Close()
			die("versioning-read: скан строки: %v", err)
		}
		batch = append(batch, r)
	}
	fetchErr := rows.Err()
	rows.Close()
	if fetchErr != nil {
		die("versioning-read: обход строк: %v", fetchErr)
	}
	fetchElapsed := time.Since(t0)

	if len(batch) == 0 {
		die("versioning-read: ноль событий в стриме %q — мерить нечего", versioningStreamID)
	}

	t1 := time.Now()
	var sum float64
	var badUnit int
	for _, r := range batch {
		e, err := Upcast(r.payload, r.schemaVer)
		if err != nil {
			die("versioning-read: upcast: %v", err)
		}
		sum += e.Amount
		// Сверка ПО ЗНАЧЕНИЮ, а не "поле непустое": испорченный шаг v1->v2
		// мог бы подставить в Unit мусор или пустую строку без изменения
		// Amount, и одна только сумма это не поймала бы.
		if e.Unit != versioningExpectedUnit {
			badUnit++
		}
	}
	upcastElapsed := time.Since(t1)

	out := VersioningResult{
		Mode: "versioning-read", Stream: versioningStreamID,
		Events: len(batch), SumAmount: sum, BadUnitEvents: badUnit,
		FetchMs: ms(fetchElapsed), UpcastMs: ms(upcastElapsed),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("вывод: %v", err)
	}
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*p)]
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loadgen: "+format+"\n", args...)
	os.Exit(1)
}
