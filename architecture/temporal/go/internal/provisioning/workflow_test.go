package provisioning_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

// Подтверждённый путь: приходит сигнал approved=true, отрабатывает Allocate,
// компенсация НЕ вызывается.
func TestProvisioningApproved(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(&provisioning.Activities{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(provisioning.SignalConfirmation,
			provisioning.ConfirmationSignal{Approved: true, By: "тест"})
	}, 20*time.Second)

	env.ExecuteWorkflow(provisioning.ProvisioningWorkflow, provisioning.Request{
		Resource:       "gpu-node-7",
		ConfirmTimeout: 5 * time.Minute,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res provisioning.Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "allocated", res.Outcome)
	require.Equal(t, "res-gpu-node-7-001", res.ReservationID)
}

// Таймаут ожидания: сигнал не приходит вовсе — отрабатывает компенсация.
// Тест НЕ ждёт пять минут: тестовое окружение перематывает время само.
func TestProvisioningTimesOutIntoCompensation(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(&provisioning.Activities{})

	env.ExecuteWorkflow(provisioning.ProvisioningWorkflow, provisioning.Request{
		Resource:       "gpu-node-7",
		ConfirmTimeout: 5 * time.Minute,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res provisioning.Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "cancelled_timeout", res.Outcome)
}

// Явный отказ отличается от таймаута: исход другой, компенсация та же.
func TestProvisioningRejected(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(&provisioning.Activities{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(provisioning.SignalConfirmation,
			provisioning.ConfirmationSignal{Approved: false, By: "тест"})
	}, 20*time.Second)

	env.ExecuteWorkflow(provisioning.ProvisioningWorkflow, provisioning.Request{
		Resource:       "gpu-node-7",
		ConfirmTimeout: 5 * time.Minute,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res provisioning.Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "cancelled_rejected", res.Outcome)
}
