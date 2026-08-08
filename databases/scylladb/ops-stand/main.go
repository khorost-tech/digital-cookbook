// Command ops-stand — стенд #7 серии "ScyllaDB: глубокое погружение":
// эксплуатация. Единственный live-сценарий этого бинаря — `-scenario
// alternator`: тот же ScyllaDB отвечает по DynamoDB-совместимому протоколу
// (Alternator) на ОТДЕЛЬНОМ, ТРАНЗИТНОМ однонодовом контейнере (см. README
// «Стенд #7» и ops/ops-demo.sh) — основной 3-узловой кластер
// (scylla1/2/3, keyspace telemetry) НЕ запускался с `--alternator-port` и
// трогать его конфиг means recreate → потеря 672000 строк readings, поэтому
// Alternator демонстрируется на отдельном узле scylla-alt на ТОЙ ЖЕ сети
// scylla-cookbook-net, а не на основном кластере.
//
// AWS SDK for Go v2 (dynamodb) с кастомным base endpoint (BaseEndpoint =
// http://scylla-alt:8000 внутри compose-сети) и статичными
// dummy-credentials — Alternator не проверяет AWS-подпись содержательно
// (ScyllaDB реализует только протокол, не биллинг/IAM), но SDK v2 требует
// непустые access key/secret/region, иначе клиент не соберётся.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func main() {
	scenario := flag.String("scenario", "alternator", "alternator — единственный сценарий стенда #7")
	endpoint := flag.String("endpoint", envOr("ALTERNATOR_ENDPOINT", "http://localhost:8000"), "DynamoDB-совместимый endpoint Alternator")
	table := flag.String("table", "ops_alternator_demo", "имя DynamoDB-таблицы для демо")
	waitReady := flag.Duration("wait-ready", 60*time.Second, "сколько ждать, пока Alternator-порт откликнется, перед стартом сценария")
	flag.Parse()

	switch *scenario {
	case "alternator":
		if err := runAlternator(*endpoint, *table, *waitReady); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown -scenario %q (expected alternator)\n", *scenario)
		os.Exit(2)
	}
}

func runAlternator(endpoint, table string, waitReady time.Duration) error {
	fmt.Printf("=== Стенд #7: ops-stand -scenario alternator ===\n")
	fmt.Printf("endpoint: %s\n", endpoint)
	fmt.Printf("table:    %s\n\n", table)

	cfg := aws.Config{
		Region:      "us-east-1", // Alternator игнорирует регион, SDK v2 требует непустое значение
		Credentials: credentials.NewStaticCredentialsProvider("dummy", "dummy", ""),
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitReady)
	defer cancel()

	fmt.Printf("ожидаем готовности Alternator (до %s)...\n", waitReady)
	pollT0 := time.Now()
	var lastErr error
	ready := false
	for time.Since(pollT0) < waitReady {
		_, err := client.ListTables(ctx, &dynamodb.ListTablesInput{})
		if err == nil {
			ready = true
			break
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if !ready {
		return fmt.Errorf("alternator не ответил за %s: %w", waitReady, lastErr)
	}
	fmt.Printf("Alternator готов (%s)\n\n", time.Since(pollT0).Round(time.Millisecond))

	// -- CreateTable --------------------------------------------------------
	fmt.Println("-- CreateTable --")
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("device_id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("event_time"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("device_id"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("event_time"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		var inUse *ddbtypes.ResourceInUseException
		if errors.As(err, &inUse) {
			fmt.Println("таблица уже существует (идемпотентный повтор) — продолжаем")
		} else {
			return fmt.Errorf("CreateTable: %w", err)
		}
	} else {
		fmt.Println("CreateTable: OK, ждём ACTIVE...")
	}
	if err := waitTableActive(ctx, client, table, 30*time.Second); err != nil {
		return fmt.Errorf("wait ACTIVE: %w", err)
	}
	fmt.Println("таблица ACTIVE")
	fmt.Println()

	// -- PutItem --------------------------------------------------------------
	deviceID := "dev-alternator-0001"
	eventTime := time.Now().UTC().Format(time.RFC3339Nano)
	putValue := 42.5

	fmt.Println("-- PutItem --")
	item := map[string]ddbtypes.AttributeValue{
		"device_id":  &ddbtypes.AttributeValueMemberS{Value: deviceID},
		"event_time": &ddbtypes.AttributeValueMemberS{Value: eventTime},
		"metric":     &ddbtypes.AttributeValueMemberS{Value: "cpu"},
		"value":      &ddbtypes.AttributeValueMemberN{Value: fmt.Sprintf("%.2f", putValue)},
		"region":     &ddbtypes.AttributeValueMemberS{Value: "eu-west"},
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
		return fmt.Errorf("PutItem: %w", err)
	}
	fmt.Printf("PutItem: device_id=%s event_time=%s value=%.2f — OK\n\n", deviceID, eventTime, putValue)

	// -- GetItem --------------------------------------------------------------
	fmt.Println("-- GetItem --")
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]ddbtypes.AttributeValue{
			"device_id":  &ddbtypes.AttributeValueMemberS{Value: deviceID},
			"event_time": &ddbtypes.AttributeValueMemberS{Value: eventTime},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("GetItem: %w", err)
	}
	if out.Item == nil {
		return fmt.Errorf("GetItem: item не найден (device_id=%s event_time=%s)", deviceID, eventTime)
	}

	gotValueAttr, ok := out.Item["value"].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return fmt.Errorf("GetItem: поле value отсутствует или неверного типа: %#v", out.Item["value"])
	}
	gotDeviceAttr, _ := out.Item["device_id"].(*ddbtypes.AttributeValueMemberS)
	gotEventAttr, _ := out.Item["event_time"].(*ddbtypes.AttributeValueMemberS)

	fmt.Printf("GetItem: device_id=%s event_time=%s value=%s\n\n", gotDeviceAttr.Value, gotEventAttr.Value, gotValueAttr.Value)

	// Сравниваем ЧИСЛОВОЕ значение, не литеральную строку: живая находка —
	// Alternator (как и настоящий DynamoDB) канонизирует тип N при записи,
	// PutItem с "42.50" читается обратно как "42.5" (убирает незначащий
	// нуль) — это НЕ баг Alternator/этого демо, а задокументированное
	// поведение типа Number в самом протоколе DynamoDB (значение — decimal,
	// не строка), см. README «Стенд #7». Побайтовое сравнение строк было бы
	// нечестным ассертом для протокола, который сам не гарантирует
	// побайтовую сохранность литерала числа.
	gotValueNum, numErr := strconv.ParseFloat(gotValueAttr.Value, 64)
	valueMatch := numErr == nil && gotValueNum == putValue
	match := gotDeviceAttr.Value == deviceID && gotEventAttr.Value == eventTime && valueMatch

	fmt.Printf("RESULT scenario=alternator endpoint=%s table=%s put_ok=true get_ok=true match=%t\n", endpoint, table, match)

	if !match {
		return fmt.Errorf("ассерт провален: get != put (device_id=%q/%q event_time=%q/%q value=%q/%.2f)",
			gotDeviceAttr.Value, deviceID, gotEventAttr.Value, eventTime, gotValueAttr.Value, putValue)
	}
	fmt.Println("\nОК: get item == put item — тот же ScyllaDB отвечает по DynamoDB-совместимому протоколу (Alternator).")
	return nil
}

func waitTableActive(ctx context.Context, client *dynamodb.Client, table string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		desc, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
		if err != nil {
			return err
		}
		if desc.Table != nil && desc.Table.TableStatus == ddbtypes.TableStatusActive {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("таблица %s не стала ACTIVE за %s", table, timeout)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
