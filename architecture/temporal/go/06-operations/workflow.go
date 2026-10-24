package main

// Нагрузочный воркфлоу: N activity подряд, каждая занимает воркер на
// заметное время. Смысл — создать очередь activity task, которую воркер
// разбирает медленнее, чем она наполняется. Именно в этот момент растёт
// schedule-to-start: задача создана, но свободного воркера для неё нет.

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

type SlowActs struct {
	Work time.Duration
}

// Slow возвращает собственное ожидание в очереди: StartedTime − ScheduledTime.
// Обе отметки ставит сервер, поэтому величина не зависит ни от часов воркера,
// ни от того, как SDK агрегирует метрику. Это и есть schedule-to-start
// отдельной задачи — из них считаются среднее и честный максимум.
func (s *SlowActs) Slow(ctx context.Context, _ int) (time.Duration, error) {
	info := activity.GetInfo(ctx)
	waited := info.StartedTime.Sub(info.ScheduledTime)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(s.Work):
		return waited, nil
	}
}

// LoadWorkflow планирует все activity СРАЗУ, а потом ждёт их все. Так
// создаётся очередь: задач много, воркер разбирает их по своей ёмкости.
func LoadWorkflow(ctx workflow.Context, steps int) ([]time.Duration, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// ScheduleToStart намеренно велик: мы хотим ИЗМЕРИТЬ ожидание,
		// а не отбраковывать задачи по таймауту.
		ScheduleToStartTimeout: 10 * time.Minute,
		StartToCloseTimeout:    time.Minute,
	})
	var a *SlowActs
	futures := make([]workflow.Future, 0, steps)
	for i := 0; i < steps; i++ {
		futures = append(futures, workflow.ExecuteActivity(ctx, a.Slow, i))
	}
	waits := make([]time.Duration, 0, steps)
	for _, f := range futures {
		var w time.Duration
		if err := f.Get(ctx, &w); err != nil {
			return nil, err
		}
		waits = append(waits, w)
	}
	return waits, nil
}

// DayLongWorkflow — процесс, который логически идёт сутки. В проде его
// не дождаться; в тесте окружение перематывает время мгновенно.
func DayLongWorkflow(ctx workflow.Context) (string, error) {
	if err := workflow.Sleep(ctx, 24*time.Hour); err != nil {
		return "", err
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	})
	var a *SlowActs
	if err := workflow.ExecuteActivity(ctx, a.Slow, 1).Get(ctx, nil); err != nil {
		return "", err
	}
	return "сутки прошли", nil
}
