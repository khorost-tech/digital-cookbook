package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// pad возвращает строку длиной примерно targetBytes: базовая часть + заполнитель
// из filler-символа. Используется, чтобы контролируемо занимать место на диске
// (число/размер сегментов в стенде — управляемая, а не случайная величина).
func pad(base string, targetBytes int, filler byte) string {
	if len(base) >= targetBytes {
		return base
	}
	return base + strings.Repeat(string(filler), targetBytes-len(base))
}

// newSyncProducer — клиент-продюсер для синхронной (ProduceSync) отправки,
// acks=all по умолчанию (durability нам тут не демонстрируем — важна
// последовательность записи на диск, а не гонки acks).
//
// ⚠️ Если compression не передан явно, ЯВНО ставим NoCompression() — franz-go
// по умолчанию использует Snappy (проверено живьём: без этой строки
// retention-produce с pad('x', 5000) на диске давал ~341 байт/запись вместо
// ~5000 — однобайтовый filler чудовищно сжимается снаппи, и segment.bytes
// roll срабатывал в разы позже, чем рассчитано по логическому размеру
// данных). Retention/compaction-сценарии считают физический размер на диске
// (roll по segment.bytes) — там компрессия должна быть предсказуемо
// выключена. compress-сценарии передают codec явно.
func newSyncProducer(seeds []string, compression ...kgo.CompressionCodec) *kgo.Client {
	if len(compression) == 0 {
		compression = []kgo.CompressionCodec{kgo.NoCompression()}
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(seeds...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(compression...),
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatalf("kgo.NewClient (producer): %v", err)
	}
	return cl
}

// produceUnkeyedSequential — n записей без ключа (retention-демо: сам факт
// ключа тут не важен, важен только объём и время записи), синхронно одна за
// другой, значение фиксированной длины ~padBytes.
func produceUnkeyedSequential(cl *kgo.Client, topic string, n int, padBytes int) {
	ctx := context.Background()
	for i := 0; i < n; i++ {
		value := pad(fmt.Sprintf("retention-%06d-", i), padBytes, 'x')
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := cl.ProduceSync(pctx, rec).First()
		cancel()
		if err != nil {
			log.Fatalf("produce i=%d: %v", i, err)
		}
	}
	fmt.Printf("[produce] topic=%s: отправлено %d записей без ключа, ~%d байт значения каждая\n", topic, n, padBytes)
}

// produceKeyedUpdates — round-robin по ключам, rounds обновлений на каждый
// ключ (K: v0->v1->...->v(rounds-1)) — тот же паттерн чередования, что в
// log-basics (не пачками подряд на ключ, а раундами), чтобы порядок записи в
// логе воспроизводимо перемежал ключи.
func produceKeyedUpdates(cl *kgo.Client, topic string, keys []string, rounds int, padBytes int) int {
	ctx := context.Background()
	sent := 0
	for round := 0; round < rounds; round++ {
		for _, k := range keys {
			value := pad(fmt.Sprintf("%s-round%d-", k, round), padBytes, 'y')
			rec := &kgo.Record{Topic: topic, Key: []byte(k), Value: []byte(value)}
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := cl.ProduceSync(pctx, rec).First()
			cancel()
			if err != nil {
				log.Fatalf("produce key=%s round=%d: %v", k, round, err)
			}
			sent++
		}
	}
	fmt.Printf("[produce] topic=%s: %d ключей x %d раундов = %d update-записей отправлено\n", topic, len(keys), rounds, sent)
	return sent
}

// produceTombstones — по одной tombstone-записи (Value=nil, ЯВНО nil, а не
// пустой []byte{} — это и есть маркер удаления ключа в compacted-топике) на
// каждый ключ из keys.
func produceTombstones(cl *kgo.Client, topic string, keys []string) {
	ctx := context.Background()
	for _, k := range keys {
		rec := &kgo.Record{Topic: topic, Key: []byte(k), Value: nil}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := cl.ProduceSync(pctx, rec).First()
		cancel()
		if err != nil {
			log.Fatalf("produce tombstone key=%s: %v", k, err)
		}
	}
	fmt.Printf("[produce] topic=%s: %d tombstone-записей отправлено (ключи: %v)\n", topic, len(keys), keys)
}

// produceFiller — n записей с уникальными одноразовыми ключами (нумерация
// начинается с startAt — см. ниже, почему это важно) и заданным размером
// значения. Первая цель — физически довести текущий (активный) сегмент до
// roll (segment.bytes), чтобы сегмент с "боевыми" последними
// апдейтами/tombstone-ами перестал быть активным и стал доступен log
// cleaner'у для компакции.
//
// ⚠️ Вторая цель, обнаруженная живьём: log cleaner физически удаляет
// tombstone-маркер ТОЛЬКО на ВТОРОМ проходе компакции над сегментом, который
// его содержит (проверено живьём: первый проход схлопывает версии по живым
// ключам, но tombstone остаётся ещё МИНУТЫ, пока сегмент не "дёрнут" вторым
// проходом; это реальный, задокументированный механизм Kafka — предохранитель
// от гонки с отстающими консьюмерами, см. KAFKA-3137 и content-notes README).
// Второй проход не запускается сам по себе, если у лога нет новых "грязных"
// данных — нужен ЕЩЁ один roll сегмента. Отсюда — сценарий стенда вызывает
// produceFiller ДВАЖДЫ (см. ops/inspect-segments.sh), и startAt даёт второму
// вызову ПРОДОЛЖИТЬ нумерацию ключей (250..499), а не начать заново с 0 —
// иначе второй раунд filler'а перезаписал бы ТЕ ЖЕ ключи первого раунда,
// и cleaner молча дедуплицировал бы filler-записи как обычные обновления
// (тоже реальное поведение, но не то, что мы хотим здесь демонстрировать —
// filler задуман как "балласт", а не как второй бизнес-сценарий).
func produceFiller(cl *kgo.Client, topic string, n int, padBytes int, startAt int) {
	ctx := context.Background()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("roll-filler-%04d", startAt+i)
		value := pad(key+"-", padBytes, 'z')
		rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: []byte(value)}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := cl.ProduceSync(pctx, rec).First()
		cancel()
		if err != nil {
			log.Fatalf("produce filler i=%d: %v", i, err)
		}
	}
	fmt.Printf("[produce] topic=%s: %d filler-записей отправлено (форсируют roll сегмента, не участвуют в бизнес-ассертах)\n", topic, n)
}

// produceBatchedAsync — n записей АСИНХРОННО (Produce без ожидания ответа на
// каждую, затем один Flush в конце) — в отличие от produceUnkeyedSequential,
// это даёт клиенту накопить настоящие батчи (linger.ms/batch.size), на
// которых компрессия реально работает: последовательные ProduceSync с
// ожиданием ответа перед каждой следующей записью почти всегда создают батчи
// размера 1, где gzip/lz4/zstd почти ничего не сжимают (и даже могут
// "раздуть" запись из-за заголовков кодека) — это НЕ то, как компрессия
// работает в реальном проде (там батчинг — норма).
func produceBatchedAsync(cl *kgo.Client, topic string, n int, padBytes int) (acked, failed int, elapsed time.Duration) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		value := pad(fmt.Sprintf("compress-%08d-order_id=%d-user=user-%d-status=OK-region=eu-west-1-", i, i, i%50), padBytes, 'c')
		rec := &kgo.Record{Topic: topic, Value: []byte(value)}
		wg.Add(1)
		cl.Produce(context.Background(), rec, func(r *kgo.Record, err error) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				acked++
			} else {
				failed++
			}
		})
	}
	wg.Wait()
	elapsed = time.Since(start)
	fmt.Printf("[produce] topic=%s: батч-отправка %d записей (acked=%d failed=%d) за %s\n", topic, n, acked, failed, elapsed)
	return
}
