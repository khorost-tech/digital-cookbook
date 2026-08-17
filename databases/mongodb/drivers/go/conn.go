// Package mongoconn — общий Go-хелпер подключения к MongoDB, разделяемый
// всеми стендами cookbook серии "MongoDB: глубокое погружение" (modeling,
// wiredtiger, indexes, aggregation, replication, sharding, ops), чтобы не
// дублировать boilerplate ApplyURI/Ping/таймаут в каждом модуле.
package mongoconn

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// pingTimeout — таймаут на первичный Ping после подключения. Стенды сами
// решают таймауты своих последующих запросов; это только про факт того,
// что сервер вообще жив на момент Connect.
const pingTimeout = 10 * time.Second

// Connect подключается к MongoDB по uri (например
// "mongodb://localhost:27017/?replicaSet=rs0" или мультихостовый URI для
// sharded-кластера через mongos), проверяет соединение Ping и возвращает
// готовый к использованию клиент. Вызывающий код обязан вызвать
// client.Disconnect(ctx) при завершении работы.
func Connect(uri string) (*mongo.Client, error) {
	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongoconn.Connect: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongoconn.Connect: ping %s: %w", uri, err)
	}
	return client, nil
}

// MustEnv читает обязательную переменную окружения key и паникует, если
// она не задана или пуста — fail-loud для конфигурации стендов. Env vars —
// единственный механизм конфигурации проекта (см. корневой CLAUDE.md).
func MustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("mongoconn.MustEnv: обязательная переменная окружения %s не задана", key))
	}
	return v
}
