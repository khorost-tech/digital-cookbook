package main

import (
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// scenarioStatic демонстрирует static membership (kgo.InstanceID, аналог
// group.instance.id): рестарт статического члена В ПРЕДЕЛАХ session.timeout
// НЕ триггерит ребаланс всей группы и возвращает те же партиции. Для
// контраста тут же — рестарт ОБЫЧНОГО (динамического) члена: тот триггерит
// ребаланс немедленно, потому что graceful Close() динамического члена шлёт
// LeaveGroupRequest, а статического — нет (проверено по исходникам franz-go:
// pkg/kgo/consumer_group.go, leave() пропускает отправку запроса, если
// cfg.instanceID != nil; тот же принцип — KIP-345 — реализован в
// kafka-clients).
func scenarioStatic() {
	const groupID = "cg-static-demo"
	const sessionTimeout = 12 * time.Second

	producer, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	defer producer.Close()
	stopProduce := make(chan struct{})
	continuousProducer(producer, stopProduce, 150*time.Millisecond)

	// --- Часть A: статические члены ---
	a := newMember("static-a", groupID, nil, kgo.InstanceID("static-a"), kgo.SessionTimeout(sessionTimeout))
	b := newMember("static-b", groupID, nil, kgo.InstanceID("static-b"), kgo.SessionTimeout(sessionTimeout))
	a.waitFirstAssign(20 * time.Second)
	b.waitFirstAssign(20 * time.Second)
	waitUntilStable(15*time.Second, a, b)

	beforeA, beforeB := a.snapshot(), b.snapshot()
	bAssignsBefore, bRevokesBefore, _ := b.eventCounts()
	logf("[static] начально: static-a=%s static-b=%s", beforeA, beforeB)
	assertPartitioned("static: до рестарта", a, b)

	logf("[static] graceful close static-a (instance-id сохранён) — по семантике static membership LeaveGroupRequest НЕ отправляется")
	closeStart := time.Now()
	a.close()

	// Рестарт: НОВЫЙ клиент с ТЕМ ЖЕ instance-id, в пределах sessionTimeout.
	a2 := newMember("static-a-restarted", groupID, nil, kgo.InstanceID("static-a"), kgo.SessionTimeout(sessionTimeout))
	a2.waitFirstAssign(20 * time.Second)
	restartGap := time.Since(closeStart)
	waitUntilStable(15*time.Second, a2, b)

	afterA2, afterB := a2.snapshot(), b.snapshot()
	bAssignsAfter, bRevokesAfter, _ := b.eventCounts()

	logf("[static] рестарт static-a занял %s (< session.timeout=%s)", restartGap, sessionTimeout)
	logf("[static] после рестарта: static-a-restarted=%s (было у static-a: %s) static-b=%s", afterA2, beforeA, afterB)

	if !equalSets(beforeA, afterA2) {
		log.Fatalf("[assert] FAIL (static): рестартовавший статический член получил другие партиции: было %s, стало %s", beforeA, afterA2)
	}
	logf("[assert] OK (static): static-a-restarted вернул СЕБЕ ровно те же партиции: %s", afterA2)

	if bAssignsAfter != bAssignsBefore || bRevokesAfter != bRevokesBefore {
		log.Fatalf("[assert] FAIL (static): static-b пережил лишний revoke/assign во время рестарта static-a (assign %d->%d, revoke %d->%d)",
			bAssignsBefore, bAssignsAfter, bRevokesBefore, bRevokesAfter)
	}
	logf("[assert] OK (static): static-b НЕ получил ни одного revoke/assign во время рестарта static-a (группа не ребалансировалась)")

	a2.close()
	b.close()

	// --- Часть B: контраст — динамические (обычные) члены, та же процедура ---
	logf("[static] контраст: та же процедура, но БЕЗ instance-id (динамическое членство)")
	dynGroup := groupID + "-dyn"
	dc := newMember("dynamic-c", dynGroup, nil, kgo.SessionTimeout(sessionTimeout))
	dd := newMember("dynamic-d", dynGroup, nil, kgo.SessionTimeout(sessionTimeout))
	dc.waitFirstAssign(20 * time.Second)
	dd.waitFirstAssign(20 * time.Second)
	waitUntilStable(15*time.Second, dc, dd)

	ddAssignsBefore, ddRevokesBefore, _ := dd.eventCounts()
	logf("[dynamic] начально: dynamic-c=%s dynamic-d=%s", dc.snapshot(), dd.snapshot())

	dc.close() // без instance-id Close() ШЛЁТ LeaveGroupRequest -> немедленный ребаланс у dynamic-d
	dc2 := newMember("dynamic-c-restarted", dynGroup, nil, kgo.SessionTimeout(sessionTimeout))
	dc2.waitFirstAssign(20 * time.Second)
	waitUntilStable(15*time.Second, dc2, dd)

	ddAssignsAfter, ddRevokesAfter, _ := dd.eventCounts()
	logf("[dynamic] после рестарта dynamic-c: dynamic-c-restarted=%s dynamic-d=%s", dc2.snapshot(), dd.snapshot())

	if ddAssignsAfter == ddAssignsBefore && ddRevokesAfter == ddRevokesBefore {
		log.Fatalf("[assert] FAIL (dynamic-контраст): ожидался ребаланс у dynamic-d при рестарте dynamic-c (без static membership), но событий не было")
	}
	logf("[assert] OK (dynamic-контраст): dynamic-d получил revoke/assign при рестарте dynamic-c (assign %d->%d, revoke %d->%d) — в отличие от static-b выше",
		ddAssignsBefore, ddAssignsAfter, ddRevokesBefore, ddRevokesAfter)

	dc2.close()
	dd.close()
	close(stopProduce)

	fmt.Println("[static] сценарий завершён")
}
