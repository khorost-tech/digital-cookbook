// bridge.go демонстрирует вспомогательный паттерн bridge-channel из раздела
// «Вспомогательные каналы: or-done, tee, bridge» статьи.
//
// Bridge превращает «канал каналов» (<-chan <-chan T) в один плоский поток,
// последовательно вычитывая каждый вложенный канал до его закрытия и переходя
// к следующему. Полезно, когда стадии сами порождают каналы. Построен поверх
// OrDone: и внешний, и вложенные каналы читаются с защитой по <-ctx.Done().
package patterns

import "context"

// Bridge разворачивает поток каналов chanStream в единый выходной канал.
// Владелец закрывает выход через defer close, когда chanStream исчерпан или
// ctx отменён. Отправка защищена select с <-ctx.Done().
func Bridge[T any](ctx context.Context, chanStream <-chan <-chan T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
		// Внешний поток каналов читаем через OrDone — чистый range с отменой.
		for stream := range OrDone(ctx, chanStream) {
			// Каждый вложенный канал так же вычитываем через OrDone.
			for v := range OrDone(ctx, stream) {
				select {
				case <-ctx.Done():
					return
				case out <- v:
				}
			}
		}
	}()
	return out
}
