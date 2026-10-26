package main

// rebuild.go — задача 6: пересборка проекции "с нуля" (blue-green), пока
// онлайн-проектор ПРОДОЛЖАЕТ работать и читатели ПРОДОЛЖАЮТ читать боевую
// таблицу. Статья 3 серии («Проекции вглубь») честно вынесла эту тему за
// охват — здесь она закрывается отдельным замером, дозамером к артефакту 03.
//
// Схема: shadow-проекция (`balances_shadow`, отдельная таблица, отдельный
// чекпоинт `balances-shadow`) реплеится с global_pos=0 параллельно с тем,
// что боевой online-проектор (`balances`, чекпоинт `balances`, тот же
// ProjectBatch, что и в задаче 3) продолжает применять события к боевой
// таблице. Параллельно с обеими проекциями работает независимый писатель
// (обычные ConsumeOxygen через oneCommandPostgres — тот же командный путь,
// что в задаче 1) — он и производит "хвост" событий, накопившийся за время
// пересборки, который shadow обязана добрать ПЕРЕД переключением. Как только
// shadow догнала всё, что видит (включая хвост от писателя), происходит
// атомарная подмена: тройной ALTER TABLE ... RENAME внутри одной транзакции
// Postgres, вместе с переносом позиции чекпоинта online-проектора на
// позицию shadow — это ключевая деталь, без которой online-проектор после
// подмены применил бы уже примененные shadow события ПОВТОРНО (его
// собственный чекпоинт остался бы отставшим от нового содержимого таблицы,
// на которую он теперь физически указывает).
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	shadowTableName      = "balances_shadow"
	shadowProjectorName  = "balances-shadow"
	shadowBatchLimit     = 2000 // крупная пачка — shadow обязана обгонять online, иначе "пересборка без остановки" нечего измерять
	onlineRebuildBatch   = 50   // мелкая пачка у online НА ВРЕМЯ ЭТОГО ЗАМЕРА — держит его "в работе" дольше, чем длится shadow-пересборка (см. ассерт про ненулевое продвижение)
	onlineRebuildSleep   = 5 * time.Millisecond
	shadowDrainPollDelay = 2 * time.Millisecond // пауза между проверками "писатель ещё не закончил, а пачка уже пуста"
	probeInterval        = 3 * time.Millisecond
)

// BlueGreenResult — измерения одного прогона `-mode rebuild-bluegreen`.
// Reconcile*Diff — количество расходящихся стримов из FULL OUTER JOIN сверки
// (см. reconcileDiff ниже); ОБЯЗАНЫ быть 0, иначе пересборка/подмена дали
// неверный результат. Постусловие проверяет вызывающий shell-скрипт (тот же
// стиль, что у runVersioningRead/04-versioning.sh: Go считает и печатает,
// скрипт падает через fail(), если число не 0/не то, что требуется).
type BlueGreenResult struct {
	Mode                    string  `json:"mode"`
	HistoryEventsAtStart    int64   `json:"history_events_at_start"`
	WriterEventsAppended    int64   `json:"writer_events_appended"`
	RebuildMs               float64 `json:"rebuild_ms"`
	ShadowEventsReplayed    int64   `json:"shadow_events_replayed"`
	OnlineProgressEvents    int64   `json:"online_progress_events"`
	ProbeChecks             int64   `json:"probe_checks"`
	ProbeFailures           int64   `json:"probe_failures"`
	ReconcileShadowVsFold   int64   `json:"reconcile_shadow_vs_fold_diff"`
	ReconcilePostSwapVsFold int64   `json:"reconcile_post_swap_vs_fold_diff"`
}

// checkpointCursor — текущий чекпоинт проектора ПАРОЙ (xid, global_pos);
// ('0', 0), если строки ещё нет (проектор ни разу не запускался). Пара, а не
// одно число: global_pos выдаётся до коммита и потому не является порядком
// коммитов — см. sql/01-schema.sql и go/projector.go.
func checkpointCursor(ctx context.Context, pool *pgxpool.Pool, projector string) (string, int64, error) {
	var xid string
	var pos int64
	err := pool.QueryRow(ctx,
		`SELECT COALESCE((SELECT xid::text FROM checkpoints WHERE projector = $1), '0'),
		        COALESCE((SELECT global_pos FROM checkpoints WHERE projector = $1), 0)`,
		projector).Scan(&xid, &pos)
	return xid, pos, err
}

// checkpointPos — только позиция, для мест, где важно лишь «продвинулся или
// нет» (ассерт 3 прогона blue-green).
func checkpointPos(ctx context.Context, pool *pgxpool.Pool, projector string) (int64, error) {
	_, pos, err := checkpointCursor(ctx, pool, projector)
	return pos, err
}

// reconcileDiff — та же сверка, что в scripts/03-projections.sh: FULL OUTER
// JOIN между сверткой events и таблицей проекции table, а не INNER JOIN —
// INNER молча слеп к стриму, потерянному проекцией ЦЕЛИКОМ (см. FIXTURES.md,
// артефакт 03, «Фикс-раунд 1» — та же ловушка, не повторяем её здесь).
// Возвращает число расходящихся стримов; 0 — сверка прошла.
//
// table подставляется через fmt.Sprintf по той же причине и с той же
// оговоркой безопасности, что и в ProjectBatchInto (go/projector.go):
// источник — фиксированные константы этого файла, не пользовательский ввод.
func reconcileDiff(ctx context.Context, pool *pgxpool.Pool, table string) (int64, error) {
	// ПЕРЕД сверкой сумм — проверка списка допустимых типов событий.
	//
	// Без неё сверка слепа к неизвестному типу ровно так же, как был слеп
	// проектор: `CASE ... ELSE 0` сворачивает незнакомое событие в ноль,
	// проектор (до фикса) применял ноль, и обе стороны сходились на
	// неверном значении. Ревью показало это контрпримером OxygenLeaked:
	// diff_rows=0 при полностью потерянном событии. Сумма не может поймать
	// то, что обе её стороны одинаково не умеют считать, — нужна отдельная
	// проверка ДРУГОГО рода, не арифметическая.
	if err := assertKnownEventTypes(ctx, pool); err != nil {
		return 0, err
	}
	q := fmt.Sprintf(`
		SELECT count(*) FROM (
		  SELECT e.stream_id,
		         sum(CASE WHEN e.event_type = 'OxygenAdded' THEN (e.data->>'liters')::numeric
		                  WHEN e.event_type = 'OxygenConsumed' THEN -(e.data->>'liters')::numeric
		                  ELSE 0 END) AS folded
		  FROM events e GROUP BY e.stream_id
		) f FULL OUTER JOIN %s b USING (stream_id)
		WHERE f.stream_id IS NULL
		   OR b.stream_id IS NULL
		   OR abs(f.folded - b.liters) > 0.0001`, table)
	var diff int64
	err := pool.QueryRow(ctx, q).Scan(&diff)
	return diff, err
}

// knownEventTypes — список типов, которые умеют сворачивать И проектор, И
// сверка. Всё, чего здесь нет, обе стороны свернули бы в ноль одинаково,
// поэтому список один на двоих и проверяется явно.
var knownEventTypes = []string{"TankRegistered", "OxygenAdded", "OxygenConsumed"}

// assertKnownEventTypes падает, если в events есть тип события вне списка.
// Ветка `ELSE 0` в сверке выше после этой проверки недостижима — но
// недостижима она именно потому, что проверка есть, а не сама по себе.
func assertKnownEventTypes(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx,
		`SELECT event_type, count(*) FROM events
		 WHERE event_type <> ALL($1) GROUP BY event_type ORDER BY 2 DESC`,
		knownEventTypes)
	if err != nil {
		return err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var t string
		var n int64
		if err := rows.Scan(&t, &n); err != nil {
			return err
		}
		found = append(found, fmt.Sprintf("%q (%d шт.)", t, n))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) > 0 {
		return fmt.Errorf(
			"в events есть события неизвестных типов: %s — ни проектор, ни сверка "+
				"не умеют их сворачивать, и обе молча дали бы ноль; известные типы: %v",
			strings.Join(found, ", "), knownEventTypes)
	}
	return nil
}

// swapBlueGreen — атомарная подмена: boевая balances становится архивом,
// пересобранная balances_shadow становится боевой balances, бывшая боевая
// (теперь архив) занимает освободившееся имя balances_shadow — три ALTER
// TABLE в ОДНОЙ транзакции. DDL в Postgres транзакционен: читатель, уже
// выполняющий SELECT ... FROM balances, либо успел получить снимок ДО
// коммита (видит старые данные целиком), либо его SELECT НАЧАЛСЯ после
// коммита (видит новые данные целиком) — момента "таблицы balances не
// существует" не бывает.
//
// В ТОЙ ЖЕ транзакции чекпоинт online-проектора (checkpoints.global_pos для
// projector=projectorName) переносится на shadowFinalPos — позицию, до
// которой shadow реально дореплеила историю. Это обязательно: после подмены
// имя "balances" физически указывает на данные shadow, а старый чекпоинт
// online-проектора (позиция в ЕГО собственном, более раннем темпе
// применения) остался бы МЕНЬШЕ shadowFinalPos. Без переноса чекпоинта
// online-проектор на следующем вызове ProjectBatch прочитал бы диапазон
// global_pos, который shadow уже применила к этой же физической таблице, и
// применил бы его ВТОРОЙ раз — баланс задвоился бы. Перенос чекпоинта — не
// оптимизация, а условие корректности подмены.
func swapBlueGreen(ctx context.Context, pool *pgxpool.Pool, onlineProjector string,
	shadowFinalXid string, shadowFinalPos int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stmts := []string{
		`ALTER TABLE balances RENAME TO balances_swap_tmp`,
		fmt.Sprintf(`ALTER TABLE %s RENAME TO balances`, shadowTableName),
		fmt.Sprintf(`ALTER TABLE balances_swap_tmp RENAME TO %s`, shadowTableName),
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("swap: %s: %w", s, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO checkpoints (projector, global_pos, xid) VALUES ($1, $2, $3::xid8)
		 ON CONFLICT (projector) DO UPDATE SET global_pos = $2, xid = $3::xid8`,
		onlineProjector, shadowFinalPos, shadowFinalXid); err != nil {
		return fmt.Errorf("swap: перенос чекпоинта online-проектора: %w", err)
	}
	return tx.Commit(ctx)
}

// runRebuildBlueGreen — оркестрация всего замера задачи 6 в одном процессе:
// живой писатель, работающий online-проектор, проба чтения и сама
// shadow-пересборка с подменой — все в одних и тех же горутинах одного
// pgxpool.Pool, потому что горутины и открытые соединения не переживают
// границу процесса (каждый `-mode` — отдельный запуск бинаря), а поставить
// их на паузу между запусками было бы нечем.
func runRebuildBlueGreen(dsn string, workers, streams int, writeDuration time.Duration, streamPrefix string) {
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		die("rebuild-bluegreen: разбор dsn: %v", err)
	}
	// +4: горутина online-проектора, проба чтения, писатель делит workers
	// соединений между собой, плюс основной поток делает сверки/RENAME.
	cfg.MaxConns = int32(workers + 4)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		die("rebuild-bluegreen: пул: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		die("rebuild-bluegreen: Postgres не отвечает: %v", err)
	}

	var historyAtStart int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&historyAtStart); err != nil {
		die("rebuild-bluegreen: подсчёт истории: %v", err)
	}
	if historyAtStart == 0 {
		die("rebuild-bluegreen: история пуста — пересобирать нечего (наполните её -mode append-fine перед этим режимом)")
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf("TRUNCATE %s", shadowTableName)); err != nil {
		die("rebuild-bluegreen: очистка %s: %v", shadowTableName, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM checkpoints WHERE projector = $1`, shadowProjectorName); err != nil {
		die("rebuild-bluegreen: сброс чекпоинта %s: %v", shadowProjectorName, err)
	}

	// ===== Проба чтения: не должна видеть НИ ОДНОЙ ошибки за всё время =====
	// пересборки и подмены, включая сам момент RENAME.
	probeCtx, probeCancel := context.WithCancel(ctx)
	var probeChecks, probeFailures int64
	var probeWG sync.WaitGroup
	probeWG.Add(1)
	go func() {
		defer probeWG.Done()
		for {
			select {
			case <-probeCtx.Done():
				return
			default:
			}
			var streamID string
			var liters float64
			err := pool.QueryRow(probeCtx, `SELECT stream_id, liters FROM balances LIMIT 1`).Scan(&streamID, &liters)
			atomic.AddInt64(&probeChecks, 1)
			// pgx.ErrNoRows — "boевая таблица ещё пуста" (online-проектор не
			// успел закоммитить первую строку), это НЕ прерывание чтения,
			// а честный пустой результат непустого запроса. Считать его
			// провалом значило бы засчитывать этот же самый холодный старт
			// (до первого батча online) как "ререйм сломал чтение", хотя
			// причина ровно противоположная — таблица ещё не наполнена, а
			// не стала недоступна. context.Canceled — штатное завершение
			// пробы через probeCancel() в конце замера, тоже не провал.
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, pgx.ErrNoRows) {
				atomic.AddInt64(&probeFailures, 1)
			}
			time.Sleep(probeInterval)
		}
	}()

	// ===== Писатель: производит РЕАЛЬНЫЙ хвост новых событий, пока идёт =====
	// пересборка — без него "shadow догоняет хвост" нечем проверить: если
	// history не растёт, у shadow просто нет хвоста, а есть только
	// единовременный снимок.
	writerDone := make(chan struct{})
	var writerEvents int64
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		defer close(writerDone)
		wctx, cancel := context.WithTimeout(ctx, writeDuration)
		defer cancel()
		var inner sync.WaitGroup
		for w := 0; w < workers; w++ {
			inner.Add(1)
			go func(id int) {
				defer inner.Done()
				rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
				for wctx.Err() == nil {
					streamID := fmt.Sprintf("%s%d", streamPrefix, rnd.Intn(streams))
					if err := oneCommandPostgres(wctx, pool, streamID); err == nil {
						atomic.AddInt64(&writerEvents, 1)
					}
					// Не в конфликт-режиме генератора нагрузки задачи 1: здесь
					// цель — стабильный поток новых событий для хвоста, а не
					// замер конкуренции, поэтому конфликты и прочие ошибки
					// просто не считаются успехом и не логируются отдельно.
				}
			}(w)
		}
		inner.Wait()
	}()

	// ===== Online-проектор: та же ProjectBatch, что в задаче 3, но с =====
	// мелкой пачкой и паузой — держит его "в работе" дольше, чем длится
	// shadow-пересборка, чтобы замер захватывал интервал, где online
	// РЕАЛЬНО продвигается параллельно, а не успел закончить заранее.
	onlineStop := make(chan struct{})
	var onlineWG sync.WaitGroup
	onlineWG.Add(1)
	go func() {
		defer onlineWG.Done()
		for {
			select {
			case <-onlineStop:
				return
			default:
			}
			if _, err := ProjectBatch(ctx, pool, projectorName, onlineRebuildBatch); err != nil {
				die("rebuild-bluegreen: online ProjectBatch: %v", err)
			}
			time.Sleep(onlineRebuildSleep)
		}
	}()

	onlineStart, err := checkpointPos(ctx, pool, projectorName)
	if err != nil {
		die("rebuild-bluegreen: чтение чекпоинта online: %v", err)
	}

	// ===== Shadow-пересборка: реплей с global_pos=0 в отдельную таблицу, =====
	// пока online и писатель продолжают работать. Дренирует, пока пачки не
	// станут пустыми И писатель не закончит — иначе можно остановиться
	// раньше, чем дописан хвост, случайно попавший между последней непустой
	// пачкой и завершением писателя.
	tStart := time.Now()
	var shadowTotal int64
drainLoop:
	for {
		n, err := ProjectBatchInto(ctx, pool, shadowProjectorName, shadowTableName, shadowBatchLimit)
		if err != nil {
			die("rebuild-bluegreen: shadow ProjectBatchInto: %v", err)
		}
		shadowTotal += int64(n)
		if n == 0 {
			select {
			case <-writerDone:
				break drainLoop
			default:
				time.Sleep(shadowDrainPollDelay)
			}
		}
	}

	onlineAtShadowDone, err := checkpointPos(ctx, pool, projectorName)
	if err != nil {
		die("rebuild-bluegreen: чтение чекпоинта online (после дренажа shadow): %v", err)
	}
	onlineProgress := onlineAtShadowDone - onlineStart

	// Останавливаем online ПЕРЕД подменой и ждём, пока его текущий вызов
	// ProjectBatch (если он в это время шёл) действительно завершится —
	// иначе гонка между его коммитом (пишет в checkpoints по имени
	// projectorName) и нашим переносом чекпоинта внутри swapBlueGreen могла
	// бы отменить один из двух результатов в зависимости от порядка
	// коммитов.
	close(onlineStop)
	onlineWG.Wait()

	diffShadow, err := reconcileDiff(ctx, pool, shadowTableName)
	if err != nil {
		die("rebuild-bluegreen: сверка shadow против свёртки: %v", err)
	}

	shadowFinalXid, shadowFinalPos, err := checkpointCursor(ctx, pool, shadowProjectorName)
	if err != nil {
		die("rebuild-bluegreen: чтение финального чекпоинта shadow: %v", err)
	}

	if err := swapBlueGreen(ctx, pool, projectorName, shadowFinalXid, shadowFinalPos); err != nil {
		die("rebuild-bluegreen: атомарная подмена: %v", err)
	}
	rebuildElapsed := time.Since(tStart)

	// После подмены "balances" физически — то, что построила shadow.
	// Дальше синхронно (писатель уже остановлен writerDone) даём
	// online-проектору догнать то немногое, что могло появиться уже после
	// переноса чекпоинта (в норме — ничего, писатель к этому моменту стоит),
	// и только затем сверяем итог: это и есть постусловие "боевая проекция
	// после переключения совпадает с тем, что построил online, догнав
	// хвост" — а не сверка ДО того, как online успел отреагировать на новое
	// содержимое таблицы.
	for {
		n, err := ProjectBatch(ctx, pool, projectorName, shadowBatchLimit)
		if err != nil {
			die("rebuild-bluegreen: online ProjectBatch после подмены: %v", err)
		}
		if n == 0 {
			break
		}
	}

	diffPostSwap, err := reconcileDiff(ctx, pool, "balances")
	if err != nil {
		die("rebuild-bluegreen: сверка balances после подмены: %v", err)
	}

	probeCancel()
	probeWG.Wait()
	writerWG.Wait()

	out := BlueGreenResult{
		Mode:                    "rebuild-bluegreen",
		HistoryEventsAtStart:    historyAtStart,
		WriterEventsAppended:    atomic.LoadInt64(&writerEvents),
		RebuildMs:               ms(rebuildElapsed),
		ShadowEventsReplayed:    shadowTotal,
		OnlineProgressEvents:    onlineProgress,
		ProbeChecks:             atomic.LoadInt64(&probeChecks),
		ProbeFailures:           atomic.LoadInt64(&probeFailures),
		ReconcileShadowVsFold:   diffShadow,
		ReconcilePostSwapVsFold: diffPostSwap,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		die("rebuild-bluegreen: вывод: %v", err)
	}
}
