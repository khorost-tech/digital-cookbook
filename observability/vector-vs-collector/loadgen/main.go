package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type manifest struct {
	Nginx         int64 `json:"nginx"`
	App           int64 `json:"app"`
	Total         int64 `json:"total"`
	NginxExpected int64 `json:"nginx_expected"` // строки без /health — столько должно доехать
	AppExpected   int64 `json:"app_expected"`   // различимые комбинации полей dedupe
}

// emitTick пишет perTick строк за один тик, чередуя файлы по ОБЩЕМУ числу уже
// записанных строк. Чередование по позиции внутри тика было бы дефектом: при
// perTick == 1 (а это любой rate меньше 200) в каждый тик попадала бы только
// строка nginx, и app.log оставался бы пустым весь прогон.
// Вынесено из main ради регрессионного теста на perTick == 1.
//
// seen копит уже встреченные ключи дедупа app-веток по ВСЕМУ прогону (не только
// текущему тику) — так AppExpected считает различимые комбинации так же, как
// это делает dedupe-кэш в агенте.
func emitTick(nginxW, appW io.Writer, r *rand.Rand, ts time.Time, perTick int, m manifest, seen map[string]struct{}) manifest {
	for i := 0; i < perTick; i++ {
		if (m.Nginx+m.App)%2 == 0 {
			line := NginxLine(r, ts)
			fmt.Fprintln(nginxW, line)
			m.Nginx++
			if !strings.Contains(line, "/health") {
				m.NginxExpected++
			}
		} else {
			line := AppJSONLine(r, ts)
			fmt.Fprintln(appW, line)
			m.App++
			key := appDedupeKey(line)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				m.AppExpected++
			}
		}
	}
	return m
}

func main() {
	rate := flag.Int("rate", 5000, "событий в секунду суммарно по обоим файлам")
	duration := flag.Duration("duration", 60*time.Second, "длительность прогона")
	out := flag.String("out", "/logs", "каталог для файлов логов и манифеста")
	seed := flag.Int64("seed", 42, "seed генератора: фиксирует последовательность содержимого, не побайтовое совпадение файла (таймстемпы берутся из реального времени)")
	otlp := flag.Bool("otlp", false, "режим OTLP-клиента: слать трейсы и логи по OTLP/HTTP вместо записи файлов")
	endpoint := flag.String("endpoint", "http://otelcol:4318", "адрес приёмника OTLP/HTTP")
	spans := flag.Int("spans", 100, "сколько спанов отправить в режиме -otlp (0 — не слать трейсы)")
	otlpLogs := flag.Int("otlp-logs", 100, "сколько записей логов отправить в режиме -otlp (0 — не слать логи)")
	flag.Parse()

	if *otlp {
		runOTLP(*endpoint, *spans, *otlpLogs, *seed)
		return
	}

	// нижняя граница rate — не придирка: при rate < 100 в тик приходится меньше одной
	// строки, генератор округлил бы вверх и молча выдавал больше запрошенного,
	// а замер, построенный на таком прогоне, врал бы беззвучно
	if *rate < 100 {
		log.Fatalf("rate должен быть не меньше 100 событий/с, получено %d", *rate)
	}
	if *duration <= 0 {
		log.Fatalf("duration должен быть больше нуля, получено %s", *duration)
	}

	// файлы пересоздаются на каждом прогоне: иначе повторный прогон читается
	// из страничного кэша ОС и замеры файлового чтения врут
	nginxF, err := os.Create(filepath.Join(*out, "nginx-access.log"))
	if err != nil {
		log.Fatalf("не создать nginx-access.log: %v", err)
	}
	defer nginxF.Close()
	appF, err := os.Create(filepath.Join(*out, "app.log"))
	if err != nil {
		log.Fatalf("не создать app.log: %v", err)
	}
	defer appF.Close()

	nginxW := bufio.NewWriterSize(nginxF, 1<<20)
	appW := bufio.NewWriterSize(appF, 1<<20)
	r := rand.New(rand.NewSource(*seed))

	// пишем пачками по 10 мс, чтобы держать rate без busy-loop
	const tickMs = 10
	perTick := *rate * tickMs / 1000
	if perTick < 1 {
		perTick = 1
	}
	ticker := time.NewTicker(tickMs * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(*duration)

	var m manifest
	seen := map[string]struct{}{}
	for now := range ticker.C {
		if now.After(deadline) {
			break
		}
		m = emitTick(nginxW, appW, r, now, perTick, m, seen)
	}

	if err := nginxW.Flush(); err != nil {
		log.Fatalf("flush nginx: %v", err)
	}
	if err := appW.Flush(); err != nil {
		log.Fatalf("flush app: %v", err)
	}
	m.Total = m.Nginx + m.App

	mf, err := os.Create(filepath.Join(*out, "manifest.json"))
	if err != nil {
		log.Fatalf("не создать manifest.json: %v", err)
	}
	defer mf.Close()
	if err := json.NewEncoder(mf).Encode(m); err != nil {
		log.Fatalf("не записать манифест: %v", err)
	}

	log.Printf("сгенерировано: nginx=%d app=%d total=%d", m.Nginx, m.App, m.Total)
}
