package main

import (
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// scenarioRebalance — базовый сценарий: consumer-1 в одиночку получает все
// партиции; подключаем consumer-2, затем consumer-3 — ребаланс,
// перераспределение; отключаем consumer-3, затем consumer-2 — обратный
// ребаланс. Стратегия зафиксирована на "range" (классический eager-протокол)
// — самый простой случай для первого знакомства с revoke/assign. Сравнение
// стратегий, включая cooperative-sticky, — сценарий "strategies" ниже.
func scenarioRebalance() {
	const groupID = "cg-rebalance-demo"

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	defer producer.Close()
	stopProduce := make(chan struct{})

	c1 := newMember("consumer-1", groupID, nil, kgo.Balancers(kgo.RangeBalancer()))
	c1.waitFirstAssign(15 * time.Second)
	logf("--- consumer-1 один в группе: %s ---", c1.snapshot())
	assertPartitioned("consumer-1 один", c1)

	// Продюсить начинаем ТОЛЬКО теперь (после стабильного назначения) и
	// продолжаем непрерывно весь сценарий — иначе живое перераспределение
	// чтения между consumer-1/2/3 не будет видно (см. content-note в README).
	sent := continuousProducer(producer, stopProduce, 150*time.Millisecond)

	c2 := newMember("consumer-2", groupID, nil, kgo.Balancers(kgo.RangeBalancer()))
	c2.waitFirstAssign(20 * time.Second)
	waitUntilStable(15*time.Second, c1, c2)
	logf("--- после подключения consumer-2: c1=%s c2=%s ---", c1.snapshot(), c2.snapshot())
	assertPartitioned("consumer-1+2", c1, c2)

	c3 := newMember("consumer-3", groupID, nil, kgo.Balancers(kgo.RangeBalancer()))
	c3.waitFirstAssign(20 * time.Second)
	waitUntilStable(15*time.Second, c1, c2, c3)
	logf("--- после подключения consumer-3: c1=%s c2=%s c3=%s ---", c1.snapshot(), c2.snapshot(), c3.snapshot())
	assertPartitioned("consumer-1+2+3", c1, c2, c3)

	logf("--- отключаем consumer-3 ---")
	c3.close()
	waitUntilStable(15*time.Second, c1, c2)
	logf("--- после отключения consumer-3: c1=%s c2=%s ---", c1.snapshot(), c2.snapshot())
	assertPartitioned("consumer-1+2 (после ухода 3)", c1, c2)

	logf("--- отключаем consumer-2 ---")
	c2.close()
	waitUntilStable(15*time.Second, c1)
	logf("--- после отключения consumer-2: c1=%s ---", c1.snapshot())
	assertPartitioned("consumer-1 (после ухода 2 и 3)", c1)

	close(stopProduce)
	c1.close()
	logf("[producer] за сценарий rebalance отправлено: %d, суммарно получено (c1+c2+c3): %d",
		sent.Load(), c1.consumed.Load()+c2.consumed.Load()+c3.consumed.Load())
}
