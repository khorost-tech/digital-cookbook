// pipeline.go демонстрирует конвейер стадий на каналах с отменой по context.
//
// Конвейер: source → stage → stage → … Каждая стадия владеет своим выходным
// каналом и закрывает его через defer close по исчерпании входа или при отмене
// ctx. И приём, и отправка обёрнуты в select с <-ctx.Done(), поэтому досрочная
// остановка потребителя не оставляет зависших горутин.
package patterns

import "context"

// Source порождает стадию-источник: по одному выдаёт элементы values в выходной
// канал и закрывает его по исчерпании или при отмене ctx.
func Source[T any](ctx context.Context, values ...T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out) // владелец канала закрывает его сам.
		for _, v := range values {
			select {
			case <-ctx.Done():
				return
			case out <- v:
			}
		}
	}()
	return out
}

// PipeStage — промежуточная стадия: применяет fn к каждому элементу входа и
// отправляет результат дальше. Закрывает выход по исчерпании входа или отмене
// ctx; защищён <-ctx.Done() на обоих концах.
func PipeStage[T, R any](ctx context.Context, in <-chan T, fn func(T) R) <-chan R {
	out := make(chan R)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-in:
				if !ok {
					return // вход исчерпан — каскадно закрываем выход.
				}
				select {
				case <-ctx.Done():
					return
				case out <- fn(v):
				}
			}
		}
	}()
	return out
}

// Drain дренирует финальный канал в срез. Возвращает управление по закрытию
// входа (нормальное завершение) или при отмене ctx (досрочная остановка).
func Drain[T any](ctx context.Context, in <-chan T) []T {
	var collected []T
	for {
		select {
		case <-ctx.Done():
			return collected
		case v, ok := <-in:
			if !ok {
				return collected
			}
			collected = append(collected, v)
		}
	}
}
