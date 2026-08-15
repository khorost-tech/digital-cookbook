// Настройка подключения к Temporal и воркера.
//
// Клиент SDK подключается к host-порту dev-сервера (localhost:7243 → в контейнере 7233).
// Воркер и клиент живут в одном процессе (см. main.go): воркер обслуживает
// task queue, клиент запускает воркфлоу. Активити держат общий in-memory Store,
// поэтому параллельный «читатель» в main видит то же состояние.
package main

import (
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

const (
	// HostPort — адрес frontend-а Temporal на хосте (маппинг 7243:7233 в compose).
	// Именно 127.0.0.1, а НЕ localhost: на Windows localhost резолвится сначала в
	// IPv6 [::1], а проброс порта Docker на этой машине по ::1 не отвечает (тайм-аут) —
	// та же особенность окружения, что отмечена в стенде kafka. IPv4 работает штатно.
	HostPort = "127.0.0.1:7243"
	// TaskQueue — общая очередь задач воркфлоу и активити саги.
	TaskQueue = "saga-task-queue"
)

// newClient открывает клиент Temporal к dev-серверу.
// Адрес можно переопределить переменной окружения TEMPORAL_HOSTPORT — это нужно,
// чтобы прогнать клиент ИЗНУТРИ контейнера на сети стенда (saga-temporal:7233):
// на этой Windows-машине проброс порта на хост из локально собранного Go-бинаря
// блокируется на уровне ОС/файрвола (см. README и аналогичную заметку в стенде kafka).
func newClient() (client.Client, error) {
	hostPort := HostPort
	if v := os.Getenv("TEMPORAL_HOSTPORT"); v != "" {
		hostPort = v
	}
	return client.Dial(client.Options{
		HostPort:  hostPort,
		Namespace: "default",
	})
}

// newWorker создаёт воркера, регистрирует воркфлоу и активити (привязанные к store).
func newWorker(c client.Client, store *Store) worker.Worker {
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(OrderSaga)
	w.RegisterActivity(&Activities{store: store})
	return w
}
