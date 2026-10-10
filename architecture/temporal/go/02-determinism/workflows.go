package main

// Два воркфлоу с ОДИНАКОВОЙ бизнес-логикой и разной дисциплиной
// детерминизма. Разница ровно в том, откуда берётся решение о ветке.

import (
	"os"
	"time"

	"go.temporal.io/sdk/workflow"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

// BrokenWorkflow нарушает детерминизм: решение о ветке принимается по
// состоянию, которого НЕТ в истории — по окружению процесса. При первом
// исполнении всё проходит: расхождению не с чем сравниваться. Оно
// проявится при replay, когда окружение окажется другим.
//
// Почему именно переменная окружения, а не time.Now() и rand. Смысл
// нарушения тот же — воркфлоу читает внешнее изменяемое состояние, — но
// воспроизводимость разная. С rand ветка при проигрывании совпадает с
// записанной чаще, чем расходится, и контрпример срабатывает через раз:
// первый прогон этого профиля дал именно чистый replay сломанного
// воркфлоу. Контрпример, который воспроизводится через раз, доказывает не
// то, что нужно. Здесь источник расхождения выбирается явно и
// срабатывает всегда.
func BrokenWorkflow(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	// НАРУШЕНИЕ: чтение внешнего состояния прямо в теле воркфлоу.
	branchLong := os.Getenv("DEMO_BRANCH") == "long"

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	if branchLong {
		// Лишний шаг, которого может не быть при повторном проигрывании:
		// набор команд разойдётся с историей.
		var rid string
		if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
			return "", err
		}
		return rid, nil
	}
	return "короткая ветка", nil
}

// FixedWorkflow — та же логика на детерминированных примитивах.
// workflow.Now() возвращает время из ИСТОРИИ, а SideEffect записывает
// результат недетерминированного вычисления в историю при первом
// исполнении и при replay берёт записанное.
func FixedWorkflow(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var branchLong bool
	enc := workflow.SideEffect(ctx, func(workflow.Context) any {
		return time.Now().UnixNano()%2 == 0
	})
	if err := enc.Get(&branchLong); err != nil {
		return "", err
	}
	// workflow.Now() детерминирован: одно и то же значение при replay.
	_ = workflow.Now(ctx)

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	if branchLong {
		var rid string
		if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
			return "", err
		}
		return rid, nil
	}
	return "короткая ветка", nil
}

// LongWorkflow — воркфлоу с управляемой длиной истории: N шагов, каждый
// пишет в историю несколько событий. Используется для замера цены replay.
func LongWorkflow(ctx workflow.Context, steps int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities
	for i := 0; i < steps; i++ {
		var ok bool
		if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, "шаг").Get(ctx, &ok); err != nil {
			return i, err
		}
	}
	return steps, nil
}
