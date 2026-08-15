// batch.go демонстрирует батчинг из раздела «Батчинг и дебаунс» статьи.
//
// Batch собирает поток мелких элементов в пачки, чтобы амортизировать
// стоимость операции (один INSERT на 100 строк вместо 100 запросов). Условие
// сброса — по размеру maxSize ИЛИ по времени maxWait, что наступит раньше,
// иначе последние элементы залипнут в буфере. При закрытии входа остаток
// отдаётся финальным flush; таймер корректно останавливается через Stop.
// Проверка len(buf)==0 делает сброс устойчивым к устаревшему тику таймера.
package patterns

import (
	"context"
	"time"
)

// Batch группирует элементы in в пачки []T, отдавая пачку по достижении
// maxSize либо по истечении maxWait с момента первого элемента пачки.
// Владелец закрывает выход через defer close; на закрытии in отдаёт остаток.
func Batch[T any](ctx context.Context, in <-chan T, maxSize int, maxWait time.Duration) <-chan []T {
	out := make(chan []T)
	go func() {
		defer close(out)

		var buf []T
		timer := time.NewTimer(maxWait)
		timer.Stop() // стартуем без активного таймера.
		defer timer.Stop()

		flush := func() {
			if len(buf) == 0 {
				return // защита от «пустого» сброса по устаревшему тику.
			}
			select {
			case out <- buf:
			case <-ctx.Done():
			}
			buf = nil
			timer.Stop()
		}

		for {
			select {
			case item, ok := <-in:
				if !ok {
					flush() // входной канал закрыт — отдать остаток.
					return
				}
				if len(buf) == 0 {
					timer.Reset(maxWait) // отсчёт с первого элемента пачки.
				}
				buf = append(buf, item)
				if len(buf) >= maxSize {
					flush() // сброс по размеру.
				}
			case <-timer.C:
				flush() // сброс по времени.
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
