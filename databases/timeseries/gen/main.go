// gen — генератор временных рядов для стенда timeseries.
//
// Один и тот же детерминированный набор точек уходит в три системы:
//
//	gen ts    — в гипертаблицу TimescaleDB через COPY (pgx CopyFrom);
//	gen om    — в файл OpenMetrics для `promtool tsdb create-blocks-from openmetrics`;
//	gen vm    — в VictoriaMetrics через /api/v1/import/prometheus;
//	gen rw    — живая запись по протоколу remote_write (Prometheus и VictoriaMetrics);
//	gen card  — remote_write N новых рядов с одним лейблом высокой кардинальности;
//	gen check — прочитать точки обратно через /api/v1/query и сравнить с исходными.
//
// Форма ряда задаётся -shape: counter, gauge2 (gauge, округлённый до 0,01),
// gauge (тот же gauge с полной точностью float64), const.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/golang/snappy"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protowire"
)

type opts struct {
	shape  string
	series int
	span   time.Duration
	step   time.Duration
	end    time.Time
	name   string
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: gen ts|om|vm|rw|card [flags]")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o opts
	var endStr, dsn, table, url, out string
	var offset, batch int
	fs.StringVar(&o.shape, "shape", "gauge2", "counter | gauge2 | gauge | const")
	fs.IntVar(&o.series, "series", 200, "число рядов")
	fs.DurationVar(&o.span, "span", 24*time.Hour, "глубина истории")
	fs.DurationVar(&o.step, "step", 15*time.Second, "шаг между точками")
	fs.StringVar(&endStr, "end", "", "конец истории, RFC 3339 (по умолчанию — начало текущего часа)")
	fs.StringVar(&o.name, "name", "", "имя метрики (по умолчанию demo_<shape>)")
	fs.StringVar(&dsn, "dsn", "postgres://postgres:ts@ts:5432/ts", "TimescaleDB")
	fs.StringVar(&table, "table", "metrics", "таблица для gen ts")
	fs.StringVar(&url, "url", "", "адрес приёмника")
	fs.StringVar(&out, "out", "", "файл для gen om")
	fs.IntVar(&offset, "offset", 0, "gen card: номер первого нового ряда")
	fs.IntVar(&batch, "batch", 5000, "gen card: рядов в одном запросе")
	fs.Parse(args)

	if endStr == "" {
		o.end = time.Now().UTC().Truncate(time.Hour)
	} else {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			log.Fatal(err)
		}
		o.end = t.UTC()
	}
	if o.name == "" {
		o.name = "demo_" + o.shape
	}

	var err error
	switch cmd {
	case "ts":
		err = loadTS(dsn, table, o)
	case "om":
		err = writeOM(out, o)
	case "vm":
		err = importVM(url, o)
	case "rw":
		err = remoteWriteHistory(url, o)
	case "check":
		err = check(url, o)
	case "card":
		err = cardinality(url, o.series, offset, batch)
	default:
		err = fmt.Errorf("неизвестная команда %q", cmd)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// points вызывает f для каждой точки каждого ряда — по рядам, внутри ряда
// по возрастанию времени. Значения зависят только от seed, номера ряда и шага.
// Точки покрывают полуинтервал [end-span, end): сама точка end не пишется.
func points(o opts, f func(s int, t time.Time, v float64) error) error {
	n := int(o.span / o.step)
	start := o.end.Add(-time.Duration(n) * o.step)
	for s := 0; s < o.series; s++ {
		r := rand.New(rand.NewPCG(42, uint64(s)))
		phase := r.Float64() * 2 * math.Pi
		base := 40 + r.Float64()*20
		acc := 0.0
		for i := 0; i < n; i++ {
			t := start.Add(time.Duration(i) * o.step)
			var v float64
			switch o.shape {
			case "counter":
				acc += float64(r.IntN(20))
				v = acc
			case "gauge", "gauge2":
				v = base + 15*math.Sin(float64(i)/240+phase) + r.NormFloat64()*2
				if o.shape == "gauge2" {
					v = math.Round(v*100) / 100
				}
			case "const":
				v = 1
			default:
				return fmt.Errorf("неизвестная форма %q", o.shape)
			}
			if err := f(s, t, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func host(s int) string { return fmt.Sprintf("h-%03d", s) }
func dc(s int) string   { return fmt.Sprintf("dc%d", s%4+1) }

func loadTS(dsn, table string, o opts) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	rows := make(chan []any, 4096)
	errc := make(chan error, 1)
	go func() {
		errc <- points(o, func(s int, t time.Time, v float64) error {
			rows <- []any{t, int32(s), v}
			return nil
		})
		close(rows)
	}()
	began := time.Now()
	n, err := conn.CopyFrom(ctx, pgx.Identifier{table}, []string{"time", "series_id", "value"},
		pgx.CopyFromFunc(func() ([]any, error) {
			r, ok := <-rows
			if !ok {
				return nil, nil
			}
			return r, nil
		}))
	if err != nil {
		return err
	}
	if err := <-errc; err != nil {
		return err
	}
	fmt.Printf("ts: %s: %d строк за %s\n", table, n, time.Since(began).Round(time.Millisecond))
	return nil
}

func writeOM(path string, o opts) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	typ := "gauge"
	if o.shape == "counter" {
		typ = "counter"
	}
	fmt.Fprintf(w, "# TYPE %s %s\n", o.name, typ)
	suffix := ""
	if typ == "counter" {
		suffix = "_total"
	}
	var n int
	err = points(o, func(s int, t time.Time, v float64) error {
		n++
		_, err := fmt.Fprintf(w, "%s%s{host=%q,dc=%q} %s %d\n", o.name, suffix, host(s), dc(s),
			fmtFloat(v), t.Unix())
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "# EOF")
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("om: %s: %d точек\n", path, n)
	return nil
}

func fmtFloat(v float64) string { return fmt.Sprintf("%v", v) }

// importVM отправляет точки в текстовом формате Prometheus с метками времени
// в миллисекундах — потоком, без промежуточного файла.
func importVM(url string, o opts) error {
	pr, pw := io.Pipe()
	var n int
	go func() {
		w := bufio.NewWriterSize(pw, 1<<20)
		err := points(o, func(s int, t time.Time, v float64) error {
			n++
			_, err := fmt.Fprintf(w, "%s{host=%q,dc=%q} %s %d\n", vmName(o), host(s), dc(s),
				fmtFloat(v), t.UnixMilli())
			return err
		})
		if err == nil {
			err = w.Flush()
		}
		pw.CloseWithError(err)
	}()
	resp, err := http.Post(url+"/api/v1/import/prometheus", "text/plain", pr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("vm: %s: %s", resp.Status, body)
	}
	fmt.Printf("vm: %d точек\n", n)
	return nil
}

func vmName(o opts) string {
	if o.shape == "counter" {
		return o.name + "_total"
	}
	return o.name
}

// --- remote_write: WriteRequest кодируется вручную через protowire ---
//
//	WriteRequest { repeated TimeSeries timeseries = 1; }
//	TimeSeries   { repeated Label labels = 1; repeated Sample samples = 2; }
//	Label        { string name = 1; string value = 2; }
//	Sample       { double value = 1; int64 timestamp = 2; }

type sample struct {
	v  float64
	ts int64
}

type series struct {
	labels  [][2]string // __name__ первым, остальные по алфавиту
	samples []sample
}

func encode(ss []series) []byte {
	var req []byte
	for _, s := range ss {
		var ts []byte
		for _, l := range s.labels {
			var lb []byte
			lb = protowire.AppendTag(lb, 1, protowire.BytesType)
			lb = protowire.AppendString(lb, l[0])
			lb = protowire.AppendTag(lb, 2, protowire.BytesType)
			lb = protowire.AppendString(lb, l[1])
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, lb)
		}
		for _, p := range s.samples {
			var sb []byte
			sb = protowire.AppendTag(sb, 1, protowire.Fixed64Type)
			sb = protowire.AppendFixed64(sb, math.Float64bits(p.v))
			sb = protowire.AppendTag(sb, 2, protowire.VarintType)
			sb = protowire.AppendVarint(sb, uint64(p.ts))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, sb)
		}
		req = protowire.AppendTag(req, 1, protowire.BytesType)
		req = protowire.AppendBytes(req, ts)
	}
	return snappy.Encode(nil, req)
}

func push(url string, ss []series) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encode(ss)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s: %s", url, resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// remoteWriteHistory — история по remote_write: все точки ряда в одном
// TimeSeries, по возрастанию времени.
func remoteWriteHistory(url string, o opts) error {
	byS := make([]series, o.series)
	for s := range byS {
		byS[s].labels = [][2]string{{"__name__", vmName(o)}, {"dc", dc(s)}, {"host", host(s)}}
	}
	var n int
	if err := points(o, func(s int, t time.Time, v float64) error {
		byS[s].samples = append(byS[s].samples, sample{v, t.UnixMilli()})
		n++
		return nil
	}); err != nil {
		return err
	}
	if err := push(url, byS); err != nil {
		return err
	}
	fmt.Printf("rw: %d рядов, %d точек\n", o.series, n)
	return nil
}

// cardinality — count новых рядов demo_requests{user_id="u-…"} по одной
// текущей точке: так растёт число рядов, когда в лейбл попадает идентификатор.
func cardinality(url string, count, offset, batch int) error {
	now := time.Now().UnixMilli()
	for from := offset; from < offset+count; from += batch {
		to := min(from+batch, offset+count)
		ss := make([]series, 0, to-from)
		for i := from; i < to; i++ {
			ss = append(ss, series{
				labels:  [][2]string{{"__name__", "demo_requests"}, {"user_id", fmt.Sprintf("u-%07d", i)}},
				samples: []sample{{1, now}},
			})
		}
		if err := push(url, ss); err != nil {
			return err
		}
	}
	fmt.Printf("card: ряды %d..%d записаны\n", offset, offset+count-1)
	return nil
}

// check читает сырые точки метрики запросом name[span] в момент end — так
// отвечают и Prometheus, и VictoriaMetrics — и сравнивает каждую точку с тем,
// что генератор записал для того же ряда и момента. Считаются пропуски
// (записано, но не вернулось), лишние точки и дубликаты пары «ряд — время»;
// совпадение — побитное, через math.Float64bits.
func check(base string, o opts) error {
	want := map[string]float64{}
	if err := points(o, func(s int, t time.Time, v float64) error {
		want[fmt.Sprintf("%s/%d", host(s), t.UnixMilli())] = v
		return nil
	}); err != nil {
		return err
	}
	// Окно (end-span-1s, end-1s]: в него попадают все записанные точки.
	q := url.Values{}
	q.Set("query", fmt.Sprintf("%s[%ds]", vmName(o), int(o.span.Seconds())))
	q.Set("time", strconv.FormatInt(o.end.Unix()-1, 10))
	resp, err := http.Get(base + "/api/v1/query?" + q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s: %s", base, resp.Status, bytes.TrimSpace(body))
	}
	var r struct {
		Status string
		Error  string
		Data   struct {
			ResultType string
			Result     []struct {
				Metric map[string]string
				Values [][2]any
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if r.Status != "success" || r.Data.ResultType != "matrix" {
		return fmt.Errorf("%s: status=%q resultType=%q error=%q", base, r.Status, r.Data.ResultType, r.Error)
	}
	seen := map[string]bool{}
	var got, exact, extra, dup int
	var maxAbs, maxRel float64
	for _, res := range r.Data.Result {
		for _, p := range res.Values {
			got++
			ts := int64(math.Round(p[0].(float64) * 1000))
			v, err := strconv.ParseFloat(p[1].(string), 64)
			if err != nil {
				return err
			}
			key := fmt.Sprintf("%s/%d", res.Metric["host"], ts)
			w, ok := want[key]
			if !ok {
				extra++
				continue
			}
			if seen[key] {
				dup++
				continue
			}
			seen[key] = true
			if math.Float64bits(v) == math.Float64bits(w) {
				exact++
				continue
			}
			d := math.Abs(v - w)
			maxAbs = max(maxAbs, d)
			if w != 0 {
				maxRel = max(maxRel, d/math.Abs(w))
			}
		}
	}
	missing := len(want) - len(seen)
	fmt.Printf("check: записано %d, получено %d, сопоставлено %d, пропусков %d, лишних %d, дубликатов %d\n",
		len(want), got, len(seen), missing, extra, dup)
	fmt.Printf("check: совпали побитно %d из %d, max |ошибка| %.3g, max относительная %.3g\n",
		exact, len(seen), maxAbs, maxRel)
	if missing > 0 || extra > 0 || dup > 0 {
		return fmt.Errorf("ответ неполон или с лишними точками")
	}
	return nil
}
