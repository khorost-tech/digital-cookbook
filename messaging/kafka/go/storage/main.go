// Command storage — стенд #4 серии "Kafka: глубокое погружение": хранение —
// retention (время/размер), log compaction, компрессия.
//
// Как и replication (стенд #3), часть демонстраций требует, чтобы ХОСТ
// инспектировал реальные файлы сегментов на брокере между вызовами клиента
// (docker exec kafka-log-dirs.sh/ls, изменение динамических broker-конфигов
// log.cleaner.backoff.ms) — у клиента внутри контейнера нет доступа к docker
// socket и нет способа увидеть файлы сегментов напрямую. Оркестрирует
// ../../ops/inspect-segments.sh, вызывая нужные "фазы" этой программы ДО и
// ПОСЛЕ соответствующих docker exec-команд.
//
// Запуск отдельной фазы (пример):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./storage -scenario=retention-setup
//
// См. ../../ops/inspect-segments.sh для полного сценария и ../../README.md
// за реальный прогон.
package main

import (
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092", "comma-separated bootstrap servers")
	scenario := flag.String("scenario", "", "retention-setup|retention-produce|offsets|compact-setup|compact-produce|compact-produce-business|compact-produce-filler|compact-consume|compress-setup|compress-produce")
	topic := flag.String("topic", "", "имя топика (по умолчанию — стандартное для сценария)")
	n := flag.Int("n", 150, "число записей (retention-produce/compress-produce)")
	padBytes := flag.Int("pad-bytes", 120, "целевой размер значения записи в байтах")
	retentionMs := flag.Int("retention-ms", 6000, "retention.ms (retention-setup)")
	// 1048576 (1MiB) — минимум, разрешённый брокером (LogConfig.validate:
	// "Value must be at least 1048576"), проверено живьём попыткой создать
	// топик с segment.bytes=2048 (INVALID_CONFIG). Меньше выставить нельзя.
	segmentBytes := flag.Int("segment-bytes", 1048576, "segment.bytes (retention-setup/compact-setup, минимум брокера — 1048576)")
	keysCSV := flag.String("keys", "biz-key-1,biz-key-2,biz-key-3,biz-key-4,biz-key-5,biz-key-6,biz-key-7,biz-key-8", "ключи для compact-produce/compact-consume (через запятую)")
	rounds := flag.Int("rounds", 4, "число раундов обновлений на ключ (compact-produce)")
	tombstoneKeysCSV := flag.String("tombstone-keys", "biz-key-7,biz-key-8", "ключи, получающие tombstone в конце (compact-produce/compact-consume)")
	fillerN := flag.Int("filler-n", 24, "число filler-записей после tombstone (compact-produce), форсируют roll последнего сегмента")
	fillerStart := flag.Int("filler-start", 0, "с какого номера нумеровать filler-ключи (compact-produce-filler); второй раунд должен продолжать нумерацию, а не начинать с 0 — иначе перезапишет те же ключи первого раунда")
	idle := flag.Duration("idle", 5*time.Second, "idle-таймаут consume (compact-consume)")
	label := flag.String("label", "", "метка для вывода (например 'до'/'после')")
	assert := flag.Bool("assert", false, "compact-consume: проверить финальное состояние (1 запись/живой ключ, tombstone-ключи отсутствуют)")
	minEarliest := flag.Int64("min-earliest-gt", -1, "offsets: если >=0, падать, если earliest НЕ строго больше этого значения")
	codec := flag.String("codec", "none", "compress-setup/compress-produce: none|gzip|lz4|zstd")
	partitions := flag.Int("partitions", 1, "число партиций (setup)")
	rf := flag.Int("rf", 3, "replication factor (setup)")
	flag.Parse()

	seeds := strings.Split(*brokers, ",")
	keys := strings.Split(*keysCSV, ",")
	tombstoneKeys := strings.Split(*tombstoneKeysCSV, ",")

	switch *scenario {
	case "retention-setup":
		t := defTopic(*topic, "demo-storage-retention")
		configs := map[string]*string{
			"cleanup.policy": strPtr("delete"),
			"segment.bytes":  strPtr(itoa(*segmentBytes)),
			"segment.ms":     strPtr("3600000"), // намеренно большой — roll управляется ТОЛЬКО размером, не временем (детерминизм демо)
			"retention.ms":   strPtr(itoa(*retentionMs)),
			"retention.bytes": strPtr("-1"), // чистый time-based retention, без второго управляющего параметра
		}
		recreateTopic(seeds, t, int32(*partitions), int16(*rf), configs)
		waitForLeader(seeds, t, 30*time.Second)

	case "retention-produce":
		t := defTopic(*topic, "demo-storage-retention")
		cl := newSyncProducer(seeds)
		defer cl.Close()
		produceUnkeyedSequential(cl, t, *n, *padBytes)

	case "offsets":
		t := defTopic(*topic, "demo-storage-retention")
		o := reportOffsets(seeds, t, *label)
		if *minEarliest >= 0 {
			if o.earliest <= *minEarliest {
				log.Fatalf("[assert] FAIL: earliest=%d НЕ больше %d — retention не сдвинул earliest offset", o.earliest, *minEarliest)
			}
			fmt.Printf("[assert] OK: earliest=%d > %d — retention сдвинул earliest offset вперёд\n", o.earliest, *minEarliest)
		}

	case "compact-setup":
		t := defTopic(*topic, "demo-storage-compact")
		configs := map[string]*string{
			"cleanup.policy":            strPtr("compact"),
			"segment.bytes":             strPtr(itoa(*segmentBytes)),
			"segment.ms":                strPtr("3600000"), // тот же трюк: roll только по размеру
			"min.cleanable.dirty.ratio": strPtr("0.01"),    // почти любой "грязный" объём запускает компакцию
			"delete.retention.ms":       strPtr("100"),     // tombstone живёт совсем недолго после компакции — быстрая демонстрация
			"min.compaction.lag.ms":     strPtr("0"),
			"max.compaction.lag.ms":     strPtr("8000"), // страховка: компакция гарантированно случится в пределах 8с даже без dirty-ratio триггера
		}
		recreateTopic(seeds, t, int32(*partitions), int16(*rf), configs)
		waitForLeader(seeds, t, 30*time.Second)

	case "compact-produce":
		t := defTopic(*topic, "demo-storage-compact")
		cl := newSyncProducer(seeds)
		defer cl.Close()
		produceKeyedUpdates(cl, t, keys, *rounds, *padBytes)
		produceTombstones(cl, t, tombstoneKeys)
		produceFiller(cl, t, *fillerN, *padBytes, *fillerStart)

	// compact-produce-business / compact-produce-filler — то же самое, что
	// compact-produce, но раздельными фазами. Нужно затем, чтобы захватить
	// ЧЕСТНОЕ "до"-состояние: пока бизнес-записи (обновления+tombstone) лежат
	// в АКТИВНОМ (не rolled) сегменте, log cleaner их вообще не видит —
	// компакция физически невозможна для активного сегмента. compact-produce
	// (одним вызовом) не даёт окна для consume МЕЖДУ бизнес-записями и
	// filler — при быстром log.cleaner.backoff.ms (демо-настройка, см.
	// ops/inspect-segments.sh) компакция может успеть отработать РАНЬШЕ, чем
	// внешний consume-вызов вообще начнётся, и "до" покажет уже сжатое
	// состояние (проверено живьём: именно так и произошло на первой попытке).
	case "compact-produce-business":
		t := defTopic(*topic, "demo-storage-compact")
		cl := newSyncProducer(seeds)
		defer cl.Close()
		produceKeyedUpdates(cl, t, keys, *rounds, *padBytes)
		produceTombstones(cl, t, tombstoneKeys)

	// -filler-start=0 для ПЕРВОГО вызова (форсирует roll, закрывающий сегмент
	// с бизнес-записями/tombstone — делает его видимым log cleaner'у).
	// -filler-start=<fillerN> для ВТОРОГО вызова (см. ops/inspect-segments.sh) —
	// живьём проверено: физическое удаление tombstone-маркера требует ВТОРОГО
	// прохода компакции над тем же сегментом (проход 1 схлопывает версии
	// живых ключей, но ОСТАВЛЯЕТ tombstone — предохранитель Kafka от гонки с
	// отстающими консьюмерами, см. README/content-notes, KAFKA-3137). Проход
	// 2 сам не запускается без новых "грязных" данных — нужен ЕЩЁ один roll.
	case "compact-produce-filler":
		t := defTopic(*topic, "demo-storage-compact")
		cl := newSyncProducer(seeds)
		defer cl.Close()
		produceFiller(cl, t, *fillerN, *padBytes, *fillerStart)

	case "compact-consume":
		t := defTopic(*topic, "demo-storage-compact")
		recv := consumeAllFromStart(seeds, t, *idle)
		printCompactState(*label, recv)
		if *assert {
			assertCompacted(recv, keys, tombstoneKeys, *rounds-1)
		}

	case "compress-setup":
		t := defTopic(*topic, "demo-storage-compress-"+*codec)
		configs := map[string]*string{
			"cleanup.policy": strPtr("delete"),
		}
		recreateTopic(seeds, t, int32(*partitions), int16(*rf), configs)
		waitForLeader(seeds, t, 30*time.Second)

	case "compress-produce":
		t := defTopic(*topic, "demo-storage-compress-"+*codec)
		cl := newSyncProducer(seeds, codecFromString(*codec))
		defer cl.Close()
		produceBatchedAsync(cl, t, *n, *padBytes)

	default:
		log.Fatalf("неизвестный -scenario=%q", *scenario)
	}
}

func defTopic(flagVal, def string) string {
	if flagVal != "" {
		return flagVal
	}
	return def
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func codecFromString(s string) kgo.CompressionCodec {
	switch s {
	case "none":
		return kgo.NoCompression()
	case "gzip":
		return kgo.GzipCompression()
	case "lz4":
		return kgo.Lz4Compression()
	case "zstd":
		return kgo.ZstdCompression()
	default:
		log.Fatalf("неизвестный -codec=%q (none|gzip|lz4|zstd)", s)
		return kgo.NoCompression()
	}
}
