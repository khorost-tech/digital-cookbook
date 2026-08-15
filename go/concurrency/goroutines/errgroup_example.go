// errgroup_example.go демонстрирует errgroup.WithContext + SetLimit.
//
// Каждая задача обрабатывается в своей горутине под лимитом параллелизма.
// Первая же ошибка отменяет производный ctx — остальные задачи, слушающие
// <-ctx.Done(), завершаются досрочно. На успехе результаты заполняются строго
// по индексу: каждая горутина пишет в свою ячейку среза (out[i]), пересечений
// по индексам нет, поэтому запись без мьютекса свободна от гонок.
package goroutines

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// ProcessAll обрабатывает inputs параллельно с ограничением limit одновременных
// горутин. Возвращает срез результатов по тем же индексам, что и inputs, либо
// первую возникшую ошибку (в этом случае ctx для остальных задач уже отменён).
func ProcessAll[T, R any](
	ctx context.Context,
	limit int,
	inputs []T,
	fn func(context.Context, T) (R, error),
) ([]R, error) {
	out := make([]R, len(inputs))

	g, gctx := errgroup.WithContext(ctx)
	if limit > 0 {
		g.SetLimit(limit)
	}

	for i, in := range inputs {
		g.Go(func() error {
			r, err := fn(gctx, in)
			if err != nil {
				return err // первая ошибка отменит gctx для остальных.
			}
			out[i] = r // своя ячейка на горутину — гонки по записи нет.
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
