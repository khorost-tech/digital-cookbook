// Command serialization — стенд #7 ("экосистема"), часть 3: относительный
// размер ОДНИХ И ТЕХ ЖЕ данных в JSON / Avro / Protobuf.
//
// Схема данных зеркалит ../../ecosystem/schemas/user-v1.avsc (id/name/email) —
// ту же схему, что зарегистрирована и проэволюционирована в Schema Registry
// (см. ops/ecosystem-demo.sh, часть 1). Реализовано БЕЗ Schema-Registry
// сериализаторов (Avro/Protobuf кодируются напрямую в процессе) — постановка
// стенда прямо допускает "вручную" способ сравнения размеров;
// вызывать здесь ещё и SR-клиент ради ТОЛЬКО размера сообщения показалось
// неоправданным усложнением при том же самом честном результате.
//
//   - JSON     — encoding/json (стандартная библиотека), самоописываемый формат
//     (имена полей едут вместе с каждым сообщением).
//   - Avro     — github.com/hamba/avro/v2, бинарная кодировка СТРОГО по схеме
//     (schema передаётся сторонам ОТДЕЛЬНО — Schema Registry). Числа
//     zigzag-varint, строки — varint-длина + байты, БЕЗ имён полей в потоке.
//   - Protobuf — google.golang.org/protobuf/encoding/protowire, низкоуровневое
//     tag-based wire-кодирование БЕЗ протокол-компилятора (protoc) и БЕЗ
//     генерации кода из .proto — см. content-note в README: это те же самые
//     байты, что выдал бы protoc-сгенерированный Marshal() для эквивалентного
//     proto3-сообщения (varint-тэг поля + varint/length-delimited значение),
//     но получены прямым вызовом низкоуровневого API, а не полным
//     protoc-тулчейном (осознанно не тащим protoc в стенд ради одного замера).
//
// Запуск:
//
//	docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 go run ./ecosystem/serialization
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/hamba/avro/v2"
	"google.golang.org/protobuf/encoding/protowire"
)

// User — зеркало ecosystem/schemas/user-v1.avsc (id: long, name: string, email: string).
type User struct {
	ID    int64  `avro:"id" json:"id"`
	Name  string `avro:"name" json:"name"`
	Email string `avro:"email" json:"email"`
}

// avroSchemaJSON — идентична (по существу; порядок ключей JSON не важен для
// Avro) ../../ecosystem/schemas/user-v1.avsc, буквально той же схеме,
// которая регистрируется в Schema Registry в части 1 стенда.
const avroSchemaJSON = `{
  "type": "record",
  "name": "User",
  "namespace": "tech.khorost.kafka.ecosystem",
  "fields": [
    { "name": "id", "type": "long" },
    { "name": "name", "type": "string" },
    { "name": "email", "type": "string" }
  ]
}`

// encodeProtobufUser — ручное tag-based wire-кодирование, эквивалентное
// proto3-сообщению `message User { int64 id=1; string name=2; string email=3; }`.
func encodeProtobufUser(u User) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(u.ID))
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, u.Name)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, u.Email)
	return b
}

func main() {
	schema, err := avro.Parse(avroSchemaJSON)
	if err != nil {
		log.Fatalf("avro.Parse: %v", err)
	}

	// Реалистичный, но детерминированный набор данных — тот же стиль
	// значений (user-N / имя / email), что и в других стендах серии.
	users := []User{
		{ID: 1, Name: "Alice Ivanova", Email: "alice.ivanova@example.com"},
		{ID: 2, Name: "Bob Petrov", Email: "bob.petrov@example.com"},
		{ID: 3, Name: "Carol Sidorova", Email: "carol.sidorova@example.com"},
		{ID: 4, Name: "Dmitry Kuznetsov", Email: "dmitry.kuznetsov@example.com"},
		{ID: 5, Name: "Elena Volkova", Email: "elena.volkova@example.com"},
	}

	fmt.Println("=== Размер ОДНОГО сообщения (байт), по каждой записи ===")
	fmt.Printf("%-20s %8s %8s %8s\n", "record", "JSON", "Avro", "Protobuf")
	var totalJSON, totalAvro, totalProto int
	for _, u := range users {
		j, err := json.Marshal(u)
		if err != nil {
			log.Fatalf("json.Marshal: %v", err)
		}
		a, err := avro.Marshal(schema, u)
		if err != nil {
			log.Fatalf("avro.Marshal: %v", err)
		}
		p := encodeProtobufUser(u)

		fmt.Printf("%-20s %8d %8d %8d\n", fmt.Sprintf("user-%d", u.ID), len(j), len(a), len(p))
		totalJSON += len(j)
		totalAvro += len(a)
		totalProto += len(p)
	}

	fmt.Println()
	fmt.Println("=== Суммарно по 5 записям (байт) и относительно JSON ===")
	fmt.Printf("JSON:     %5d байт (1.00x, baseline)\n", totalJSON)
	fmt.Printf("Avro:     %5d байт (%.2fx размера JSON)\n", totalAvro, float64(totalAvro)/float64(totalJSON))
	fmt.Printf("Protobuf: %5d байт (%.2fx размера JSON)\n", totalProto, float64(totalProto)/float64(totalJSON))

	fmt.Println()
	fmt.Println("[info] размер детерминирован СХЕМОЙ+ДАННЫМИ (не зависит от хоста/прогона) —")
	fmt.Println("       в отличие от throughput/latency-замеров в других стендах серии.")
	fmt.Println("[info] Avro/Protobuf выигрывают засчёт отсутствия имён полей в потоке")
	fmt.Println("       (схема передаётся сторонам ОТДЕЛЬНО — то, что делает Schema Registry")
	fmt.Println("       из части 1 этого же стенда) и компактного бинарного кодирования чисел.")
}
