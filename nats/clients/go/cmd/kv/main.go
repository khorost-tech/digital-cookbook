// kv демонстрирует KV API пакета jetstream: put/get/watch поверх bucket'а,
// материализованного как stream KV_<bucket>. Запускать против
// nats/01-jetstream или nats/02-cluster.
package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nats-cookbook-go/internal/natsconn"
)

const bucketName = "config"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nc, err := natsconn.Connect("kv")
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		natsconn.Shutdown(shutCtx, nc)
	}()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream.New: %v", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: bucketName,
	})
	if err != nil {
		log.Fatalf("create kv bucket: %v", err)
	}
	log.Printf("kv bucket %q готов (материализован как stream KV_%s)", bucketName, bucketName)

	watchDone := make(chan struct{})
	go watchKey(ctx, kv, "feature.flag", watchDone)

	// Небольшая пауза, чтобы watcher успел подписаться до первого put —
	// иначе можно пропустить самое первое обновление.
	time.Sleep(200 * time.Millisecond)

	rev, err := kv.Put(ctx, "feature.flag", []byte("true"))
	if err != nil {
		log.Fatalf("put: %v", err)
	}
	log.Printf("put feature.flag=true (revision %d)", rev)

	entry, err := kv.Get(ctx, "feature.flag")
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	log.Printf("get feature.flag: %s @ revision %d", string(entry.Value()), entry.Revision())

	// Второй put — чтобы watcher увидел обновление, а не только начальное
	// состояние (Watch по умолчанию сразу присылает текущее значение).
	if _, err := kv.Put(ctx, "feature.flag", []byte("false")); err != nil {
		log.Fatalf("put #2: %v", err)
	}
	log.Println("put feature.flag=false")

	select {
	case <-watchDone:
	case <-time.After(3 * time.Second):
		log.Println("watch: не дождались второго обновления за таймаут")
	}
}

// watchKey подписывается на изменения одного ключа и печатает первые два
// события: начальное значение (Watch всегда присылает его первым) и
// следующее обновление. watcher.Updates() закрывается nil-записью, когда
// история "довычитана" и начинаются живые события — здесь это не
// разбирается отдельно, чтобы не усложнять демо.
func watchKey(ctx context.Context, kv jetstream.KeyValue, key string, done chan<- struct{}) {
	watcher, err := kv.Watch(ctx, key)
	if err != nil {
		log.Printf("watch: %v", err)
		close(done)
		return
	}
	defer watcher.Stop()

	seen := 0
	for {
		select {
		case entry, ok := <-watcher.Updates():
			if !ok {
				close(done)
				return
			}
			if entry == nil {
				// Разделитель между "историческими" и "живыми" обновлениями —
				// не отдельное значение ключа, пропускаем.
				continue
			}
			seen++
			log.Printf("watch: %s = %s (revision %d)", entry.Key(), string(entry.Value()), entry.Revision())
			if seen >= 2 {
				close(done)
				return
			}
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				log.Printf("watch: контекст завершён: %v", ctx.Err())
			}
			close(done)
			return
		}
	}
}
