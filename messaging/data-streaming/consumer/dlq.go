package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// dlqTopicSuffix — DLQ конкретного топика живёт по адресу <топик>+суффикс, а
// не в отдельно захардкоженной константе: правило одно для любого источника.
const dlqTopicSuffix = ".dlq"

// ensureDLQTopic создаёт DLQ-топик заранее и явно, вместо того чтобы
// полагаться на auto.create.topics.enable брокера.
//
// Причина, по которой без этого шага toDLQ детерминированно проваливается
// с UNKNOWN_TOPIC_OR_PARTITION на первом же битом сообщении, — КЛИЕНТСКАЯ,
// не брокерная. Брокер этого стенда автосоздание НЕ отключает: у него
// auto.create.topics.enable=true (проверено `kafka-configs.sh
// --entity-type brokers --describe --all`; compose/compose.yml его не
// переопределяет) — это подтверждает и kafka-console-producer.sh, который
// на этом брокере СОЗДАЁТ топик при Produce (kafka-topics.sh --list до и
// после подтверждает разницу). Но franz-go по умолчанию НЕ просит брокер
// создать топик при Produce на несуществующий: поле allowAutoTopicCreation
// в конфиге клиента (pkg/kgo/config.go:94) имеет нулевое значение false
// и включается только явным опционом AllowAutoTopicCreation()
// (config.go:761) — без него в запрос продюса уходит
// req.AllowAutoTopicCreation=false (client.go:867), и брокер, даже
// разрешая автосоздание у себя, его не выполняет: клиент не попросил.
// Это не только обход конкретного поведения клиентской библиотеки: в
// проде DLQ-топику обычно нужны СВОИ параметры (retention длиннее, чем
// у исходного топика — разбирать причину сбоя можно не сразу), которые
// дефолты auto-create не дадут — провижининг должен быть явным шагом
// пайплайна, а не побочным эффектом первого падения.
//
// Partitions=1/ReplicationFactor=1 — как и у остальных топиков этого
// single-node стенда (см. KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 и
// соседние настройки в compose/compose.yml); в проде — по факту кластера.
//
// Идемпотентно: TOPIC_ALREADY_EXISTS (обычный случай при повторных запусках
// консьюмера) — не ошибка, топик уже готов.
func ensureDLQTopic(ctx context.Context, adm *kadm.Client, topic string) error {
	dlqTopic := topic + dlqTopicSuffix
	// adm.CreateTopic возвращает per-топиковую ошибку (TOPIC_ALREADY_EXISTS и
	// т.п.) КАК err этой функции, а не только внутри резалта (см. её реализацию
	// в kadm/topics.go: `return response, response.Err`) — поэтому единственная
	// проверка именно на err, а не отдельно на resp.Err.
	_, err := adm.CreateTopic(ctx, 1, 1, nil, dlqTopic)
	if err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
		return fmt.Errorf("create topic %s: %w", dlqTopic, err)
	}
	return nil
}

// toDLQ отправляет непарсимое сообщение в DLQ вместе с причиной и СИНХРОННО
// ждёт подтверждения брокера (ProduceSync), возвращая ошибку, а не глотая её.
//
// Смысл: пайплайн не должен вставать из-за одного битого события, но и
// терять его молча нельзя. DLQ — это «отложить и жить дальше», а не
// «выбросить»: причина едет в заголовке, тело сохраняется как есть.
//
// Почему возвращает error, а не печатает и продолжает: вызывающий код
// (run() в main.go) коммитит оффсет битого сообщения ОДНИМ вызовом
// CommitRecords вместе со всей остальной пачкой. Если бы toDLQ сам проглотил
// неудачную отправку, run() закоммитил бы оффсет сообщения, которое на самом
// деле НЕ доехало ни до витрины (оно и не должно было), ни до DLQ — событие
// потерялось бы бесследно, а стенд как раз демонстрирует at-least-once, где
// потеря молча запрещена. Поэтому ошибка должна дойти до run() и там
// остановить коммит этой пачки — см. комментарий у вызова toDLQ в main.go.
func toDLQ(ctx context.Context, cl *kgo.Client, rec *kgo.Record, cause error) error {
	dlq := &kgo.Record{
		Topic: rec.Topic + dlqTopicSuffix,
		Key:   rec.Key,
		Value: rec.Value,
		Headers: []kgo.RecordHeader{
			{Key: "dlq-reason", Value: []byte(cause.Error())},
			{Key: "dlq-origin-topic", Value: []byte(rec.Topic)},
			{Key: "dlq-origin-offset", Value: []byte(fmt.Sprint(rec.Offset))},
		},
	}
	if err := cl.ProduceSync(ctx, dlq).FirstErr(); err != nil {
		return fmt.Errorf("produce to %s: %w", dlq.Topic, err)
	}
	return nil
}
