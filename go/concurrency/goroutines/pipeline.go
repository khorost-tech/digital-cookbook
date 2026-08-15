// pipeline.go демонстрирует конвейер стадий на каналах: generator → stage → sink.
// Закрытие входного канала каскадно завершает все стадии (каждая стадия закрывает
// свой выход по исчерпании входа). Досрочная остановка — через отмену context:
// каждая стадия слушает <-ctx.Done() и на приёме, и на отправке, поэтому не
// зависает и не оставляет горутин при раннем выходе потребителя.
package goroutines

import "context"

// Generator порождает стадию-источник: выдаёт элементы values по одному в
// выходной канал и закрывает его по исчерпании или при отмене ctx.
func Generator[T any](ctx context.Context, values ...T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
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

// Stage — промежуточная стадия: применяет fn к каждому элементу входного канала
// и отправляет результат дальше. Закрывает выход, когда вход исчерпан или ctx
// отменён; защищён <-ctx.Done() на обоих концах.
func Stage[T, R any](ctx context.Context, in <-chan T, fn func(T) R) <-chan R {
	out := make(chan R)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-in:
				if !ok {
					return
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

// Sink дренирует финальный канал в срез. Возвращает управление, когда вход
// закрыт (каскадное завершение) или ctx отменён (досрочная остановка).
func Sink[T any](ctx context.Context, in <-chan T) []T {
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
