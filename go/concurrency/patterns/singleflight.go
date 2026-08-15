// singleflight.go демонстрирует golang.org/x/sync/singleflight: схлопывание
// конкурентных одинаковых вызовов в один.
//
// Do по ключу key гарантирует, что fn для этого ключа выполняется один раз, пока
// длится вызов: все конкурентные вызовы с тем же ключом ждут единственного
// исполнения и получают общий результат (флаг shared = true). Это классическая
// защита от «промаха кеша толпой» (cache stampede). DoChan даёт асинхронный
// вариант через канал, Forget сбрасывает запомненный in-flight вызов, позволяя
// следующему вызову исполнить fn заново.
package patterns

import (
	"context"

	"golang.org/x/sync/singleflight"
)

// Coalesce выполняет fn под защитой singleflight по ключу key. Конкурентные
// вызовы с тем же ключом схлопнутся в одно исполнение fn. Флаг shared сообщает,
// был ли результат разделён более чем одним вызывающим.
func Coalesce[R any](
	g *singleflight.Group,
	key string,
	fn func() (R, error),
) (value R, shared bool, err error) {
	v, err, shared := g.Do(key, func() (any, error) {
		return fn()
	})
	if err != nil {
		return value, shared, err
	}
	return v.(R), shared, nil
}

// CoalesceChan — асинхронный вариант через DoChan. Возвращает результат по ключу
// key, ожидая либо готовности singleflight-вызова, либо отмены ctx. Само
// исполнение fn не прерывается отменой ctx (это ограничение singleflight —
// разделяемый вызов один на всех), но ожидающий вызывающий отвязывается по ctx.
func CoalesceChan[R any](
	ctx context.Context,
	g *singleflight.Group,
	key string,
	fn func() (R, error),
) (value R, shared bool, err error) {
	ch := g.DoChan(key, func() (any, error) {
		return fn()
	})
	select {
	case <-ctx.Done():
		return value, false, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return value, res.Shared, res.Err
		}
		return res.Val.(R), res.Shared, nil
	}
}

// Forget сбрасывает запомненный in-flight вызов по ключу key: следующий Do/DoChan
// с этим ключом исполнит fn заново, не присоединяясь к текущему исполнению.
func Forget(g *singleflight.Group, key string) {
	g.Forget(key)
}
