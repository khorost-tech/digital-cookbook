package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
)

// printCompactState печатает ВСЕ записи, реально читаемые сейчас в топике
// (от earliest до latest), с явной пометкой tombstone-записей. До компакции
// это покажет полную историю версий на каждый ключ; после — по одной записи
// (последней) на живой ключ и НИ ОДНОЙ на tombstone-ключ.
func printCompactState(label string, recv []recvRecord) {
	fmt.Printf("\n[compact-consume] %s: всего читаемо %d записей\n", label, len(recv))
	byKey := map[string][]recvRecord{}
	var order []string
	for _, r := range recv {
		if _, ok := byKey[r.key]; !ok {
			order = append(order, r.key)
		}
		byKey[r.key] = append(byKey[r.key], r)
	}
	sort.Strings(order)
	// filler-ключи (roll-filler-NNNN, техническая нагрузка, форсирующая roll
	// сегмента) печатаем одной сводной строкой, а не по одному — их сотни, и
	// каждый уникален по определению (см. producer.go:produceFiller), так что
	// построчный вывод не несёт содержательной информации, только раздувает лог.
	fillerCount := 0
	for _, k := range order {
		if strings.HasPrefix(k, "roll-filler-") {
			fillerCount += len(byKey[k])
			continue
		}
		var parts []string
		for _, r := range byKey[k] {
			parts = append(parts, fmt.Sprintf("offset=%d:%s", r.offset, truncateValue(valueLabel(r.value))))
		}
		fmt.Printf("  key=%-16s (%d записей): %s\n", k, len(byKey[k]), strings.Join(parts, " | "))
	}
	if fillerCount > 0 {
		fmt.Printf("  (+ %d filler-записей roll-filler-*, по одной уникальной на ключ, не показаны построчно)\n", fillerCount)
	}
}

// truncateValue — сокращает длинные значения для читаемого вывода отчёта
// (pad-bytes стенда может доходить до нескольких KB на запись, чтобы
// управляемо занимать место на диске под segment.bytes roll — печатать это
// построчно нечитаемо). Ассерты (assertCompacted) работают с ПОЛНЫМ значением,
// это чисто вывод для отчёта/README.
func truncateValue(v string) string {
	const maxLen = 48
	if v == "<tombstone/null>" || len(v) <= maxLen {
		return v
	}
	return fmt.Sprintf("%s...(len=%d)", v[:maxLen], len(v))
}

// assertCompacted — fail-loud проверка финального состояния после компакции:
//   - каждый "боевой" (не tombstone) ключ из keys встречается РОВНО один раз
//     и его значение — версия последнего раунда (lastRound);
//   - каждый tombstone-ключ ОТСУТСТВУЕТ полностью (ни значения, ни самого
//     tombstone-маркера — обе стадии компакции: сначала схлопнули старые
//     версии, затем удалили сам tombstone после delete.retention.ms).
//
// filler-ключи (roll-filler-*) в проверке не участвуют — они технические
// (форсируют roll сегмента), у каждого свой уникальный ключ, поэтому
// компакция их не трогает по определению (уникальный ключ = 1 версия и без
// компакции); их наличие в выводе — не баг, а ожидаемый шум.
func assertCompacted(recv []recvRecord, keys []string, tombstoneKeys []string, lastRound int) {
	byKey := map[string][]recvRecord{}
	for _, r := range recv {
		byKey[r.key] = append(byKey[r.key], r)
	}

	tomb := map[string]bool{}
	for _, k := range tombstoneKeys {
		tomb[k] = true
	}

	marker := fmt.Sprintf("-round%d-", lastRound)

	for _, k := range keys {
		if tomb[k] {
			if recs, ok := byKey[k]; ok {
				log.Fatalf("[assert] FAIL: tombstone-ключ %s всё ещё присутствует после компакции (%d записей: %+v)", k, len(recs), recs)
			}
			continue
		}
		recs, ok := byKey[k]
		if !ok {
			log.Fatalf("[assert] FAIL: живой ключ %s ПОЛНОСТЬЮ отсутствует после компакции (должен остаться с последним значением)", k)
		}
		if len(recs) != 1 {
			log.Fatalf("[assert] FAIL: живой ключ %s встречен %d раз(а) после компакции (ожидалась ровно 1 — компакция не отработала)", k, len(recs))
		}
		v := recs[0].value
		if v == nil {
			log.Fatalf("[assert] FAIL: живой ключ %s имеет tombstone-значение после компакции — не должен был получать tombstone", k)
		}
		if !strings.Contains(*v, marker) {
			log.Fatalf("[assert] FAIL: живой ключ %s — значение %q не содержит маркер последнего раунда %q (осталась СТАРАЯ версия, не последняя)", k, *v, marker)
		}
	}
	fmt.Printf("[assert] OK: %d живых ключей — ровно по 1 записи (последняя версия, маркер %q); %d tombstone-ключей — отсутствуют полностью\n",
		len(keys)-len(tombstoneKeys), marker, len(tombstoneKeys))
}
