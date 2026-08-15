// errgroup.go демонстрирует golang.org/x/sync/errgroup: WithContext + SetLimit +
// TryGo.
//
// WithContext даёт производный ctx, который отменяется при первой же ошибке любой
// задачи — остальные задачи, слушающие <-gctx.Done(), завершаются досрочно.
// SetLimit ограничивает число одновременных горутин. TryGo пытается запустить
// задачу без блокировки: если лимит исчерпан, возвращает false, и вызывающий сам
// решает, что делать с непринятой задачей.
package patterns

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// ProcessAll обрабатывает inputs параллельно с ограничением limit одновременных
// горутин (через SetLimit) и блокирующим g.Go. Возвращает результаты по тем же
// индексам, что и inputs, либо первую ошибку — в этом случае gctx для остальных
// задач уже отменён.
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

// TryProcess пытается запустить задачу на каждый вход неблокирующим TryGo под
// лимитом limit. Индексы задач, которые не поместились под лимит (TryGo вернул
// false), собираются в rejected. Возвращает заполненные результаты, список
// отклонённых индексов и первую ошибку принятых задач.
func TryProcess[T, R any](
	ctx context.Context,
	limit int,
	inputs []T,
	fn func(context.Context, T) (R, error),
) (out []R, rejected []int, err error) {
	out = make([]R, len(inputs))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit) // TryGo имеет смысл только при заданном лимите.

	for i, in := range inputs {
		if !g.TryGo(func() error {
			r, fnErr := fn(gctx, in)
			if fnErr != nil {
				return fnErr
			}
			out[i] = r
			return nil
		}) {
			rejected = append(rejected, i) // лимит исчерпан — задача не принята.
		}
	}

	if waitErr := g.Wait(); waitErr != nil {
		return out, rejected, waitErr
	}
	return out, rejected, nil
}
