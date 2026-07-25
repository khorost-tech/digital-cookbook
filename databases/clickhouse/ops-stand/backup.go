package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phaseBackup — Step 3 брифа: BACKUP TABLE ... TO Disk(...)/File(...) +
// RESTORE round-trip, проверка восстановления. BACKUP/RESTORE в ClickHouse
// синхронны по умолчанию (запрос блокируется до готовности, возвращает
// (id, status)) — в отличие от мутаций тут нет отдельного is_done для
// polling, таймаут — через контекст с дедлайном.
//
// backupPath должен попадать под <backups><allowed_path> в конфиге сервера
// (см. ../config/backups.xml, смонтирован в ../compose/compose.yml) — путь
// внутри уже существующего тома clickhouse-data (/var/lib/clickhouse/...),
// отдельный docker-том не понадобился.
func phaseBackup(ctx context.Context, ch clickhouse.Conn, table, backupPath string, timeout time.Duration) {
	fmt.Println("\n=== BACKUP/RESTORE round-trip (Step 3 брифа) ===")

	beforeCount, beforeChecksum, err := tableChecksum(ctx, ch, table)
	if err != nil {
		log.Fatalf("checksum before backup: %v", err)
	}
	fmt.Printf("[backup] до бэкапа: count()=%d, checksum(sum(cityHash64(*)))=%d\n", beforeCount, beforeChecksum)

	backupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backupSQL := fmt.Sprintf("BACKUP TABLE demo.%s TO File('%s')", table, backupPath)
	start := time.Now()
	var backupID, backupStatus string
	if err := ch.QueryRow(backupCtx, backupSQL).Scan(&backupID, &backupStatus); err != nil {
		log.Fatalf("backup: %v", err)
	}
	backupDur := time.Since(start)
	fmt.Printf("[backup] BACKUP TABLE demo.%s TO File('%s'): %s (id=%s, status=%s)\n", table, backupPath, backupDur, backupID, backupStatus)

	if err := ch.Exec(ctx, fmt.Sprintf("DROP TABLE demo.%s", table)); err != nil {
		log.Fatalf("drop before restore: %v", err)
	}
	fmt.Printf("[backup] DROP TABLE demo.%s (симуляция потери данных) — таблицы больше нет\n", table)

	restoreCtx, cancel2 := context.WithTimeout(ctx, timeout)
	defer cancel2()
	restoreSQL := fmt.Sprintf("RESTORE TABLE demo.%s FROM File('%s')", table, backupPath)
	start = time.Now()
	var restoreID, restoreStatus string
	if err := ch.QueryRow(restoreCtx, restoreSQL).Scan(&restoreID, &restoreStatus); err != nil {
		log.Fatalf("restore: %v", err)
	}
	restoreDur := time.Since(start)
	fmt.Printf("[backup] RESTORE TABLE demo.%s FROM File('%s'): %s (id=%s, status=%s) — DDL таблицы восстановлен из метаданных бэкапа, руками CREATE TABLE не выполнялся\n",
		table, backupPath, restoreDur, restoreID, restoreStatus)

	afterCount, afterChecksum, err := tableChecksum(ctx, ch, table)
	if err != nil {
		log.Fatalf("checksum after restore: %v", err)
	}
	fmt.Printf("[backup] после restore: count()=%d, checksum=%d\n", afterCount, afterChecksum)

	upperStatus := strings.ToUpper(restoreStatus)
	assertFailFast(!strings.Contains(upperStatus, "FAIL") && !strings.Contains(upperStatus, "ERROR"),
		"RESTORE вернул незавершившийся статус (status=%s)", restoreStatus)
	assertFailFast(afterCount == beforeCount, "restore: count() после (%d) == до бэкапа (%d)", afterCount, beforeCount)
	assertFailFast(afterChecksum == beforeChecksum, "restore: checksum после (%d) == до бэкапа (%d) — данные побайтово совпадают", afterChecksum, beforeChecksum)
}

// tableChecksum — count() + порядко-независимая контрольная сумма всей
// таблицы (sum(cityHash64(*)) по всем строкам/колонкам) — тот же приём, что
// ../drivers/go/report.go для межрайверной сверки (там CRC32 по канонической
// строке групп, тут — построчный хеш всех колонок целиком).
func tableChecksum(ctx context.Context, conn clickhouse.Conn, table string) (count uint64, checksum uint64, err error) {
	err = conn.QueryRow(ctx, fmt.Sprintf("SELECT count(), sum(cityHash64(*)) FROM demo.%s", table)).Scan(&count, &checksum)
	return
}
