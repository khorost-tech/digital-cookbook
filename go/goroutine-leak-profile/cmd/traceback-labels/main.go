// Новинка 1.27: заголовок трейсбека содержит pprof-метки горутины — но только
// для модулей с директивой go 1.27 и выше. Отключается GODEBUG=tracebacklabels=0.
//
// Проверяется наблюдением: помеченная горутина паникует, и метка обязана
// появиться в выводе. Паника здесь — намеренный способ увидеть трейсбек, а не
// ошибка программы: ненулевой код возврата процесса — ожидаемый результат.
package main

import (
	"context"
	"runtime/pprof"
	"time"
)

func main() {
	labels := pprof.Labels("worker", "payment-processor", "shard", "7")
	pprof.Do(context.Background(), labels, func(ctx context.Context) {
		go func() {
			time.Sleep(100 * time.Millisecond)
			panic("демонстрация трейсбека с метками")
		}()
		time.Sleep(time.Second)
	})
}
