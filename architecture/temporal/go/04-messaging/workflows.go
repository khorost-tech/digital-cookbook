package main

// Четыре способа связи воркфлоу с внешним миром плюс Continue-As-New.
//
// Ключевое различие, ради которого профиль существует: Signal ничего не
// возвращает и ничего не валидирует, Query ничего не меняет и не пишет в
// историю, Update делает и то и другое — валидирует и отвечает.

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"
)

const (
	SignalAdd    = "add-item"
	QueryTotal   = "total"
	UpdateSetCap = "set-cap"
)

// CartState — состояние, переносимое через Continue-As-New.
type CartState struct {
	Total      int  `json:"total"`
	Cap        int  `json:"cap"`
	Cycles     int  `json:"cycles"`
	MaxCycles  int  `json:"maxCycles"`
	Generation int  `json:"generation"`
	WithCAN    bool `json:"withCan"`
}

// CartWorkflow — долгоживущий «агрегат», управляемый сообщениями.
//
// MaxCycles ограничивает жизнь одной итерации: по достижении лимита
// воркфлоу либо завершается (WithCAN=false), либо перезапускает себя
// с чистой историей, перенеся только нужное состояние (WithCAN=true).
func CartWorkflow(ctx workflow.Context, state CartState) (CartState, error) {
	logger := workflow.GetLogger(ctx)

	// Query: только читает. Обработчик не меняет состояние и не пишет
	// в историю — поэтому его можно дёргать сколько угодно.
	if err := workflow.SetQueryHandler(ctx, QueryTotal, func() (int, error) {
		return state.Total, nil
	}); err != nil {
		return state, err
	}

	// Update: синхронная мутация с ВАЛИДАЦИЕЙ и ответом вызывающему.
	// Валидатор выполняется до записи в историю: отклонённый update
	// историю не засоряет.
	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateSetCap,
		func(_ workflow.Context, newCap int) (int, error) {
			old := state.Cap
			state.Cap = newCap
			logger.Info("cap изменён", "old", old, "new", newCap)
			return old, nil
		},
		workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, newCap int) error {
				if newCap <= 0 {
					return errors.New("cap должен быть положительным")
				}
				return nil
			},
		}); err != nil {
		return state, err
	}

	ch := workflow.GetSignalChannel(ctx, SignalAdd)
	for state.Cycles < state.MaxCycles {
		var amount int
		ch.Receive(ctx, &amount)
		state.Cycles++
		if state.Total+amount > state.Cap {
			logger.Info("превышен cap, позиция отклонена", "amount", amount, "cap", state.Cap)
			continue
		}
		state.Total += amount
	}

	if !state.WithCAN {
		logger.Info("лимит итераций исчерпан, завершаемся", "total", state.Total)
		return state, nil
	}

	// Continue-As-New: то же логическое исполнение продолжается новым
	// запуском с ПУСТОЙ историей. Переносим только состояние, которое
	// нужно дальше, — история предыдущего запуска остаётся позади.
	next := state
	next.Cycles = 0
	next.Generation++
	logger.Info("Continue-As-New", "generation", next.Generation, "total", next.Total)
	return state, workflow.NewContinueAsNewError(ctx, CartWorkflow, next)
}

// ParentWorkflow — декомпозиция на дочерние воркфлоу. Каждый child
// имеет собственную историю и версионируется независимо от родителя.
func ParentWorkflow(ctx workflow.Context, children int) ([]string, error) {
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowExecutionTimeout: 5 * time.Minute,
	})
	futures := make([]workflow.ChildWorkflowFuture, 0, children)
	for i := 0; i < children; i++ {
		futures = append(futures, workflow.ExecuteChildWorkflow(ctx, ChildWorkflow, i))
	}
	results := make([]string, 0, children)
	for _, f := range futures {
		var r string
		if err := f.Get(ctx, &r); err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

// ChildWorkflow — дочерний воркфлоу с собственной историей.
func ChildWorkflow(ctx workflow.Context, idx int) (string, error) {
	if err := workflow.Sleep(ctx, time.Duration(idx+1)*100*time.Millisecond); err != nil {
		return "", err
	}
	return fmt.Sprintf("child-%d готов", idx), nil
}
