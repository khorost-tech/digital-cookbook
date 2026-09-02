package main

import "testing"

// Конверт реестра (требование 3 брифа Задачи 7) — тот же провод, что
// использует Confluent/Apicurio serdes для Avro: один магический байт
// (0x00) плюс 4-байтовый big-endian глобальный идентификатор схемы,
// затем сами avro-байты без схемы писателя внутри потока. Это НЕ
// придумка стенда — это то, что фактически распаковывает клиентская
// библиотека, обращаясь к реестру; стенд лишь воспроизводит этот же
// провод напрямую, не подключая клиентскую библиотеку целиком.
func TestWrapUnwrapEnvelope_RoundTrips(t *testing.T) {
	avroBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	env := wrapEnvelope(42, avroBytes)

	if len(env) != len(avroBytes)+envelopePrefixLen {
		t.Fatalf("длина конверта = %d, хотим %d (5 байт префикса + %d байт полезной нагрузки)",
			len(env), len(avroBytes)+envelopePrefixLen, len(avroBytes))
	}

	gotID, gotPayload, err := unwrapEnvelope(env)
	if err != nil {
		t.Fatalf("unwrapEnvelope: %v", err)
	}
	if gotID != 42 {
		t.Fatalf("globalID = %d, хотим 42", gotID)
	}
	if string(gotPayload) != string(avroBytes) {
		t.Fatalf("payload = %v, хотим %v", gotPayload, avroBytes)
	}
}

func TestUnwrapEnvelope_TooShort(t *testing.T) {
	_, _, err := unwrapEnvelope([]byte{0x00, 0x01})
	if err == nil {
		t.Fatalf("ожидали ошибку на укороченном конверте (меньше 5 байт префикса)")
	}
}

func TestUnwrapEnvelope_WrongMagicByte(t *testing.T) {
	env := wrapEnvelope(1, []byte{0xAA})
	env[0] = 0x7F // испорченный магический байт
	_, _, err := unwrapEnvelope(env)
	if err == nil {
		t.Fatalf("ожидали ошибку на неизвестном магическом байте")
	}
}

// TestNaiveDecodeIgnoresPrefix — требование 3: наивный декодер читает
// байты конверта КАК ЕСТЬ, включая 5-байтовый префикс, будто это чистый
// поток Avro. prefixLen ИЗМЕРЯЕТСЯ (разница длин), а не берётся из
// константы напрямую, — так расхождение между константой протокола и
// фактически собранными байтами тоже стало бы видимым.
func TestMeasuredPrefixLenMatchesConstant(t *testing.T) {
	avroBytes := []byte{0x0A, 0x0B, 0x0C}
	env := wrapEnvelope(7, avroBytes)
	measured := len(env) - len(avroBytes)
	if measured != envelopePrefixLen {
		t.Fatalf("измеренная длина префикса = %d, конструктивная константа = %d — разошлись",
			measured, envelopePrefixLen)
	}
}
