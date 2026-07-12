package main

// worker.go — подключение к Temporal и запуск воркера.
//
// Воркер — процесс, который забирает задачи из очереди задач (task queue) и
// исполняет код workflow и activities. В этом самодостаточном стенде воркер
// поднимается ВНУТРИ того же процесса, что и клиент, запускающий workflow, —
// так `go run .` делает всё сам. В проде воркер обычно живёт отдельным
// процессом (или несколькими), но API ровно то же.

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// taskQueue — очередь задач, через которую клиент отдаёт workflow воркеру.
// Имя произвольное, но клиент и воркер должны использовать одно и то же.
const taskQueue = "order-fulfillment"

// dialTemporal подключается к frontend-сервису Temporal по gRPC.
func dialTemporal(hostPort string) (client.Client, error) {
	return client.Dial(client.Options{HostPort: hostPort})
}

// startWorker создаёт воркер на очереди taskQueue, регистрирует в нём workflow и
// все четыре activities и запускает его НЕ блокируя (w.Start()). Остановить —
// w.Stop() (вызывающий делает defer).
func startWorker(c client.Client) (worker.Worker, error) {
	w := worker.New(c, taskQueue, worker.Options{})

	// Один workflow — весь сценарий целиком (см. orchestration.go).
	w.RegisterWorkflow(OrderWorkflow)

	// Четыре activities — по одному шагу на каждую.
	w.RegisterActivity(TakePayment)
	w.RegisterActivity(ReserveStock)
	w.RegisterActivity(CreateShipment)
	w.RegisterActivity(NotifyCustomer)

	if err := w.Start(); err != nil {
		return nil, err
	}
	return w, nil
}
