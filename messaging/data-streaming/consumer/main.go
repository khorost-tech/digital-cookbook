// Go-консьюмер: customer.totals -> ClickHouse (+ изолированный дедуп-эксперимент).
//
// Почему явный консьюмер, а не Kafka engine ClickHouse: engine спрятал бы шов.
// Здесь видно главное — транзакция Kafka закончилась на границе брокера, и за
// дедуп на стоке отвечаем мы (version + ReplacingMergeTree, см. sql/02-clickhouse.sql).
//
//	go run . -brokers localhost:9096 -ch localhost:9001
//	go run . -dup          # каждое сообщение вставляется ДВАЖДЫ — имитация повтора
//	go run . -from-start   # backfill: перечитать топик с начала (сбрасывает committed offset)
//
// -mode dedup-demo — ДРУГОЙ вход и сток, тот же клиент и та же обвязка
// (read_committed, DLQ, ручной коммит после вставки): читает orders.events
// (события, не снимки состояния) и льёт в ds.order_counts_raw/ds.order_dedup —
// изолированный эксперимент, где дубль реально удваивает число (в отличие
// от customer_totals/raw_totals, где naive sum по снимкам состояния завышена
// уже без единого дубля, см. FIXTURES.md §3/§3.1 и комментарий у
// sql/02-clickhouse.sql).
//
//	go run . -mode dedup-demo -from-start -for 15s
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	// Дефолтный режим (-mode totals): customer.totals -> customer_totals/raw_totals.
	customerTotalsTopic = "customer.totals"
	customerTotalsGroup = "ds-sink"

	// -mode dedup-demo: orders.events -> order_counts_raw/order_dedup.
	// Отдельная consumer-group: другой топик, свой независимый committed
	// offset — общая группа с ds-sink здесь была бы просто неверна (ds-sink
	// вообще не подписан на orders.events).
	ordersEventsTopic = "orders.events"
	ordersEventsGroup = "ds-sink-dedup-demo"

	modeTotals    = "totals"
	modeDedupDemo = "dedup-demo"

	// maxConsecutiveEmptyErrors — сколько подряд poll'ов без единой записи, но
	// с ошибками fetch, терпим, прежде чем сдаться. Брокер недоступен или
	// топика нет — это НЕ "данных пока нет", и незачем крутиться вхолостую до
	// -for.
	maxConsecutiveEmptyErrors = 3
)

// main НЕ содержит логики: os.Exit() обрывает выполнение немедленно и
// пропускает все отложенные defer (cl.Close(), conn.Close(), cancel()).
// Если звать os.Exit() из тела с активными defer'ами, cl.Close() не
// отправит брокеру LeaveGroup — группа "зависнет" с мёртвым участником
// до session.timeout.ms, и СЛЕДУЮЩИЙ запуск может простоять впустую весь
// -for в ожидании ребалансировки (0 сообщений, exit 0 — то же самое немое
// поведение, с которым борется Important-6). Поэтому вся логика — в run(),
// а os.Exit() вызывается только здесь, после того как run() отработал все
// свои defer'ы и вернул код возврата.
func main() {
	os.Exit(run())
}

func run() int {
	brokers := flag.String("brokers", "localhost:9096", "kafka bootstrap")
	chAddr := flag.String("ch", "localhost:9001", "clickhouse native addr")
	dup := flag.Bool("dup", false, "вставлять каждое сообщение дважды (имитация at-least-once)")
	fromStart := flag.Bool("from-start", false, "читать топик с начала: сбрасывает committed offset группы на earliest ПЕРЕД чтением (backfill/replay)")
	dur := flag.Duration("for", 20*time.Second, "сколько работать")
	mode := flag.String("mode", modeTotals,
		"totals (по умолчанию): customer.totals -> customer_totals/raw_totals. "+
			"dedup-demo: orders.events -> order_counts_raw/order_dedup — изолированный дедуп-эксперимент "+
			"(см. sql/02-clickhouse.sql), где дубль/replay реально удваивает число, в отличие от накопительных снимков totals-режима")
	flag.Parse()

	var topic, group string
	switch *mode {
	case modeTotals:
		topic, group = customerTotalsTopic, customerTotalsGroup
	case modeDedupDemo:
		topic, group = ordersEventsTopic, ordersEventsGroup
	default:
		fmt.Printf("неизвестный -mode=%q, ожидается %q или %q\n", *mode, modeTotals, modeDedupDemo)
		return 1
	}

	conn, err := chConnect(*chAddr)
	if err != nil {
		fmt.Println("clickhouse:", err)
		return 1
	}
	defer conn.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Admin-клиент отдельный от основного consumer-group клиента: и для
	// preflight-проверки, и для -from-start ниже нужны административные
	// вызовы (метаданные, ручной commit оффсетов группы) ДО того, как
	// join'нится сам consumer.
	adminCl, err := kgo.NewClient(kgo.SeedBrokers(*brokers))
	if err != nil {
		fmt.Println("kafka admin client:", err)
		return 1
	}
	adm := kadm.NewClient(adminCl)

	// Preflight: брокер недоступен или топика нет — должны узнать об этом
	// СРАЗУ, с понятной ошибкой, а не наблюдать вхолостую крутящийся
	// PollFetches до конца -for. На полностью недоступном брокере
	// PollFetches сам по себе не возвращает ни записей, ни ошибок — он
	// просто блокируется до дедлайна (kgo ретраит соединение внутри себя),
	// поэтому проверка нужна ДО входа в цикл, с собственным (коротким)
	// таймаутом, а не полагаться на deadline всего прогона.
	if err := verifyTopic(ctx, adm, topic); err != nil {
		adminCl.Close()
		fmt.Println("kafka:", err)
		return 1
	}

	// DLQ-топик должен существовать ДО того, как в цикле ниже встретится первое
	// битое сообщение — см. комментарий у ensureDLQTopic в dlq.go про то, почему
	// нельзя полагаться на auto.create.topics.enable брокера.
	if err := ensureDLQTopic(ctx, adm, topic); err != nil {
		adminCl.Close()
		fmt.Println("kafka:", err)
		return 1
	}

	if *fromStart {
		// -from-start обещает перечитать топик С НАЧАЛА при каждом запуске.
		// kgo.ConsumeResetOffset применяется ТОЛЬКО когда у группы ещё нет
		// закоммиченного оффсета — начиная со второго прогона он уже есть, и
		// флаг молча переставал бы работать. Поэтому сбрасываем committed
		// offset явно через kadm, ДО того как присоединится основной
		// consumer-group клиент ниже.
		if err := resetGroupToEarliest(ctx, adm, group, topic); err != nil {
			adminCl.Close()
			fmt.Println("reset offset:", err)
			return 1
		}
	}
	adminCl.Close()

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(*brokers),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(group),
		// Если у группы вообще нет committed offset (самый первый запуск) —
		// начинаем с начала, а не с конца: демо-стенду нужны все события.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		// Коммитим оффсеты САМИ, руками, после того как пачка реально долетела
		// до ClickHouse (см. CommitRecords ниже) — не каждые ~5с по таймеру.
		// Автокоммит здесь означал бы противоположное тому, что рассказывает
		// статья: при падении между "прочитали" и "вставили" данные ТЕРЯЮТСЯ
		// молча (at-most-once), а не дублируются (at-least-once). Это и есть
		// тот шов, ради которого используется явный консьюмер, а не engine.
		kgo.DisableAutoCommit(),
		// read_committed, а не дефолт franz-go (read_uncommitted). Источник —
		// customer.totals: транзакционный ВЫВОД Kafka Streams
		// (PROCESSING_GUARANTEE_CONFIG=EXACTLY_ONCE_V2, см. StreamsApp.java) —
		// записи попадают в топик ВНУТРИ транзакции, и брокер видит две
		// разных вещи: "запись физически на диске" и "транзакция закоммичена
		// брокером". read_uncommitted отдаёт первое — в том числе записи
		// транзакции, которая ЕЩЁ идёт или уже ПРЕРВАНА (aborted). Именно
		// это и произошло бы в демонстрации восстановления после kill -9
		// (см. FIXTURES.md, «TaskCorruptedException» и §2): если Kafka
		// Streams убит посреди активной EOS-транзакции, её незакоммиченные
		// записи физически лежат в customer.totals ДО того, как брокер
		// решит abort/commit — read_uncommitted консьюмер прочитал бы их
		// как обычные сообщения и, если транзакция в итоге прервалась,
		// протащил бы в витрину данные, которых с точки зрения Kafka
		// никогда не было. read_committed — это и есть контракт EOS для
		// потребителей: видимость результата транзакции только ПОСЛЕ того,
		// как брокер зафиксировал commit-маркер (тот самый маркер, из-за
		// которого customer.totals показывает LAG=1 в состоянии покоя, см.
		// scripts/lag.sh). Admin-клиент (adminCl выше) эту опцию НЕ
		// получает: он не потребляет данные, только читает метаданные и
		// коммитит оффсеты группы административно.
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	if err != nil {
		fmt.Println("kafka:", err)
		return 1
	}
	defer cl.Close()

	deadline, cancel2 := context.WithTimeout(ctx, *dur)
	defer cancel2()

	ok, bad, insertErrs, fetchErrs, dlqErrs := 0, 0, 0, 0, 0
	consecutiveEmptyErrors := 0
	// aborted — цикл прерван ДО того, как -for/Ctrl+C завершили его штатно:
	// либо insertBatch необратимо упал (insertErrs>0 это уже покрывает), либо
	// подряд maxConsecutiveEmptyErrors fetch'ей подряд не принесли ни записи,
	// ни прогресса (брокер недоступен / топик пропал на середине прогона).
	// Отдельно от insertErrs, потому что exit-код должен зависеть именно от
	// ЭТОГО, а не от сырого fetchErrs — единичная транзиентная ошибка fetch
	// (например, ребаланс), после которой консьюмер как ни в чём не бывало
	// дочитал и вставил всё до конца, не должна ронять exit-код полностью
	// успешного прогона (40/40 вставлено).
	aborted := false

	// dlqCandidate — битая запись, ждущая отправки в DLQ (см. цикл по badRecs
	// внутри loop, ПОСЛЕ конца EachRecord и ДО insertBatch/CommitRecords).
	type dlqCandidate struct {
		rec *kgo.Record
		err error
	}

loop:
	for {
		fetches := cl.PollFetches(deadline)
		if fetches.IsClientClosed() {
			break
		}
		realErrs := 0
		for _, e := range fetches.Errors() {
			if errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, context.Canceled) {
				// Штатное завершение по -for или Ctrl+C, не ошибка брокера/топика.
				continue
			}
			realErrs++
			fetchErrs++
			fmt.Printf("ошибка fetch %s[%d]: %v\n", e.Topic, e.Partition, e.Err)
		}
		if deadline.Err() != nil {
			break
		}

		var totals []total
		var versions []uint64
		var orderEvents []orderEvent
		var orderVersions []uint64
		var recs []*kgo.Record
		var badRecs []dlqCandidate
		fetches.EachRecord(func(r *kgo.Record) {
			if *mode == modeDedupDemo {
				// Изолированный дедуп-эксперимент (см. sql/02-clickhouse.sql):
				// вход — orders.events (события), не customer.totals (снимки
				// состояния). n=2 под -dup здесь имитирует повторную доставку
				// В ПРЕДЕЛАХ ОДНОЙ пачки — самостоятельная демонстрация
				// (см. FIXTURES.md §изолированный-дедуп) строит эффект дубля
				// иначе, через ДВА прогона -from-start подряд (первый = N,
				// второй = replay поверх уже прочитанного = 2N в raw, N в
				// dedup), но n=2 под -dup остаётся содержательным и здесь: та
				// же кодовая дорожка "дубль внутри пачки", что и в totals-режиме.
				// Разбор и семантическая валидация — в одной функции
				// (parseValidOrderEvent, см. validate.go), чтобы её можно
				// было покрыть unit-тестом отдельно от Kafka/ClickHouse (см.
				// validate_test.go): order_id=0/customer_id вне 1..5
				// недостижимы для настоящего события этого стенда
				// (order_id — bigserial PK PG, начинается с 1; customer_id —
				// 1..5, см. seed.sh).
				o, err := parseValidOrderEvent(r.Value)
				if err != nil {
					bad++
					fmt.Printf("битое событие offset=%d: %v\n", r.Offset, err)
					badRecs = append(badRecs, dlqCandidate{rec: r, err: err})
					recs = append(recs, r)
					return
				}
				n := dupFactor(*dup)
				for i := 0; i < n; i++ {
					orderEvents = append(orderEvents, o)
					orderVersions = append(orderVersions, uint64(r.Offset))
				}
				recs = append(recs, r)
				return
			}

			// Разбор и семантическая валидация — в одной функции
			// (parseValidTotal, см. validate.go), чтобы её можно было
			// покрыть unit-тестом отдельно от Kafka/ClickHouse (см.
			// validate_test.go). json.Unmarshal ловит только СИНТАКСИС
			// JSON — "{}" и "{"customer_id":1}" синтаксически валидны и
			// анмаршалятся БЕЗ единой ошибки (customer_id=0/orders=0/
			// total=0 и customer_id=1/orders=0/total=0 соответственно) —
			// семантически пустые/усечённые записи, которые без проверок
			// внутри parseValidTotal тихо уехали бы в витрину как мусор.
			// Диапазон клиентов стенда — 1..5 (см. scripts/seed.sh:
			// "cust := 1 + (i % 5)"); orders==0 недостижим для настоящего
			// агрегата (TotalsProcessor всегда пишет первую запись с
			// orders=1). Ужесточение по сравнению с предыдущей редакцией
			// (раньше отвергался только customer_id==0): значение вне
			// диапазона 1..5 (например, синтетический customer_id=999 из
			// scripts/schema-evolve.sh, см. FIXTURES.md §7 — исторический
			// прогон ДО этого ужесточения) теперь тоже уходит в DLQ.
			t, err := parseValidTotal(r.Value)
			if err != nil {
				bad++
				fmt.Printf("битое событие offset=%d: %v\n", r.Offset, err)
				// r идёт и в badRecs (отправить в DLQ), и в recs (закоммитить
				// оффсет вместе с остальной пачкой после того, как DLQ
				// подтвердит запись, см. ниже). Без recs это сообщение НИКОГДА
				// не коммитится — при следующем запуске оно вычитывается
				// заново и уходит в DLQ повторно КАЖДЫЙ раз, а не один. DLQ
				// существует именно для того, чтобы пайплайн мог продвинуться
				// дальше мимо битого оффсета, а не застрять на нём навечно.
				badRecs = append(badRecs, dlqCandidate{rec: r, err: err})
				recs = append(recs, r)
				return
			}
			n := dupFactor(*dup)
			for i := 0; i < n; i++ {
				totals = append(totals, t)
				versions = append(versions, uint64(r.Offset))
			}
			recs = append(recs, r)
		})

		if len(recs) == 0 {
			if realErrs > 0 {
				consecutiveEmptyErrors++
				if consecutiveEmptyErrors >= maxConsecutiveEmptyErrors {
					fmt.Println("остановка: подряд несколько fetch без единой записи и с ошибками — брокер недоступен или топика нет")
					aborted = true
					break loop
				}
			}
			continue
		}
		consecutiveEmptyErrors = 0

		// DLQ — ДО insertBatch и ДО CommitRecords, синхронно (ждём ack брокера
		// внутри toDLQ). Оффсет битой записи коммитится ниже ОДНИМ вызовом
		// CommitRecords вместе со всей пачкой (recs включает и хорошие, и
		// битые записи), поэтому пока DLQ-запись не подтверждена брокером —
		// коммитить пачку нельзя: иначе при падении между "пропустили битое" и
		// "закоммитили оффсет" событие теряется бесследно — ни в витрине, ни в
		// DLQ, что противоречит at-least-once, который демонстрирует весь
		// стенд. Тот же принцип, что и у insertBatch чуть ниже, просто для
		// другой ветки пачки.
		if len(badRecs) > 0 {
			dlqOK := true
			unconfirmed := len(badRecs)
			for i, br := range badRecs {
				if err := toDLQ(ctx, cl, br.rec, br.err); err != nil {
					fmt.Println("dlq:", err)
					dlqOK = false
					unconfirmed = len(badRecs) - i
					break
				}
			}
			if !dlqOK {
				// Как и при ошибке insertBatch ниже: НЕ коммитим пачку вовсе
				// (даже уже успешно вставленные строки этой пачки в CH — при
				// следующем запуске без -from-start вся пачка перечитается
				// заново). unconfirmed = len(badRecs)-i, а не len(badRecs):
				// i предыдущих записей badRecs уже ДОЕХАЛИ до DLQ и получили
				// ack от брокера (ProduceSync внутри toDLQ синхронный) —
				// считать их "непроведёнными через DLQ" было бы неверно, хотя
				// их оффсет всё равно не коммитится вместе с остальной пачкой.
				// Тот же принцип "непроведённого остатка", что и у
				// insertErrs += len(recs) ниже, где ни одна запись пачки не
				// проведена вовсе (insertBatch — один вызов на всю пачку, а не
				// цикл, поэтому там частичного прогресса не бывает).
				dlqErrs += unconfirmed
				break loop
			}
		}

		// ctx (не deadline и не context.Background()): вставку должен прерывать
		// Ctrl+C, а не "закончилось время демонстрации" — иначе INSERT обрывается
		// на середине пачки посреди -for.
		//
		// len(recs)-len(badRecs), а не len(recs), в обеих ветках ниже:
		// totals/orderEvents считают СТРОКИ для вставки в CH (с -dup — вдвое
		// больше "хороших" recs, т.к. каждое сообщение дублируется), а
		// badRecs в них не входят вовсе — они не предназначались для CH.
		// "ошибок_вставки" должен отражать число НЕДОСТАВЛЕННЫХ сообщений
		// исходной пачки, иначе одна и та же упавшая пачка из 40 сообщений
		// даёт то 40, то 80 в зависимости от -dup — вводит в заблуждение при
		// чтении вывода.
		if *mode == modeDedupDemo {
			if err := insertOrderCountBatch(ctx, conn, orderEvents, orderVersions); err != nil {
				insertErrs += len(recs) - len(badRecs)
				fmt.Println("insert:", err)
				break loop
			}
			ok += len(orderEvents)
		} else {
			if err := insertBatch(ctx, conn, totals, versions); err != nil {
				// Ошибка вставки — НЕ глушим: считаем, печатаем, останавливаемся.
				// Оффсеты этой пачки НЕ коммитим (CommitRecords ниже не вызван) —
				// при следующем запуске без -from-start эти сообщения будут
				// прочитаны заново, а не потеряны. Битые записи из этой же пачки
				// (если были) уже успешно уехали в DLQ выше — при повторном
				// чтении пачки они отправятся в DLQ ещё раз (дубль в DLQ,
				// приемлемая цена at-least-once), но не потеряются.
				insertErrs += len(recs) - len(badRecs)
				fmt.Println("insert:", err)
				break loop
			}
			ok += len(totals)
		}

		if err := cl.CommitRecords(ctx, recs...); err != nil {
			fmt.Println("commit:", err)
			insertErrs++ // оффсет не подтверждён — при перезапуске возможен повтор пачки
		}
	}

	fmt.Printf("консьюмер: вставлено=%d битых=%d dlq_ошибок=%d ошибок_вставки=%d ошибок_fetch=%d (dup=%v, from-start=%v, mode=%s)\n",
		ok, bad, dlqErrs, insertErrs, fetchErrs, *dup, *fromStart, *mode)
	// Exit-код зависит от insertErrs (данные не долетели до CH), dlqErrs
	// (битое сообщение не долетело до DLQ — тоже потенциальная потеря) и
	// aborted (прогон прерван из-за упорных fetch-ошибок), а НЕ от голого
	// fetchErrs>0: транзиентная, самовосстановившаяся ошибка fetch (ребаланс
	// и т.п.), после которой всё было успешно вставлено и закоммичено, не
	// должна давать ненулевой exit — это был полностью успешный прогон.
	if insertErrs > 0 || dlqErrs > 0 || aborted {
		return 1
	}
	return 0
}

// verifyTopic проверяет брокер и топик ДО начала потребления: короткий
// собственный таймаут (не общий -for) отличает "брокер недоступен / топика
// нет" от "данных пока нет" — без этой проверки обе ситуации выглядят
// одинаково (вставлено=0, exit 0, тишина до конца -for).
func verifyTopic(ctx context.Context, adm *kadm.Client, topic string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	details, err := adm.ListTopics(checkCtx, topic)
	if err != nil {
		return fmt.Errorf("брокер недоступен: %w", err)
	}
	td, ok := details[topic]
	if !ok {
		return fmt.Errorf("топик %q не найден", topic)
	}
	if td.Err != nil {
		return fmt.Errorf("топик %q: %w", topic, td.Err)
	}
	return nil
}

// resetGroupToEarliest сбрасывает committed offset группы group на earliest
// (start) offset топика через kadm, без вступления в группу (CommitOffsets —
// административная операция, consumer group для неё не нужна).
func resetGroupToEarliest(ctx context.Context, adm *kadm.Client, group, topic string) error {
	start, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return fmt.Errorf("list start offsets: %w", err)
	}
	if err := start.Error(); err != nil {
		return fmt.Errorf("start offsets: %w", err)
	}

	if err := adm.CommitAllOffsets(ctx, group, start.Offsets()); err != nil {
		return fmt.Errorf("commit offsets: %w", err)
	}
	return nil
}
