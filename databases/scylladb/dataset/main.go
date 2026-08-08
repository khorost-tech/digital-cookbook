// Command dataset — детерминированный генератор общего датасета-телеметрии
// к серии статей "ScyllaDB: глубокое погружение". Одинаковый вход (seed,
// devices, days, per-day) -> байт-в-байт одинаковый вывод, не зависит от
// time.Now() (окно суток отсчитывается от зашитой в код опорной точки
// 2026-07-01, а не от даты запуска).
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// Reading — одна запись телеметрии устройства.
type Reading struct {
	DeviceID  string
	Day       time.Time
	EventTime time.Time
	Metric    string
	Value     float64
	Region    string
	Status    string
}

var (
	metrics  = []string{"cpu", "mem", "temp", "netio", "disk"}
	regions  = []string{"eu-west", "eu-east", "us-east", "ap-south"}
	statuses = []string{"ok", "warn", "crit"}
	refDate  = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
)

// Generate детерминирован: одинаковый вход -> одинаковый выход (seed фиксирован).
func Generate(seed int64, devices, days, perDay int) []Reading {
	r := rand.New(rand.NewSource(seed))
	out := make([]Reading, 0, devices*days*perDay)
	for d := 0; d < devices; d++ {
		id := fmt.Sprintf("dev-%05d", d)
		region := regions[d%len(regions)] // регион детерминирован по устройству
		for day := 0; day < days; day++ {
			dayTime := refDate.AddDate(0, 0, day)
			for p := 0; p < perDay; p++ {
				et := dayTime.Add(time.Duration(p) * 15 * time.Minute) // 96/сутки = каждые 15 мин
				val := 20 + 60*r.Float64()
				st := statuses[0]
				if val > 70 {
					st = statuses[2]
				} else if val > 55 {
					st = statuses[1]
				}
				out = append(out, Reading{
					DeviceID: id, Day: dayTime, EventTime: et,
					Metric: metrics[p%len(metrics)], Value: math.Round(val*100) / 100,
					Region: region, Status: st,
				})
			}
		}
	}
	return out
}

func main() {
	var seed int64
	devices := flag.Int("devices", 500, "число устройств")
	days := flag.Int("days", 14, "число суток")
	perDay := flag.Int("per-day", 96, "замеров в сутки")
	flag.Int64Var(&seed, "seed", 42, "seed")
	out := flag.String("out", "csv", "csv|count")
	load := flag.Bool("load", false, "грузить в telemetry.<table>")
	table := flag.String("table", "readings", "readings|readings_bad|readings_hot — целевая таблица для -load")
	hosts := flag.String("hosts", envOr("SCYLLA_HOSTS", "127.0.0.1:9042"), "scylla hosts")
	flag.Parse()

	rows := Generate(seed, *devices, *days, *perDay)
	if *load {
		if err := loadRows(*hosts, *table, rows); err != nil {
			fmt.Fprintln(os.Stderr, "load:", err)
			os.Exit(1)
		}
		fmt.Printf("loaded %d rows into telemetry.%s\n", len(rows), *table)
		return
	}
	switch *out {
	case "count":
		fmt.Println(len(rows))
	default:
		w := os.Stdout
		fmt.Fprintln(w, "device_id,day,event_time,metric,value,region,status")
		for _, x := range rows {
			fmt.Fprintf(w, "%s,%s,%s,%s,%.2f,%s,%s\n", x.DeviceID,
				x.Day.Format("2006-01-02"), x.EventTime.Format(time.RFC3339),
				x.Metric, x.Value, x.Region, x.Status)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// batchChunkRows — максимум строк в одном батче. readings_bad (партиция =
// device_id, 14*96=1344 строк) и особенно readings_hot (партиция = region,
// 125*14*96=168000 строк) не помещаются в один батч без риска упереться в
// batch_size_(warn|fail)_threshold — резать КАЖДУЮ партицию на чанки такого
// размера, батч не пересекает границу партиции (chunk строго внутри группы).
const batchChunkRows = 200

// tableSpec описывает, как грузить один Reading в конкретную таблицу telemetry:
// INSERT (порядок столбцов = порядок PRIMARY KEY этой таблицы в schema.cql),
// функцию партиционного ключа (для группировки строк по партициям — Generate()
// уже отдаёт их смежными блоками по устройству/суткам, группировка одним
// проходом) и функцию биндов под prepared-плейсхолдеры.
type tableSpec struct {
	insertCQL    string
	partitionKey func(Reading) string
	bindArgs     func(Reading) []interface{}
}

func specFor(table string) (tableSpec, error) {
	switch table {
	case "readings":
		return tableSpec{
			insertCQL: `INSERT INTO telemetry.readings ` +
				`(device_id, day, event_time, metric, value, region, status) VALUES (?,?,?,?,?,?,?)`,
			partitionKey: func(r Reading) string {
				return r.DeviceID + "|" + r.Day.Format("2006-01-02")
			},
			bindArgs: func(r Reading) []interface{} {
				return []interface{}{r.DeviceID, r.Day, r.EventTime, r.Metric, r.Value, r.Region, r.Status}
			},
		}, nil
	case "readings_bad":
		// Плохая модель: партиция ТОЛЬКО device_id (день не входит в ключ) ->
		// одна партиция копит все сутки устройства.
		return tableSpec{
			insertCQL: `INSERT INTO telemetry.readings_bad ` +
				`(device_id, event_time, metric, value, region, status) VALUES (?,?,?,?,?,?)`,
			partitionKey: func(r Reading) string { return r.DeviceID },
			bindArgs: func(r Reading) []interface{} {
				return []interface{}{r.DeviceID, r.EventTime, r.Metric, r.Value, r.Region, r.Status}
			},
		}, nil
	case "readings_hot":
		// Hot-partition модель: партиция ТОЛЬКО region (4 значения) -> все
		// устройства+сутки одного региона в одной гигантской партиции.
		return tableSpec{
			insertCQL: `INSERT INTO telemetry.readings_hot ` +
				`(region, event_time, device_id, metric, value, status) VALUES (?,?,?,?,?,?)`,
			partitionKey: func(r Reading) string { return r.Region },
			bindArgs: func(r Reading) []interface{} {
				return []interface{}{r.Region, r.EventTime, r.DeviceID, r.Metric, r.Value, r.Status}
			},
		}, nil
	default:
		return tableSpec{}, fmt.Errorf("unknown -table %q (expected readings|readings_bad|readings_hot)", table)
	}
}

// loadRows грузит rows в telemetry.<table> через gocql (шард-осведомлённый
// форк ScyllaDB), prepared INSERT в UNLOGGED-батчах. Каждый батч строго внутри
// ОДНОЙ партиции целевой таблицы (границы партиции определяет partitionKey
// конкретного tableSpec) и не длиннее batchChunkRows строк — батч через
// границу партиции сам по себе анти-паттерн (см. readings_bad/readings_hot,
// которые ЭТУ ошибку демонстрируют своей МОДЕЛЬЮ ДАННЫХ, а не батчингом
// загрузчика; загрузчик всегда корректен независимо от таблицы-получателя).
func loadRows(hosts, table string, rows []Reading) error {
	spec, err := specFor(table)
	if err != nil {
		return err
	}

	hostList := strings.Split(hosts, ",")
	for i := range hostList {
		hostList[i] = strings.TrimSpace(hostList[i])
	}

	cluster := gocql.NewCluster(hostList...)
	cluster.Keyspace = "telemetry"
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 15 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	session, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("connect %s: %w", hosts, err)
	}
	defer session.Close()

	i := 0
	for i < len(rows) {
		key := spec.partitionKey(rows[i])
		j := i
		for j < len(rows) && spec.partitionKey(rows[j]) == key {
			j++
		}
		for start := i; start < j; start += batchChunkRows {
			end := start + batchChunkRows
			if end > j {
				end = j
			}
			batch := session.NewBatch(gocql.UnloggedBatch)
			for _, r := range rows[start:end] {
				batch.Query(spec.insertCQL, spec.bindArgs(r)...)
			}
			if err := session.ExecuteBatch(batch); err != nil {
				return fmt.Errorf("batch insert table=%s partition=%q rows[%d:%d]: %w", table, key, start, end, err)
			}
		}
		i = j
	}
	return nil
}
