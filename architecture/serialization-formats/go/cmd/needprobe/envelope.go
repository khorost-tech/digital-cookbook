package main

import (
	"encoding/binary"
	"fmt"
)

// Конверт реестра (Задача 7, требование 3 брифа): такой же провод, что
// использует официальный serdes Confluent/Apicurio для Avro поверх
// реестра схем — 1 магический байт + 4 байта big-endian глобального
// идентификатора схемы, затем сами avro-байты БЕЗ схемы писателя внутри
// потока (сама схема живёт в реестре, а не в данных). Стенд не тянет
// клиентскую библиотеку serdes целиком — только этот же формат провода,
// напрямую, чтобы наивный декодер (envelope.go, требование 3) мог
// столкнуться именно с ним.
const (
	envelopeMagicByte = 0x00
	envelopePrefixLen = 5 // 1 байт магии + 4 байта id
)

// wrapEnvelope собирает конверт: то, что реальный производитель написал
// бы в топик, обратившись к реестру за идентификатором схемы.
func wrapEnvelope(globalID int64, avroBytes []byte) []byte {
	out := make([]byte, 0, envelopePrefixLen+len(avroBytes))
	out = append(out, envelopeMagicByte)
	var idBytes [4]byte
	binary.BigEndian.PutUint32(idBytes[:], uint32(globalID))
	out = append(out, idBytes[:]...)
	out = append(out, avroBytes...)
	return out
}

// unwrapEnvelope — ЧЕСТНЫЙ разбор конверта: то, что делает клиент,
// знающий про формат провода реестра. Возвращает идентификатор схемы и
// сами avro-байты без префикса.
func unwrapEnvelope(b []byte) (globalID int64, payload []byte, err error) {
	if len(b) < envelopePrefixLen {
		return 0, nil, fmt.Errorf("конверт короче %d байт префикса: длина %d", envelopePrefixLen, len(b))
	}
	if b[0] != envelopeMagicByte {
		return 0, nil, fmt.Errorf("неизвестный магический байт конверта: 0x%02x (ожидался 0x%02x)", b[0], envelopeMagicByte)
	}
	id := binary.BigEndian.Uint32(b[1:envelopePrefixLen])
	return int64(id), b[envelopePrefixLen:], nil
}
