// Probe для проверки момента, с которого начинает действовать триггер,
// созданный ПОД конкурентной нагрузкой уже открытых соединений.
//
// Зачем отдельная программа, а не loadgen: loadgen выполняет операции строго
// последовательно, в один вызов за раз, поэтому на нём нельзя утверждать
// ничего про «те же соединения пула» под конкуренцией. Здесь N воркеров, у
// каждого СВОЁ соединение, и все они держат его открытым всё время опыта.
//
// Барьер здесь программный, а не по данным. Ранняя версия этого замера делила
// строки по max(id), но такое деление НЕ строго: значения sequence выдаются вне
// MVCC (nextval не откатывается и не ждёт commit), а SELECT max(id) видит
// только закоммиченные строки. Значит строка могла получить id ДО commit DDL,
// а стать видимой после — и попасть не в ту половину.
//
// Здесь порядок обеспечен явно:
//
//	фаза A — воркеры пишут customer='before', соединения открыты и прогреты;
//	         скрипт в это время выполняет CREATE TRIGGER;
//	барьер — probe ждёт строку в stdin. Скрипт пишет её ТОЛЬКО после того, как
//	         команда CREATE TRIGGER вернула управление, то есть после COMMIT;
//	фаза B — те же самые соединения пишут customer='after' ровно N раз.
//
// Проверяемое утверждение: среди строк 'after' не должно быть ни одной, куда
// триггер не применился. Это уже про конкурентные соединения и про строгую
// границу «после commit», а не про приближение по id.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", "postgres://zdt:zdt@localhost:5457/zdt", "PostgreSQL DSN")
	workers := flag.Int("workers", 8, "число конкурентных воркеров (у каждого своё соединение)")
	afterEach := flag.Int("after-each", 25, "сколько вставок делает каждый воркер в фазе B")
	flag.Parse()

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "разбор DSN: %v\n", err)
		os.Exit(1)
	}
	// Пул фиксированного размера: все соединения открываются до опыта и живут
	// до конца — именно их «возраст» и проверяется.
	cfg.MinConns = int32(*workers)
	cfg.MaxConns = int32(*workers)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "подключение: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Форсируем реальное открытие всех соединений до начала опыта.
	if err := warmUp(ctx, pool, *workers); err != nil {
		fmt.Fprintf(os.Stderr, "прогрев пула: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("пул открыт: %d соединений\n", *workers)

	var beforeCnt, afterCnt, errCnt int64
	barrier := make(chan struct{})

	// Барьер: строка в stdin. Скрипт пишет её после возврата CREATE TRIGGER.
	go func() {
		bufio.NewScanner(os.Stdin).Scan()
		close(barrier)
	}()

	var wg sync.WaitGroup
	acquireErr := make(chan error, *workers)
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Соединение берётся из пула ОДИН раз и удерживается за воркером до
			// конца опыта. Это принципиально: через pool.Exec пул выбирал бы
			// соединение заново на каждую операцию, и утверждение «в фазе B
			// писали те же соединения, что были открыты до DDL» осталось бы
			// недоказанным — именно оно здесь и проверяется.
			conn, err := pool.Acquire(ctx)
			if err != nil {
				acquireErr <- err
				return
			}
			defer conn.Release()

			for {
				select {
				case <-barrier:
					// Фаза B: фиксированное число вставок ПОСЛЕ commit DDL —
					// тем же закреплённым соединением.
					for i := 0; i < *afterEach; i++ {
						if _, err := conn.Exec(ctx,
							"INSERT INTO orders (customer, amount_cents) VALUES ('after', $1)", id); err != nil {
							atomic.AddInt64(&errCnt, 1)
						} else {
							atomic.AddInt64(&afterCnt, 1)
						}
					}
					return
				default:
					if _, err := conn.Exec(ctx,
						"INSERT INTO orders (customer, amount_cents) VALUES ('before', $1)", id); err != nil {
						atomic.AddInt64(&errCnt, 1)
					} else {
						atomic.AddInt64(&beforeCnt, 1)
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}(w)
	}
	wg.Wait()
	close(acquireErr)
	if err := <-acquireErr; err != nil {
		fmt.Fprintf(os.Stderr, "воркер не смог закрепить соединение: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("фаза A (до барьера): вставлено=%d\n", beforeCnt)
	fmt.Printf("фаза B (после барьера): вставлено=%d\n", afterCnt)
	fmt.Printf("ошибок=%d\n", errCnt)
}

// warmUp одновременно занимает все соединения пула, заставляя их открыться.
func warmUp(ctx context.Context, pool *pgxpool.Pool, n int) error {
	conns := make([]*pgxpool.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	for i := 0; i < n; i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		if _, err := c.Exec(ctx, "SELECT 1"); err != nil {
			return err
		}
		conns = append(conns, c)
	}
	return nil
}
