// Демо к статье «Распределённые блокировки»: почему без fencing-токена лок не
// защищает. Сценарий Клеппманна проигрывается дважды на одном ресурсе.
//
// Числа не нужны — важен исход: без fencing запоздавшая запись «уснувшего»
// владельца затирает свежие данные (порча), с fencing — отвергается. Запуск:
//
//	go run ./cmd/paused-owner
package main

import (
	"errors"
	"fmt"

	locking "tech.khorost/locking-cookbook"
)

func main() {
	fmt.Println("Сценарий: A берёт лок (токен 1) и засыпает; TTL истекает; B берёт")
	fmt.Println("лок (токен 2) и пишет; A просыпается и пишет со старым токеном 1.")
	fmt.Println()

	// Без fencing.
	un := &locking.UnfencedResource{}
	un.Write("B-data")  // B
	un.Write("A-stale") // A проснулся
	fmt.Printf("  без fencing: ресурс = %q  → данные B ПОТЕРЯНЫ (порча)\n", un.Value())

	// С fencing.
	fe := &locking.FencedResource{}
	_ = fe.Write(2, "B-data")
	err := fe.Write(1, "A-stale") // отвергнут
	fmt.Printf("  с fencing:   ресурс = %q  → запись A отвергнута (%v)\n", fe.Value(), errors.Is(err, locking.ErrStaleToken))

	fmt.Println("\nВывод: TTL-лок сам по себе не спасает от двойного владения — уснувший")
	fmt.Println("владелец может проснуться и записать. Защищает не лок, а fencing-токен")
	fmt.Println("на стороне РЕСУРСА: он отвергает всё, что старее уже принятого.")
}
