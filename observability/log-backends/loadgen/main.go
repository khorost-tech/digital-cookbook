// Генератор потока логов для стенда log-backends.
//
// Пишет в файл JSON-записи, которые Vector разливает в три бэкенда. Рядом с
// файлом кладёт манифест: сколько записей каждого вида отправлено. Проверки
// сверяются с манифестом, а не с тем, что нашлось в бэкендах, — иначе
// потерянные записи было бы нечем обнаружить.
//
//	loadgen -count 100000 -seed 42 -run r1
//	loadgen -count 100000 -seed 42 -run r2 -high-cardinality
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// record — одна запись лога. Поля намеренно простые и одинаковые для всех
// бэкендов: цель стенда — сравнить модели индексации, а не разбор форматов.
type record struct {
	Timestamp string `json:"timestamp"`
	Service   string `json:"service"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	// RequestID заполняется только в режиме -high-cardinality: уникальное
	// значение на запись. В обычном режиме поле отсутствует вовсе, а не пустое —
	// пустая строка тоже создала бы поле в индексе и исказила замер объёма.
	RequestID string `json:"request_id,omitempty"`
}

// manifest — что именно отправлено. Считается ДО записи в файл, поэтому не
// зависит от того, что случилось дальше по конвейеру.
type manifest struct {
	Run             string         `json:"run"`
	Seed            int64          `json:"seed"`
	Total           int            `json:"total"`
	ByLevel         map[string]int `json:"by_level"`
	ByService       map[string]int `json:"by_service"`
	HighCardinality bool           `json:"high_cardinality"`
	// ErrorsCheckout — ответ на вопрос, который задаётся всем трём бэкендам в
	// проверке сопоставимости. Держится в манифесте, чтобы проверка сравнивала
	// с фактом, а не сама с собой.
	ErrorsCheckout int `json:"errors_checkout"`
}

var (
	services = []string{"checkout", "catalog", "payments"}
	levels   = []string{"info", "info", "info", "warn", "error"}
	messages = []string{
		"order accepted",
		"item reserved",
		"payment authorized",
		"cache miss",
		"upstream timeout",
	}
)

func main() {
	count := flag.Int("count", 100000, "сколько записей записать")
	seed := flag.Int64("seed", 42, "seed: одинаковый seed даёт одинаковый состав записей")
	run := flag.String("run", "r1", "метка прогона: попадает в манифест")
	out := flag.String("out", "/logs", "каталог для файла логов и манифеста")
	highCard := flag.Bool("high-cardinality", false, "добавлять request_id, уникальный на каждую запись")
	// База времени. По умолчанию поток заканчивается «сейчас»: записи с
	// временем в будущем бэкенды принимают, но запросы за последний час их не
	// находят — VictoriaLogs молча вернул ноль при 5000 принятых строк.
	baseFlag := flag.String("base", "", "начало потока в RFC3339; пусто — поток заканчивается текущим моментом")
	flag.Parse()

	base := time.Now().UTC().Add(-time.Duration(*count) * time.Millisecond)
	if *baseFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *baseFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "не разобрать -base:", err)
			os.Exit(1)
		}
		base = parsed
	}

	m, err := generate(*out, *count, *seed, *run, *highCard, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("записано", m.Total, "строк в", filepath.Join(*out, "app.log"))
	fmt.Println("  по уровням:", m.ByLevel)
	fmt.Println("  error у checkout:", m.ErrorsCheckout)
	fmt.Println("  высокая кардинальность:", m.HighCardinality)
}

// generate пишет поток и манифест. Вынесена из main отдельной функцией, чтобы
// тесты проверяли ровно тот код, который работает в контейнере, а не его
// копию: расхождение между проверенным и работающим — худший вид зелёных
// тестов.
func generate(out string, count int, seed int64, run string, highCard bool, base time.Time) (manifest, error) {
	rnd := rand.New(rand.NewSource(seed))

	logPath := filepath.Join(out, "app.log")
	f, err := os.Create(logPath)
	if err != nil {
		return manifest{}, fmt.Errorf("не удалось создать %s: %w", logPath, err)
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)

	m := manifest{
		Run:             run,
		Seed:            seed,
		Total:           count,
		ByLevel:         map[string]int{},
		ByService:       map[string]int{},
		HighCardinality: highCard,
	}

	// Время идёт от переданной базы с шагом в миллисекунду. База — параметр, а не
	// константа внутри: с фиксированной датой в будущем бэкенды записи ПРИНИМАЮТ,
	// а запросы за последний час их не находят. VictoriaLogs в таком прогоне
	// показал vl_rows_ingested_total 5000 и ноль в ответе на запрос.
	//
	// При одинаковых seed И base файл совпадает побайтово — это условие замера
	// объёма: сравнивать надо одни и те же данные.

	for i := 0; i < count; i++ {
		svc := services[rnd.Intn(len(services))]
		lvl := levels[rnd.Intn(len(levels))]
		rec := record{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Service:   svc,
			Level:     lvl,
			Message:   messages[rnd.Intn(len(messages))],
		}
		if highCard {
			rec.RequestID = fmt.Sprintf("req-%s-%08d", run, i)
		}
		if err := enc.Encode(rec); err != nil {
			return manifest{}, fmt.Errorf("запись %d: %w", i, err)
		}
		m.ByLevel[lvl]++
		m.ByService[svc]++
		if svc == "checkout" && lvl == "error" {
			m.ErrorsCheckout++
		}
	}

	if err := w.Flush(); err != nil {
		return manifest{}, fmt.Errorf("сброс буфера: %w", err)
	}

	manifestPath := filepath.Join(out, "manifest.json")
	mf, err := os.Create(manifestPath)
	if err != nil {
		return manifest{}, fmt.Errorf("не удалось создать %s: %w", manifestPath, err)
	}
	defer mf.Close()

	menc := json.NewEncoder(mf)
	menc.SetIndent("", "  ")
	if err := menc.Encode(m); err != nil {
		return manifest{}, fmt.Errorf("манифест: %w", err)
	}

	return m, nil
}
