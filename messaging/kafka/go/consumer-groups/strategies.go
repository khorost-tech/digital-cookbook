package main

import (
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type strategySpec struct {
	name     string
	balancer kgo.GroupBalancer
}

// scenarioStrategies сравнивает 4 стратегии назначения партиций (те же, что
// franz-go предлагает "из коробки": range, roundrobin, sticky,
// cooperative-sticky) на ОДНОМ и том же сценарии: 3 консьюмера стабилизируют
// назначение, затем подключается четвёртый — и мы смотрим, что именно каждая
// стратегия отзывает у уже работающих членов. Eager-протокол (range /
// roundrobin / sticky) обязан отозвать ВСЁ текущее назначение перед
// повторным JoinGroup (stop-the-world); cooperative-sticky отзывает только
// те партиции, что реально переезжают — это и есть измеримая, а не
// декларируемая разница (см. README, реальный прогон).
func scenarioStrategies() {
	specs := []strategySpec{
		{"range", kgo.RangeBalancer()},
		{"roundrobin", kgo.RoundRobinBalancer()},
		{"sticky", kgo.StickyBalancer()},
		{"cooperative-sticky", kgo.CooperativeStickyBalancer()},
	}

	for _, spec := range specs {
		fmt.Printf("\n--- стратегия: %s (IsCooperative=%v) ---\n", spec.name, spec.balancer.IsCooperative())
		runStrategy(spec)
	}
}

func runStrategy(spec strategySpec) {
	groupID := "cg-strategy-" + spec.name
	opt := kgo.Balancers(spec.balancer)

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		logf("producer: %v", err)
		return
	}
	defer producer.Close()

	a := newMember("consumer-a", groupID, nil, opt)
	b := newMember("consumer-b", groupID, nil, opt)
	c := newMember("consumer-c", groupID, nil, opt)
	members := []*member{a, b, c}
	for _, m := range members {
		m.waitFirstAssign(20 * time.Second)
	}
	// Кооперативный ребаланс может сходиться несколькими раундами (часть
	// партиций временно "повисает", пока не отзовётся явно) — реально ждём
	// сходимости, а не гадаем с фиксированной паузой.
	waitUntilStable(15*time.Second, members...)
	logf("[%s] начальное распределение (3 консьюмера): a=%s b=%s c=%s", spec.name, a.snapshot(), b.snapshot(), c.snapshot())
	assertPartitioned(spec.name+": 3 консьюмера", members...)

	stopProduce := make(chan struct{})
	continuousProducer(producer, stopProduce, 150*time.Millisecond)

	// снимок истории revoke ДО добавления 4-го — чтобы после сравнить,
	// что именно изменилось у КАЖДОГО существующего члена.
	beforeRevokes := map[string][]revokeEvent{}
	for _, m := range members {
		beforeRevokes[m.id] = m.revokeHistory()
	}

	logf("[%s] подключаем consumer-d -> ребаланс", spec.name)
	d := newMember("consumer-d", groupID, nil, opt)
	d.waitFirstAssign(20 * time.Second)
	allMembers := []*member{a, b, c, d}
	waitUntilStable(15*time.Second, allMembers...)
	logf("[%s] после подключения consumer-d: a=%s b=%s c=%s d=%s", spec.name,
		a.snapshot(), b.snapshot(), c.snapshot(), d.snapshot())
	assertPartitioned(spec.name+": 4 консьюмера", allMembers...)

	// Ключевое наблюдение eager vs incremental: что именно REVOKED у
	// старых членов при вступлении нового.
	isCooperative := spec.balancer.IsCooperative()
	for _, m := range members {
		after := m.revokeHistory()
		newRevokes := after[len(beforeRevokes[m.id]):]
		if len(newRevokes) == 0 {
			logf("[%s] %s: НИ ОДНОГО revoke-события при вступлении consumer-d (партиции не тронуты вовсе)", spec.name, m.id)
			continue
		}
		hasFull := false
		for _, ev := range newRevokes {
			switch {
			case len(ev.ps) == 0:
				logf("[%s] %s revoked при вступлении consumer-d: %s — ПУСТО (инкрементально, партиции не переезжали)", spec.name, m.id, ev.ps)
			case ev.full:
				logf("[%s] %s revoked при вступлении consumer-d: %s — ВСЁ текущее назначение (stop-the-world, eager)", spec.name, m.id, ev.ps)
				hasFull = true
			default:
				logf("[%s] %s revoked при вступлении consumer-d: %s — ЧАСТЬ назначения (инкрементально, часть партиций сохранена — cooperative)", spec.name, m.id, ev.ps)
			}
		}
		// Ассерт — не просто лог: разница eager/cooperative обязана
		// проявляться механически, иначе регресс (не тот балансер, смена
		// поведения библиотеки) пройдёт незамеченным.
		if isCooperative && hasFull {
			log.Fatalf("[assert] FAIL (%s): %s — cooperative-sticky отозвал ПОЛНОЕ текущее назначение (full revoke), ожидался только пустой/частичный revoke", spec.name, m.id)
		}
		if !isCooperative && !hasFull {
			log.Fatalf("[assert] FAIL (%s): %s — eager-стратегия НЕ отозвала полное текущее назначение (full revoke) при вступлении consumer-d, ожидался stop-the-world", spec.name, m.id)
		}
	}
	if isCooperative {
		logf("[assert] OK (%s): ни одного full-revoke — incremental cooperative подтверждён", spec.name)
	} else {
		logf("[assert] OK (%s): у каждого прежнего члена был full-revoke — stop-the-world (eager) подтверждён", spec.name)
	}

	close(stopProduce)
	for _, m := range allMembers {
		m.close()
	}
}
