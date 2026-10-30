package a

import "context"

func work(ctx context.Context) error { return ctx.Err() }

// Положительные случаи: функция уже получила ctx.

func Lost(ctx context.Context) error {
	return work(context.Background()) // want `context.Background\(\) в функции с параметром context.Context`
}

func LostTODO(ctx context.Context) error {
	return work(context.TODO()) // want `context.TODO\(\)`
}

func LostInClosure(ctx context.Context) {
	go func() {
		_ = work(context.Background()) // want `context.Background\(\)`
	}()
}

func LostSecondParam(name string, ctx context.Context) error {
	_ = name
	return work(context.Background()) // want `context.Background\(\)`
}

// Ctx — алиас: для go/types это *types.Alias, а не *types.Named.
type Ctx = context.Context

func LostAliased(ctx Ctx) error {
	return work(context.Background()) // want `context.Background\(\)`
}

// Литерал функции со своим параметром context.Context внутри функции без него.
func LostInLiteral() {
	f := func(ctx context.Context) error {
		return work(context.Background()) // want `context.Background\(\)`
	}
	_ = f
}

// Отрицательные случаи: правило обязано молчать.

func Passed(ctx context.Context) error { return work(ctx) }

func Detached(ctx context.Context) error { return work(context.WithoutCancel(ctx)) }

func NoParam() error { return work(context.Background()) }

func main() { _ = work(context.TODO()) }

type notContext interface{ Done() <-chan struct{} }

func LookAlike(c notContext) error {
	_ = c
	return work(context.Background())
}
