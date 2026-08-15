// ordone.go демонстрирует вспомогательный паттерн or-done-channel из раздела
// «Вспомогательные каналы: or-done, tee, bridge» статьи.
//
// OrDone инкапсулирует «читать канал, пока он не закрыт или ctx не отменён»,
// позволяя потребителю писать обычный for range вместо вложенного select на
// <-ctx.Done() при каждом чтении. Горутина-владелец закрывает выход через
// defer close; отправка защищена select с <-ctx.Done(), поэтому досрочная
// отмена не оставляет зависших горутин.
package patterns

import "context"

// OrDone оборачивает вход in, отдавая тот же поток элементов, но завершаясь
// при закрытии in либо при отмене ctx. Даёт чистый for range на стороне
// потребителя без ручного select.
func OrDone[T any](ctx context.Context, in <-chan T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out) // владелец канала закрывает его сам.
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-in:
				if !ok {
					return // вход исчерпан — закрываем выход.
				}
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
