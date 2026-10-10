package main

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ChargeWorkflow вызывает одну из двух зарядных activity. Retry-политика
// одинакова для обеих: различается только сама activity.
func ChargeWorkflow(ctx workflow.Context, safe bool, orderID string, amount int64) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    200 * time.Millisecond,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Second,
			MaximumAttempts:    5,
		},
	})
	var a *Acts
	if safe {
		return workflow.ExecuteActivity(ctx, a.ChargeIdempotent, orderID, amount).Get(ctx, nil)
	}
	return workflow.ExecuteActivity(ctx, a.ChargeUnsafe, orderID, amount).Get(ctx, nil)
}

// ImportWorkflow — долгая activity с heartbeat.
//
// HeartbeatTimeout — единственный таймаут, который обнаруживает зависшую
// долгую задачу: StartToClose здесь заведомо велик, потому что работа
// действительно длинная, и на роль детектора зависания он не годится.
func ImportWorkflow(ctx workflow.Context, jobID string, total int) (int, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 1.0,
			MaximumAttempts:    5,
		},
	})
	var a *Acts
	var done int
	err := workflow.ExecuteActivity(ctx, a.LongImport, jobID, total).Get(ctx, &done)
	return done, err
}

// PingWorkflow — N вызовов минимальной activity обычным способом или
// как local activity. Различается РОВНО способ вызова.
func PingWorkflow(ctx workflow.Context, calls int, local bool) error {
	var a *Acts
	if local {
		lctx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
			StartToCloseTimeout: 5 * time.Second,
		})
		for i := 0; i < calls; i++ {
			if err := workflow.ExecuteLocalActivity(lctx, a.Ping).Get(lctx, nil); err != nil {
				return err
			}
		}
		return nil
	}
	rctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
	})
	for i := 0; i < calls; i++ {
		if err := workflow.ExecuteActivity(rctx, a.Ping).Get(rctx, nil); err != nil {
			return err
		}
	}
	return nil
}
