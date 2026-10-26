package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/kurrent-io/KurrentDB-Client-Go/kurrentdb"
)

// NewKurrentClient — клиент к KurrentDB из строки подключения (ESDB_URL).
func NewKurrentClient(url string) (*kurrentdb.Client, error) {
	cfg, err := kurrentdb.ParseConnectionString(url)
	if err != nil {
		return nil, err
	}
	return kurrentdb.NewClient(cfg)
}

// AppendKurrent — тот же сценарий, что AppendEvents: запись с ожидаемой
// версией. В KurrentDB это штатная операция, а не следствие устройства PK.
//
// expectedVersion — сентинел -1 значит «стрима ещё нет» (StreamState
// NoStream), любое значение >= 0 — конкретная ожидаемая ревизия
// (StreamRevision). Это ОТКЛОНЕНИЕ от сигнатуры в брифе (там был uint64):
// ревизии в KurrentDB 0-индексные, и первая запись в несуществующий стрим
// обязана идти с NoStream, а не с Revision(0) — иначе сервер решит, что
// стрим уже существует на нулевой ревизии, и различие «стрима нет» от
// «стрим на ревизии 0» потеряется. int64 с сентинелом -1 передаёт это
// различие тем же типом, что возвращает LoadKurrent.
func AppendKurrent(ctx context.Context, c *kurrentdb.Client, streamID string,
	expectedVersion int64, evs []Event) error {

	data := make([]kurrentdb.EventData, 0, len(evs))
	for _, e := range evs {
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		data = append(data, kurrentdb.EventData{
			ContentType: kurrentdb.ContentTypeJson,
			EventType:   e.Type,
			Data:        payload,
		})
	}

	var state kurrentdb.StreamState
	if expectedVersion < 0 {
		state = kurrentdb.NoStream{}
	} else {
		state = kurrentdb.Revision(uint64(expectedVersion))
	}

	_, err := c.AppendToStream(ctx, streamID, kurrentdb.AppendToStreamOptions{StreamState: state}, data...)
	if err != nil {
		// Реальный API установленной версии (kurrent-io/KurrentDB-Client-Go
		// v1.4.1) отличается от примера в брифе: там ожидался тип
		// *kurrentdb.ErrorWrongExpectedVersion (это API старого пакета
		// EventStore-Client-Go/v4). В v1.4.1 конфликт версии приходит как
		// общий *kurrentdb.Error с одним из двух кодов — распознаём оба:
		// ErrorCodeWrongExpectedVersion (устаревший путь ошибки) и
		// ErrorCodeStreamRevisionConflict (актуальный путь через детали
		// gRPC-статуса).
		var esErr *kurrentdb.Error
		if errors.As(err, &esErr) &&
			(esErr.Code() == kurrentdb.ErrorCodeWrongExpectedVersion ||
				esErr.Code() == kurrentdb.ErrorCodeStreamRevisionConflict) {
			return ErrVersionConflict
		}
		return err
	}
	return nil
}

// LoadKurrent читает стрим целиком и возвращает свёртку с текущей ревизией.
// Возвращает -1, если стрима ещё нет — тот же сентинел, что ждёт
// AppendKurrent на первую запись.
func LoadKurrent(ctx context.Context, c *kurrentdb.Client, streamID string) (Tank, int64, error) {
	stream, err := c.ReadStream(ctx, streamID, kurrentdb.ReadStreamOptions{
		Direction: kurrentdb.Forwards,
		From:      kurrentdb.Start{},
	}, 1_000_000)
	if err != nil {
		if isNotFound(err) {
			return Tank{}, -1, nil
		}
		return Tank{}, -1, err
	}
	defer stream.Close()

	var tank Tank
	version := int64(-1)
	for {
		re, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if isNotFound(err) {
				return Tank{}, -1, nil
			}
			return Tank{}, -1, err
		}
		rec := re.OriginalEvent()
		var e Event
		if err := json.Unmarshal(rec.Data, &e); err != nil {
			return Tank{}, -1, err
		}
		tank = tank.Apply(e)
		version = int64(rec.EventNumber)
	}
	return tank, version, nil
}

func isNotFound(err error) bool {
	var esErr *kurrentdb.Error
	return errors.As(err, &esErr) && esErr.Code() == kurrentdb.ErrorCodeResourceNotFound
}
