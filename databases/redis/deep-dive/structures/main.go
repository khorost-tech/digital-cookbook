// Стенд #1: модель данных и внутренние кодировки.
//
// Три сценария (-scenario):
//   - listpack-threshold:   hash/list/zset — точка перехода listpack → hashtable/quicklist/skiplist
//     по числу элементов и по размеру значения.
//   - intset-threshold:     set из целых чисел — переход intset → listpack/hashtable.
//   - memory-per-structure: MEMORY USAGE на характерных размерах (10/100/1000) для
//     string/hash/list/set/zset/stream.
//
// Адрес Redis/Valkey читается из REDIS_ADDR, по умолчанию 127.0.0.1:6379.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"
)

func addrFromEnv() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	return addr
}

func configGetInt(ctx context.Context, rdb *redis.Client, name string) int {
	m, err := rdb.ConfigGet(ctx, name).Result()
	if err != nil {
		fmt.Printf("CONFIG GET %s: error: %v\n", name, err)
		return -1
	}
	val, ok := m[name]
	if !ok {
		fmt.Printf("%s: <нет такого параметра>\n", name)
		return -1
	}
	fmt.Printf("%s: %s\n", name, val)
	n, err := strconv.Atoi(val)
	if err != nil {
		// отрицательные/непарсимые значения (напр. list-max-listpack-size
		// может быть отрицательным — лимит по байтам на узел quicklist,
		// а не по числу элементов) — возвращаем как есть, сценарий сам решает.
		n, _ = strconv.Atoi(val)
	}
	return n
}

func objectEncoding(ctx context.Context, rdb *redis.Client, key string) string {
	enc, err := rdb.Do(ctx, "OBJECT", "ENCODING", key).Text()
	if err != nil {
		return "error:" + err.Error()
	}
	return enc
}

// must проверяет ошибку записи и завершает процесс, если она есть. Каждое
// число, которое печатает этот стенд, должно быть основано на записи, которая
// реально прошла — молча проглоченная ошибка записи (например, оборванное
// соединение посреди sweep на 1000+ элементов) иначе привела бы к тому, что
// OBJECT ENCODING/MEMORY USAGE отчитались бы по структуре меньшего размера,
// чем предполагалось, без единого предупреждения.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи: %v\n", label, err)
		os.Exit(1)
	}
}

// sweepEntries добавляет элементы по одному через addFn(i) (i начиная с 1) и
// печатает точку перехода кодировки — момент, когда OBJECT ENCODING меняется
// относительно предыдущего шага. Печатает первые несколько шагов до перехода,
// сам переход и несколько шагов после — остальное молча пропускает, чтобы лог
// оставался читаемым.
func sweepEntries(ctx context.Context, rdb *redis.Client, label, key string, maxN int, addFn func(i int) redis.Cmder) {
	must(label+" DEL(before)", rdb.Del(ctx, key))
	prevEnc := ""
	transitioned := false
	afterCount := 0
	for i := 1; i <= maxN; i++ {
		must(fmt.Sprintf("%s write(count=%d)", label, i), addFn(i))
		enc := objectEncoding(ctx, rdb, key)
		if enc != prevEnc {
			fmt.Printf("%s: count=%d encoding=%s (было %q)\n", label, i, enc, prevEnc)
			if prevEnc != "" {
				transitioned = true
			}
			prevEnc = enc
		} else if transitioned {
			afterCount++
			if afterCount <= 3 {
				fmt.Printf("%s: count=%d encoding=%s (после перехода)\n", label, i, enc)
			}
			if afterCount >= 3 {
				break
			}
		}
	}
	if !transitioned {
		fmt.Printf("%s: переход не обнаружен до count=%d (последняя кодировка %s)\n", label, maxN, prevEnc)
	}
	must(label+" DEL(after)", rdb.Del(ctx, key))
}

func listpackThreshold(ctx context.Context, rdb *redis.Client) {
	fmt.Println("=== listpack-threshold ===")

	hashEntries := configGetInt(ctx, rdb, "hash-max-listpack-entries")
	hashValue := configGetInt(ctx, rdb, "hash-max-listpack-value")
	listSize := configGetInt(ctx, rdb, "list-max-listpack-size")
	zsetEntries := configGetInt(ctx, rdb, "zset-max-listpack-entries")
	zsetValue := configGetInt(ctx, rdb, "zset-max-listpack-value")

	// hash: по числу полей
	hashMax := hashEntries + 20
	if hashMax < 20 {
		hashMax = 200
	}
	sweepEntries(ctx, rdb, "hash/entries", "enc:hash:entries", hashMax, func(i int) redis.Cmder {
		return rdb.HSet(ctx, "enc:hash:entries", fmt.Sprintf("f%d", i), "v")
	})

	// hash: по размеру значения одного поля (растим значение единственного поля)
	hashValMax := hashValue + 20
	if hashValMax < 20 {
		hashValMax = 200
	}
	must("hash/value DEL(before)", rdb.Del(ctx, "enc:hash:value"))
	prevEnc := ""
	transitioned := false
	after := 0
	for i := 1; i <= hashValMax; i++ {
		val := make([]byte, i)
		for j := range val {
			val[j] = 'x'
		}
		must(fmt.Sprintf("hash/value write(len=%d)", i), rdb.HSet(ctx, "enc:hash:value", "f", string(val)))
		enc := objectEncoding(ctx, rdb, "enc:hash:value")
		if enc != prevEnc {
			fmt.Printf("hash/value: len=%d encoding=%s (было %q)\n", i, enc, prevEnc)
			if prevEnc != "" {
				transitioned = true
			}
			prevEnc = enc
		} else if transitioned {
			after++
			if after <= 3 {
				fmt.Printf("hash/value: len=%d encoding=%s (после перехода)\n", i, enc)
			}
			if after >= 3 {
				break
			}
		}
	}
	if !transitioned {
		fmt.Printf("hash/value: переход не обнаружен до len=%d\n", hashValMax)
	}
	must("hash/value DEL(after)", rdb.Del(ctx, "enc:hash:value"))

	// list: по числу элементов. list-max-listpack-size может быть отрицательным
	// (лимит по байтам на узел quicklist, напр. -2 = 8 КиБ) — в этом случае
	// переход зависит от суммарного размера, а не от количества элементов
	// напрямую; эмпирически (проверено живьём на коротких элементах "eN")
	// переход на -2 происходит около 1300+ элементов, поэтому берём широкий
	// диапазон, чтобы гарантированно его поймать.
	listMax := listSize + 20
	if listSize <= 0 || listMax < 20 {
		listMax = 1500
	}
	sweepEntries(ctx, rdb, "list/entries", "enc:list:entries", listMax, func(i int) redis.Cmder {
		return rdb.RPush(ctx, "enc:list:entries", fmt.Sprintf("e%d", i))
	})

	// zset: по числу элементов
	zsetMax := zsetEntries + 20
	if zsetMax < 20 {
		zsetMax = 200
	}
	sweepEntries(ctx, rdb, "zset/entries", "enc:zset:entries", zsetMax, func(i int) redis.Cmder {
		return rdb.ZAdd(ctx, "enc:zset:entries", redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)})
	})

	// zset: по размеру значения (member) единственного элемента
	zsetValMax := zsetValue + 20
	if zsetValMax < 20 {
		zsetValMax = 200
	}
	must("zset/value DEL(before)", rdb.Del(ctx, "enc:zset:value"))
	prevEnc = ""
	transitioned = false
	after = 0
	for i := 1; i <= zsetValMax; i++ {
		member := make([]byte, i)
		for j := range member {
			member[j] = 'm'
		}
		must(fmt.Sprintf("zset/value ZAdd(len=%d)", i), rdb.ZAdd(ctx, "enc:zset:value", redis.Z{Score: 1, Member: string(member)}))
		if i > 1 {
			// убрать предыдущий member той же длины-1, чтобы расти только по
			// длине значения, а не по числу элементов одновременно.
			must(fmt.Sprintf("zset/value ZRem(len=%d)", i-1), rdb.ZRem(ctx, "enc:zset:value", string(member[:i-1])))
		}
		enc := objectEncoding(ctx, rdb, "enc:zset:value")
		if enc != prevEnc {
			fmt.Printf("zset/value: len=%d encoding=%s (было %q)\n", i, enc, prevEnc)
			if prevEnc != "" {
				transitioned = true
			}
			prevEnc = enc
		} else if transitioned {
			after++
			if after <= 3 {
				fmt.Printf("zset/value: len=%d encoding=%s (после перехода)\n", i, enc)
			}
			if after >= 3 {
				break
			}
		}
	}
	if !transitioned {
		fmt.Printf("zset/value: переход не обнаружен до len=%d\n", zsetValMax)
	}
	must("zset/value DEL(after)", rdb.Del(ctx, "enc:zset:value"))
}

func intsetThreshold(ctx context.Context, rdb *redis.Client) {
	fmt.Println("=== intset-threshold ===")

	intsetEntries := configGetInt(ctx, rdb, "set-max-intset-entries")
	listpackEntries := configGetInt(ctx, rdb, "set-max-listpack-entries")
	listpackValue := configGetInt(ctx, rdb, "set-max-listpack-value")
	_ = listpackValue

	// переход по числу целых элементов: intset -> listpack/hashtable
	maxN := intsetEntries + 20
	if maxN < 20 {
		maxN = 600
	}
	sweepEntries(ctx, rdb, "set/int-entries", "enc:set:ints", maxN, func(i int) redis.Cmder {
		return rdb.SAdd(ctx, "enc:set:ints", i)
	})

	// переход при добавлении нечислового элемента в маленький intset (заведомо
	// меньше set-max-listpack-entries) — ожидаем intset -> listpack.
	key := "enc:set:small-nonint"
	must("set/non-int (small) DEL(before)", rdb.Del(ctx, key))
	for i := 1; i <= 5; i++ {
		must(fmt.Sprintf("set/non-int (small) SAdd(i=%d)", i), rdb.SAdd(ctx, key, i))
	}
	encBefore := objectEncoding(ctx, rdb, key)
	fmt.Printf("set/non-int (small): before encoding=%s (5 целых элементов)\n", encBefore)
	must("set/non-int (small) SAdd(non-int)", rdb.SAdd(ctx, key, "not-a-number"))
	encAfter := objectEncoding(ctx, rdb, key)
	fmt.Printf("set/non-int (small): after encoding=%s (добавлен нечисловой элемент)\n", encAfter)
	must("set/non-int (small) DEL(after)", rdb.Del(ctx, key))

	// переход при добавлении нечислового элемента в большой intset (около
	// set-max-listpack-entries по числу элементов) — ожидаем intset -> hashtable,
	// если число элементов превышает set-max-listpack-entries.
	key = "enc:set:large-nonint"
	must("set/non-int (large) DEL(before)", rdb.Del(ctx, key))
	largeN := listpackEntries + 10
	if largeN < 10 {
		largeN = 200
	}
	for i := 1; i <= largeN; i++ {
		must(fmt.Sprintf("set/non-int (large) SAdd(i=%d)", i), rdb.SAdd(ctx, key, i))
	}
	encBefore = objectEncoding(ctx, rdb, key)
	fmt.Printf("set/non-int (large, n=%d): before encoding=%s\n", largeN, encBefore)
	must("set/non-int (large) SAdd(non-int)", rdb.SAdd(ctx, key, "not-a-number"))
	encAfter = objectEncoding(ctx, rdb, key)
	fmt.Printf("set/non-int (large, n=%d): after encoding=%s (добавлен нечисловой элемент)\n", largeN, encAfter)
	must("set/non-int (large) DEL(after)", rdb.Del(ctx, key))
}

func memoryPerStructure(ctx context.Context, rdb *redis.Client) {
	fmt.Println("=== memory-per-structure ===")
	sizes := []int{10, 100, 1000}

	memUsage := func(key string) int64 {
		n, err := rdb.MemoryUsage(ctx, key).Result()
		if err != nil {
			fmt.Printf("MEMORY USAGE %s: error: %v\n", key, err)
			return -1
		}
		return n
	}

	for _, n := range sizes {
		// string: значение длиной n байт
		key := "enc:mem:string"
		must("string DEL(before)", rdb.Del(ctx, key))
		val := make([]byte, n)
		for j := range val {
			val[j] = 'x'
		}
		must(fmt.Sprintf("string Set(n=%d)", n), rdb.Set(ctx, key, string(val), 0))
		fmt.Printf("string n=%d(bytes) memory_usage=%d\n", n, memUsage(key))
		must("string DEL(after)", rdb.Del(ctx, key))

		// hash: n полей
		key = "enc:mem:hash"
		must("hash DEL(before)", rdb.Del(ctx, key))
		for i := 0; i < n; i++ {
			must(fmt.Sprintf("hash HSet(n=%d,i=%d)", n, i), rdb.HSet(ctx, key, fmt.Sprintf("f%d", i), fmt.Sprintf("v%d", i)))
		}
		fmt.Printf("hash n=%d memory_usage=%d\n", n, memUsage(key))
		must("hash DEL(after)", rdb.Del(ctx, key))

		// list: n элементов
		key = "enc:mem:list"
		must("list DEL(before)", rdb.Del(ctx, key))
		for i := 0; i < n; i++ {
			must(fmt.Sprintf("list RPush(n=%d,i=%d)", n, i), rdb.RPush(ctx, key, fmt.Sprintf("e%d", i)))
		}
		fmt.Printf("list n=%d memory_usage=%d\n", n, memUsage(key))
		must("list DEL(after)", rdb.Del(ctx, key))

		// set: n целых элементов
		key = "enc:mem:set"
		must("set DEL(before)", rdb.Del(ctx, key))
		for i := 0; i < n; i++ {
			must(fmt.Sprintf("set SAdd(n=%d,i=%d)", n, i), rdb.SAdd(ctx, key, i))
		}
		fmt.Printf("set n=%d memory_usage=%d\n", n, memUsage(key))
		must("set DEL(after)", rdb.Del(ctx, key))

		// zset: n элементов
		key = "enc:mem:zset"
		must("zset DEL(before)", rdb.Del(ctx, key))
		for i := 0; i < n; i++ {
			must(fmt.Sprintf("zset ZAdd(n=%d,i=%d)", n, i), rdb.ZAdd(ctx, key, redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)}))
		}
		fmt.Printf("zset n=%d memory_usage=%d\n", n, memUsage(key))
		must("zset DEL(after)", rdb.Del(ctx, key))

		// stream: n записей
		key = "enc:mem:stream"
		must("stream DEL(before)", rdb.Del(ctx, key))
		for i := 0; i < n; i++ {
			must(fmt.Sprintf("stream XAdd(n=%d,i=%d)", n, i), rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]interface{}{"i": i}}))
		}
		fmt.Printf("stream n=%d memory_usage=%d\n", n, memUsage(key))
		must("stream DEL(after)", rdb.Del(ctx, key))
	}
}

func main() {
	scenario := flag.String("scenario", "", "listpack-threshold | intset-threshold | memory-per-structure")
	flag.Parse()

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addrFromEnv()})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	switch *scenario {
	case "listpack-threshold":
		listpackThreshold(ctx, rdb)
	case "intset-threshold":
		intsetThreshold(ctx, rdb)
	case "memory-per-structure":
		memoryPerStructure(ctx, rdb)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: listpack-threshold | intset-threshold | memory-per-structure")
		os.Exit(1)
	}
}
