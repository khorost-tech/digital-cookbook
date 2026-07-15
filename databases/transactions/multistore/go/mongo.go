package main

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDB: атомарность одного документа — findOneAndUpdate с условием balance>0. Проверка и
// декремент внутри одного документа атомарны без явной транзакции; для инварианта в пределах
// одного документа этого достаточно.
type mongoStore struct {
	client *mongo.Client
	coll   *mongo.Collection
}

func newMongo() (Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().
		ApplyURI(envOr("MONGO_URI", "mongodb://localhost:27028/?directConnection=true")).
		SetMaxPoolSize(48))
	if err != nil {
		return nil, err
	}
	return &mongoStore{client, client.Database("demo").Collection("accounts")}, nil
}

func (s *mongoStore) Name() string { return "mongo" }

func (s *mongoStore) Reset(ctx context.Context, budget int64) error {
	_, err := s.coll.UpdateOne(ctx, bson.M{"_id": "acct"},
		bson.M{"$set": bson.M{"balance": budget}}, options.Update().SetUpsert(true))
	return err
}

func (s *mongoStore) Decrement(ctx context.Context) (bool, int64) {
	res := s.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": "acct", "balance": bson.M{"$gt": 0}},
		bson.M{"$inc": bson.M{"balance": -1}})
	if err := res.Err(); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) { // условие balance>0 не выполнено
			return false, 0
		}
		return false, 0
	}
	return true, 0
}

func (s *mongoStore) Final(ctx context.Context) int64 {
	var d struct {
		Balance int64 `bson:"balance"`
	}
	_ = s.coll.FindOne(ctx, bson.M{"_id": "acct"}).Decode(&d)
	return d.Balance
}

func (s *mongoStore) Close() { _ = s.client.Disconnect(context.Background()) }
