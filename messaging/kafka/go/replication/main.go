// Command replication — стенд #3 серии "Kafka: глубокое погружение":
// репликация и надёжность (ISR, acks, durability, broker-kill, split-brain).
//
// В отличие от log-basics/consumer-groups, часть сценариев этого стенда
// требует убивать/поднимать брокеров docker-командами — а у клиента внутри
// контейнера нет доступа к docker socket. Поэтому программа разбита на
// "фазы" (-scenario=...), которые оркестрирует bash-скрипт с хоста
// (../../ops/broker-kill.sh) — он вызывает нужные фазы ДО и ПОСЛЕ
// docker kill/start в нужный момент и печатает всё вживую.
//
// Запуск отдельной фазы (пример):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./replication -scenario=describe -topic=demo-repl
//
// См. ../../ops/broker-kill.sh для полного сценария и ../../README.md для
// реального прогона.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	scenario := flag.String("scenario", "", "setup|describe|acks-bench|produce|verify|minisr-produce")
	topic := flag.String("topic", "demo-repl", "имя топика")
	partitions := flag.Int("partitions", 1, "число партиций (при -scenario=setup)")
	rf := flag.Int("rf", 3, "replication factor (при -scenario=setup)")
	minIsr := flag.String("minisr", "2", "min.insync.replicas (при -scenario=setup); пусто = не задавать")
	unclean := flag.String("unclean", "", "unclean.leader.election.enable (true/false; при -scenario=setup); пусто = не задавать")
	n := flag.Int("n", 20, "число сообщений (produce/verify -expect)")
	acks := flag.String("acks", "all", "0|1|all")
	idempotent := flag.Bool("idempotent", true, "enable.idempotence")
	retries := flag.Int("retries", -1, "RecordRetries (-1 = дефолт библиотеки)")
	reqTimeout := flag.Duration("req-timeout", 10*time.Second, "таймаут produce-запроса на брокер")
	prefix := flag.String("prefix", "msg", "префикс значения записи (для индекс-парсинга при verify -checkdup)")
	delay := flag.Duration("delay", 0, "пауза между записями при produce (растянуть окно для broker-kill вживую)")
	expect := flag.Int("expect", 0, "ожидаемое число записей при verify (0 = не проверять точное число, читать до idle)")
	idle := flag.Duration("idle", 5*time.Second, "idle-таймаут при verify без -expect")
	soft := flag.Bool("soft", false, "не падать (log.Fatalf) при несовпадении verify -expect, только напечатать расхождение — для сценариев, где потеря/отказ ОЖИДАЕМЫ")
	checkdup := flag.Bool("checkdup", false, "при verify — проверить отсутствие повторяющихся индексов в значениях (идемпотентность)")
	printIndices := flag.Bool("print-indices", false, "при verify — напечатать отсортированный список индексов (parsed из value) одной строкой, для сравнения с acked-списком produce-ff")
	collect := flag.Duration("collect", 3*time.Second, "сколько ждать callback-и при -scenario=produce-ff после выставления всех записей")
	flag.Parse()

	seeds := strings.Split(*brokers, ",")

	switch *scenario {
	case "setup":
		configs := map[string]*string{}
		if *minIsr != "" {
			configs["min.insync.replicas"] = strPtr(*minIsr)
		}
		if *unclean != "" {
			configs["unclean.leader.election.enable"] = strPtr(*unclean)
		}
		recreateTopic(seeds, *topic, int32(*partitions), int16(*rf), configs)
		p := waitForLeader(seeds, *topic, 30*time.Second)
		printPartitionState("после setup", p)

	case "describe":
		p := describePartition(seeds, *topic)
		printPartitionState("текущее состояние", p)

	case "set-unclean":
		alterTopicConfig(seeds, *topic, "unclean.leader.election.enable", *unclean)

	case "acks-bench":
		runAcksBench(seeds, *topic)

	case "produce":
		runProduce(seeds, *topic, *n, *acks, *idempotent, *retries, *reqTimeout, *prefix, *delay)

	case "verify":
		runVerify(seeds, *topic, *expect, *idle, *soft, *checkdup, *printIndices)

	case "produce-ff":
		runProduceFF(seeds, *topic, *n, *acks, *idempotent, *retries, *prefix, *collect, *delay)

	case "minisr-produce":
		runMinISRProduce(seeds, *topic, *acks)

	default:
		log.Fatalf("неизвестный -scenario=%q (setup|describe|set-unclean|acks-bench|produce|verify|minisr-produce)", *scenario)
	}
}

// runAcksBench — самодостаточный (без docker kill) сценарий: пересоздаёт
// demo-repl и последовательно замеряет относительную латентность/throughput
// acks=0 vs acks=1 vs acks=all на одном и том же топике. Числа
// host-зависимы (сеть/диск/шедулинг), но ОТНОСИТЕЛЬНЫЙ порядок
// (0 быстрее 1 быстрее all) — это следствие протокола, не случайность.
func runAcksBench(seeds []string, topic string) {
	minisr := "2"
	recreateTopic(seeds, topic, 1, 3, map[string]*string{"min.insync.replicas": &minisr})
	waitForLeader(seeds, topic, 30*time.Second)

	levels := []string{"0", "1", "all"}
	const perLevel = 200
	type stat struct {
		level    string
		acked    int
		errs     int
		total    time.Duration
		min, max time.Duration
	}
	var stats []stat
	for _, lvl := range levels {
		// идемпотентность в kgo (и в Kafka в целом) требует acks=all —
		// для acks=0/1 явно выключаем, иначе NewClient падает с ошибкой.
		cl := newProducer(seeds, acksFromString(lvl), lvl == "all", -1, 10*time.Second)
		start := time.Now()
		var s stat
		s.level = lvl
		s.min = time.Hour
		for i := 0; i < perLevel; i++ {
			t0 := time.Now()
			rec := &kgo.Record{Topic: topic, Value: []byte(fmt.Sprintf("acks%s-%d", lvl, i))}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			res := cl.ProduceSync(ctx, rec)
			cancel()
			elapsed := time.Since(t0)
			if _, err := res.First(); err != nil {
				s.errs++
			} else {
				s.acked++
			}
			if elapsed < s.min {
				s.min = elapsed
			}
			if elapsed > s.max {
				s.max = elapsed
			}
		}
		s.total = time.Since(start)
		cl.Close()
		stats = append(stats, s)
		fmt.Printf("[acks-bench] acks=%-3s acked=%d/%d errs=%d elapsed=%s throughput=%.1f msg/s avg_latency=%s min=%s max=%s\n",
			lvl, s.acked, perLevel, s.errs, s.total,
			float64(perLevel)/s.total.Seconds(),
			s.total/time.Duration(perLevel), s.min, s.max)
	}
	fmt.Println("[acks-bench] характерный прогон (host-зависимые абсолютные числа); ожидаемый ПОРЯДОК: acks=0 быстрее acks=1 быстрее acks=all — это протокольное следствие (0: не ждём ответа брокера вовсе; 1: ждём запись на диск лидера; all: ждём подтверждения от всех ISR)")
}

func runProduce(seeds []string, topic string, n int, acksStr string, idempotent bool, retries int, reqTimeout time.Duration, prefix string, delay time.Duration) {
	if idempotent && acksStr != "all" {
		fmt.Printf("[produce] idempotent=true несовместимо с acks=%s (идемпотентность требует acks=all) — выключаю idempotent для этого прогона\n", acksStr)
		idempotent = false
	}
	cl := newProducer(seeds, acksFromString(acksStr), idempotent, retries, reqTimeout)
	defer cl.Close()
	results := produceSequential(cl, topic, n, prefix, delay)
	acked, failed := 0, 0
	for _, r := range results {
		if r.err == nil {
			acked++
		} else {
			failed++
		}
	}
	fmt.Printf("[produce] итого: acked=%d failed=%d из %d (acks=%s idempotent=%v)\n", acked, failed, n, acksStr, idempotent)
}

func runVerify(seeds []string, topic string, expect int, idle time.Duration, soft bool, checkdup bool, printIndices bool) {
	recv := consumeFromStart(seeds, topic, expect, idle)
	fmt.Printf("[verify] топик %s: прочитано %d записей\n", topic, len(recv))
	printRecvRecords(recv)
	if expect > 0 {
		if len(recv) != expect {
			msg := fmt.Sprintf("[verify] РАСХОЖДЕНИЕ: прочитано %d, ожидалось %d", len(recv), expect)
			if soft {
				fmt.Println(msg)
			} else {
				log.Fatalf(msg)
			}
		} else {
			fmt.Printf("[verify] OK: прочитано == ожидалось == %d\n", expect)
		}
	}
	if checkdup {
		unique, dupCount, samples := checkNoDuplicateIndices(recv)
		if dupCount > 0 {
			log.Fatalf("[verify] ДУБЛИ: %d повторных вхождений среди %d уникальных индексов, примеры индексов: %v", dupCount, unique, samples)
		} else {
			fmt.Printf("[verify] OK: дублей индексов нет (%d уникальных)\n", unique)
		}
	}
	if printIndices {
		idxs := make([]int, 0, len(recv))
		for _, r := range recv {
			idxs = append(idxs, extractIndex(r.value))
		}
		sort.Ints(idxs)
		fmt.Printf("[verify] индексы в топике (%d шт.): %v\n", len(idxs), idxs)
	}
}

func runProduceFF(seeds []string, topic string, n int, acksStr string, idempotent bool, retries int, prefix string, collect time.Duration, delay time.Duration) {
	if idempotent && acksStr != "all" {
		idempotent = false
	}
	cl := newProducer(seeds, acksFromString(acksStr), idempotent, retries, 5*time.Second)
	defer cl.Close()
	acked, failed := produceFireAndForget(cl, topic, n, prefix, collect, delay)
	sort.Ints(acked)
	sort.Ints(failed)
	fmt.Printf("[produce-ff] callback-успехов: %d/%d, callback-ошибок: %d, без ответа за отведённое время: %d\n",
		len(acked), n, len(failed), n-len(acked)-len(failed))
	fmt.Printf("[produce-ff] acked-индексы: %v\n", acked)
	if len(failed) > 0 {
		fmt.Printf("[produce-ff] failed-индексы: %v\n", failed)
	}
}

// runMinISRProduce — одна попытка acks=all записи с быстрым фейлом
// (RecordRetries(0), короткий req-timeout, идемпотентность выключена — чтобы
// не мешать быстрому фейлу и однозначно классифицировать ошибку). Печатает
// классификацию ошибки (NOT_ENOUGH_REPLICAS / CLIENT_TIMEOUT / OK / другое).
func runMinISRProduce(seeds []string, topic string, acksStr string) {
	cl := newProducer(seeds, acksFromString(acksStr), false, 0, 6*time.Second)
	defer cl.Close()
	rec := &kgo.Record{Topic: topic, Value: []byte("minisr-probe")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	res := cl.ProduceSync(ctx, rec)
	elapsed := time.Since(start)
	_, err := res.First()
	class := classifyProduceErr(err)
	fmt.Printf("[minisr-produce] topic=%s acks=%s -> %s (elapsed=%s, err=%v)\n", topic, acksStr, class, elapsed, err)
}
