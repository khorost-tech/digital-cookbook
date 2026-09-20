// Раннер проб на pgx/v5 — том же драйвере, которым стенд ходит в PostgreSQL.
//
// Смысл прогонять один набор разными клиентами: отличить «сервер не умеет» от
// «драйвер не умеет». Вывод общий для всех раннеров стенда:
// "<id>\tOK|FAIL\t<сообщение>".
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

const msgLimit = 160

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > msgLimit {
		// Обрезаем по рунам, а не по байтам: сообщения приходят с кириллицей,
		// и обрезка по байту разрубила бы символ пополам.
		r := []rune(s)
		if len(r) > msgLimit {
			r = r[:msgLimit]
		}
		return string(r)
	}
	return s
}

func main() {
	probes := flag.String("probes", "../../probes.tsv", "путь к probes.tsv")
	dsn := flag.String("dsn", os.Getenv("PICO_DSN_HOST"), "DSN Picodata")
	flag.Parse()

	if *dsn == "" {
		*dsn = "postgres://admin:Picodata1@127.0.0.1:5442/picodata?sslmode=disable"
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "подключение: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close(ctx) }()

	f, err := os.Open(*probes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "открыть %s: %v\n", *probes, err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		id, sql := parts[0], parts[2]

		// Exec, а не Query: часть проб — DDL и SET, у них нет строк результата.
		if _, execErr := conn.Exec(ctx, sql); execErr != nil {
			fmt.Printf("%s\tFAIL\t%s\n", id, oneLine(execErr.Error()))

			// После ошибки соединение может остаться непригодным, и тогда все
			// следующие пробы вернули бы FAIL по инерции — матрица выглядела бы
			// правдоподобно и была бы ложной. Поэтому переподключаемся.
			_ = conn.Close(ctx)
			conn, err = pgx.Connect(ctx, *dsn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "переподключение после пробы %s: %v\n", id, err)
				os.Exit(1)
			}
			continue
		}
		fmt.Printf("%s\tOK\t\n", id)
	}
	if scErr := sc.Err(); scErr != nil {
		fmt.Fprintf(os.Stderr, "чтение проб: %v\n", scErr)
		os.Exit(1)
	}
}
