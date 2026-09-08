// Демо к статье «Resilience-паттерны»: retry-шторм и retry-бюджет.
//
// Зависимость лежит (каждый вызов падает мгновенно). N клиентов повторяют по
// maxRetries раз. Наивно это умножает нагрузку на мёртвую зависимость: вместо N
// вызовов она получает N×(1+maxRetries) — и не встаёт как раз потому, что её
// добивают повторами. Retry-бюджет (доля повторов к обычным запросам) ограничивает
// усиление до небольшого множителя.
//
// Числа детерминированы (считаются вызовы, без таймингов). Запуск:
//
//	go run ./cmd/retrystorm
package main

import (
	"fmt"

	"tech.khorost/patterns-cookbook/retry"
)

const (
	requests   = 1000 // сколько обычных запросов
	maxRetries = 3    // сколько раз наивно повторять при отказе
	budgetRate = 0.1  // допустимая доля повторов к обычным запросам
)

func main() {
	fmt.Printf("зависимость лежит; %d запросов, наивно по %d повтора\n\n", requests, maxRetries)

	// dead — счётчик обращений к мёртвой зависимости; всегда «ошибка».
	naive := 0
	call := func(counter *int) { *counter++ } // сам вызов = удар по зависимости

	// Наивный повтор: на каждый отказ — maxRetries добавочных ударов.
	for i := 0; i < requests; i++ {
		call(&naive)
		for r := 0; r < maxRetries; r++ {
			call(&naive) // повтор без ограничений
		}
	}

	// С бюджетом: повтор только если бюджет позволяет.
	budgeted := 0
	b := retry.NewBudget(budgetRate, 100)
	for i := 0; i < requests; i++ {
		call(&budgeted)
		b.OnRequest()
		for r := 0; r < maxRetries; r++ {
			if !b.TryRetry() {
				break // бюджет исчерпан — шторм гаснет
			}
			call(&budgeted)
		}
	}

	fmt.Printf("  наивный повтор:  %d ударов по зависимости  (×%.1f от нагрузки)\n",
		naive, float64(naive)/float64(requests))
	fmt.Printf("  с retry-бюджетом: %d ударов по зависимости  (×%.2f)\n",
		budgeted, float64(budgeted)/float64(requests))
	fmt.Println("\nВывод: наивный повтор умножает нагрузку на лежащую зависимость и мешает")
	fmt.Println("ей встать; бюджет ограничивает усиление до доли обычного трафика.")
}
