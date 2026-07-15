package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// runMongo: multi-document transaction (перевод между двумя документами).
// Атомарность ОДНОГО документа в MongoDB есть всегда ($inc атомарен). Но инвариант «сумма
// балансов постоянна» связывает ДВА документа — для него нужна явная multi-document транзакция
// поверх snapshot isolation (доступна на replica set). WithTransaction сам ретраит транзитные
// конфликты (WriteConflict под конкуренцией). Здесь: 16 воркеров × 50 переводов A→B по 1;
// инвариант A+B=1000 должен сохраниться.
func runMongo() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// directConnection=true: подключаемся напрямую к узлу, не пытаясь резолвить имена членов
	// replica set из его конфигурации (single-node RS в docker отдаёт клиенту внутреннее имя
	// контейнера). Узел — PRIMARY rs0, поэтому multi-document транзакции работают.
	uri := envOr("MONGO_URI", "mongodb://localhost:27027/?directConnection=true")
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Println("mongo connect:", err)
		return
	}
	defer client.Disconnect(ctx)

	coll := client.Database("demo").Collection("accounts")
	// seed
	_, _ = coll.DeleteMany(ctx, bson.M{})
	_, _ = coll.InsertMany(ctx, []any{
		bson.M{"_id": "A", "balance": 1000},
		bson.M{"_id": "B", "balance": 0},
	})

	const workers, iters = 16, 50
	// транзакция читает snapshot и пишет с majority — консистентно
	txnOpts := options.Transaction().
		SetReadConcern(readconcern.Snapshot()).
		SetWriteConcern(writeconcern.Majority())

	transfer := func(sess mongo.Session) error {
		_, err := sess.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
			if _, e := coll.UpdateOne(sc, bson.M{"_id": "A"}, bson.M{"$inc": bson.M{"balance": -1}}); e != nil {
				return nil, e
			}
			if _, e := coll.UpdateOne(sc, bson.M{"_id": "B"}, bson.M{"$inc": bson.M{"balance": 1}}); e != nil {
				return nil, e
			}
			return nil, nil
		}, txnOpts)
		return err
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := client.StartSession()
			if err != nil {
				return
			}
			defer sess.EndSession(ctx)
			for i := 0; i < iters; i++ {
				if err := transfer(sess); err != nil {
					fmt.Println("transfer:", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	var a, b struct {
		Balance int `bson:"balance"`
	}
	_ = coll.FindOne(ctx, bson.M{"_id": "A"}).Decode(&a)
	_ = coll.FindOne(ctx, bson.M{"_id": "B"}).Decode(&b)
	total := workers * iters
	fmt.Printf("mongo: %d переводов A→B по 1. A=%d B=%d sum=%d (инвариант sum=1000 %s)\n",
		total, a.Balance, b.Balance, a.Balance+b.Balance,
		okMark(a.Balance+b.Balance == 1000))
}

func okMark(ok bool) string {
	if ok {
		return "сохранён ✓"
	}
	return "НАРУШЕН ✗"
}
