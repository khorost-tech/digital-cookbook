// Стенд #3: персистентность — что реально теряется под `kill -9`.
//
// Два сценария (-scenario):
//   - durability-loss:  пишет N ключей dur:0..dur:N-1 монотонным счётчиком,
//     на середине (i == n/2) прямо в момент записи убивает контейнер
//     `docker kill -s SIGKILL <container>` — настоящий SIGKILL, без
//     грациозного шатдауна (в отличие от `docker stop`, который посылает
//     SIGTERM и даёт Redis сохранить всё перед выходом, что дало бы
//     фиктивный «ноль потерь» на любом режиме). Печатает последний
//     подтверждённый (`SET` вернул OK) индекс K и проверяет, что контейнер
//     действительно умер (`docker inspect` → Status=exited, ExitCode=137 —
//     128+9, признак настоящего SIGKILL, не спутать с другим кодом).
//   - count-recovered: подключается к контейнеру, поднятому заново на том
//     же volume (`docker compose up -d <service>` после кила — тот же
//     контейнер, тот же volume, не пересоздание), ждёт готовности (PING с
//     ретраями), печатает INFO persistence (aof/rdb статус — признак
//     повреждения/усечения файла), фактический листинг /data (какие файлы
//     персистентности реально лежат на диске), DBSIZE (= реально
//     восстановленное число ключей, т.к. в БД кроме dur:* ничего нет —
//     FLUSHALL перед стартом durability-loss) и присутствие нескольких
//     последних перед килом ключей (хвостовая потеря или дырки).
//
// Оба сценария падают с ненулевым кодом на любой аномалии (ошибка SET до
// килла, отсутствие килла, exit_code != 137, recovered > written) — прогон,
// в котором что-то пошло не так, обязан остановить матрицу, а не доехать до
// правдоподобно выглядящего «keys lost: 0».
//
// Режим персистентности (RDB-only/AOF-always/AOF-everysec/AOF-no/hybrid)
// задаётся СНАРУЖИ, через переменные окружения compose/base.yml
// (REDIS_APPENDONLY/REDIS_APPENDFSYNC/REDIS_SAVE_SECONDS/REDIS_SAVE_CHANGES)
// при `docker compose up -d` — сам процесс контейнер не поднимает и режим
// не выставляет, только -mode — метка для читаемости лога.
//
// Адрес Redis/Valkey читается из REDIS_ADDR, по умолчанию 127.0.0.1:6379.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

func addrFromEnv() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	return addr
}

// must проверяет ошибку записи и завершает процесс, если она есть. Молча
// проглоченная ошибка записи здесь — это не просто неточность, а прямая
// порча центральной цифры стенда: «последний подтверждённый индекс» обязан
// значить именно то, что он значит — SET реально вернул OK, а не то, что
// цикл просто досчитал до какого-то i, не заметив оборванного соединения.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи: %v\n", label, err)
		os.Exit(1)
	}
}

// waitReady ждёт, пока сервер начнёт отвечать на PING (после рестарта на
// том же volume — REDIS_ADDR может быть недоступен первые мгновения, пока
// contain'ер поднимается и Redis грузит AOF/RDB). Печатает число попыток —
// это тоже часть честности замера: если восстановление заняло 15 попыток по
// 200мс, это не «мгновенно», и это должно быть видно в логе.
func waitReady(ctx context.Context, rdb *redis.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastErr error
	for time.Now().Before(deadline) {
		attempt++
		pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := rdb.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			fmt.Printf("готовность: PING ok после %d попыт(ки/ок)\n", attempt)
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("PING не ответил за %s (%d попыток), последняя ошибка: %v", timeout, attempt, lastErr)
}

// dockerKillSigkill убивает контейнер настоящим SIGKILL (не docker stop,
// который посылает SIGTERM и даёт процессу шанс сохранить состояние перед
// выходом — это дало бы фиктивный «ноль потерь» на любом durability-режиме).
func dockerKillSigkill(containerName string) error {
	cmd := exec.Command("docker", "kill", "-s", "SIGKILL", containerName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker kill -s SIGKILL %s: %v (%s)", containerName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitContainerExited ждёт, пока docker подтвердит, что контейнер реально
// умер, и возвращает его Status/ExitCode/OOMKilled — доказательство того,
// что это была настоящая смерть по сигналу, а не гонка (мы проверили
// состояние контейнера до того, как продолжить, а не понадеялись, что
// `docker kill` синхронно означает «уже умер на диске»).
func waitContainerExited(containerName string, timeout time.Duration) (status string, exitCode string, oomKilled string, err error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, e := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}}", containerName).CombinedOutput()
		if e != nil {
			err = fmt.Errorf("docker inspect %s: %v (%s)", containerName, e, strings.TrimSpace(string(out)))
			return
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) == 3 {
			status, exitCode, oomKilled = fields[0], fields[1], fields[2]
			if status == "exited" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	err = fmt.Errorf("контейнер %s не перешёл в exited за %s (последний статус: %s)", containerName, timeout, status)
	return
}

// durabilityLoss — ключевой сценарий стенда. Пишет N ключей монотонным
// счётчиком, на середине убивает контейнер настоящим SIGKILL прямо в момент
// записи (без грациозного шатдауна) и печатает последний подтверждённый
// индекс — это K, относительно которого затем (в отдельном вызове,
// count-recovered, после рестарта на том же volume) считается реальная
// потеря.
func durabilityLoss(ctx context.Context, rdb *redis.Client, containerName string, n int, mode string) {
	fmt.Printf("=== durability-loss mode=%s container=%s n=%d ===\n", mode, containerName, n)

	must("FLUSHALL(before)", rdb.FlushAll(ctx))
	fmt.Println("FLUSHALL(before): ok — БД пуста перед началом записи")

	start := time.Now()
	var lastConfirmed int
	var elapsedUntilKill time.Duration
	killed := false
	for i := 0; i < n; i++ {
		// Ошибка SET здесь — всегда настоящий сбой, а не «ожидаемый шум рядом
		// с киллом»: килл срабатывает ТОЛЬКО после подтверждённого SET и сразу
		// же выходит из цикла (break ниже), поэтому ни одна команда никогда не
		// отправляется после килла. Молчаливый break на этом месте позволил бы
		// прогону, в котором килла вообще не было, доехать до count-recovered и
		// отчитаться безупречно выглядящим «keys lost: 0» — то есть ровно та
		// подделка, ради предотвращения которой этот стенд и написан.
		must(fmt.Sprintf("SET dur:%d", i), rdb.Set(ctx, fmt.Sprintf("dur:%d", i), i, 0))
		lastConfirmed = i
		if i == n/2 {
			elapsedUntilKill = time.Since(start)
			// имитируем сбой прямо в момент записи, без грациозного шатдауна
			if err := dockerKillSigkill(containerName); err != nil {
				fmt.Fprintf(os.Stderr, "docker kill: %v\n", err)
				os.Exit(1)
			}
			killed = true
			break
		}
	}

	fmt.Println("last confirmed write index:", lastConfirmed)
	fmt.Printf("подтверждено записей (0..%d включительно): %d из n=%d запланированных\n", lastConfirmed, lastConfirmed+1, n)
	if elapsedUntilKill > 0 {
		rate := float64(lastConfirmed+1) / elapsedUntilKill.Seconds()
		fmt.Printf("время от начала записи до SIGKILL: %s (SET без пайплайна, один клиент)\n", elapsedUntilKill)
		fmt.Printf("средняя скорость записи до килла: %.1f ops/s\n", rate)
	}

	// Килла не было — значит ячейка матрицы недействительна. Ненулевой выход
	// останавливает ops/persistence-kill-matrix.sh (set -e) вместо того, чтобы
	// дать ему прогнать count-recovered и записать фиктивный «0 потеряно».
	if !killed {
		fmt.Fprintln(os.Stderr, "ошибка: цикл дошёл до конца без килла (n слишком мало относительно n/2?) — сценарий рассчитан на SIGKILL в середине, ячейка матрицы недействительна")
		os.Exit(1)
	}

	status, exitCode, oomKilled, err := waitContainerExited(containerName, 15*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "проверка состояния контейнера после kill: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("container state after SIGKILL: status=%s exit_code=%s oom_killed=%s\n", status, exitCode, oomKilled)
	// Единственная гарантия того, что замер вообще про SIGKILL. Если код не
	// 137 (128+9) — контейнер умер как-то иначе (SIGTERM, OOM, что угодно), и
	// любая цифра потерь из такого прогона говорит не о том, о чём заявлено.
	if exitCode != "137" {
		fmt.Fprintf(os.Stderr, "ошибка: exit_code=%s, ожидался 137 (128+9) для SIGKILL — контейнер умер не от SIGKILL, ячейка матрицы недействительна\n", exitCode)
		os.Exit(1)
	}
	fmt.Println("exit_code=137 (128+9) — подтверждённый настоящий SIGKILL")
}

// listDataDir печатает фактическое содержимое /data в контейнере — какие
// файлы персистентности реально лежат на диске после рестарта (dump.rdb,
// appendonlydir/ и их размеры). Без этого утверждения вида «на диске пустой
// dump.rdb» остаются догадкой: при `save 60 1`, не успевшем сработать,
// файла может не быть вовсе. Печатаем факт, а не предположение.
func listDataDir(containerName string) {
	out, err := exec.Command("docker", "exec", containerName, "sh", "-c", "ls -la /data; echo '--- appendonlydir:'; ls -la /data/appendonlydir 2>/dev/null || echo '(нет appendonlydir)'").CombinedOutput()
	if err != nil {
		fmt.Printf("data dir listing: не удалось получить (%v)\n", err)
		return
	}
	fmt.Println("--- фактическое содержимое /data после рестарта:")
	fmt.Println(strings.TrimSpace(string(out)))
	fmt.Println("--- конец листинга /data")
}

// countRecovered подключается к контейнеру, поднятому заново на том же
// volume, ждёт готовности и печатает: статус AOF/RDB-загрузки (признак
// повреждения/усечения файла после обрыва посреди записи), фактическое
// содержимое /data (какие файлы персистентности реально есть на диске),
// реально восстановленное число ключей (DBSIZE — в БД кроме dur:* ничего
// нет) и присутствие нескольких ключей у самого хвоста (перед килом) —
// показывает, теряется ли ровный суффикс или дыры вперемешку.
func countRecovered(ctx context.Context, rdb *redis.Client, containerName, mode string, confirmed int) {
	fmt.Printf("=== count-recovered mode=%s confirmed=%d ===\n", mode, confirmed)

	if err := waitReady(ctx, rdb, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "готовность после рестарта: %v\n", err)
		os.Exit(1)
	}

	listDataDir(containerName)

	info, err := rdb.Info(ctx, "persistence").Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "INFO persistence: ошибка: %v\n", err)
		os.Exit(1)
	}
	fields := []string{"loading:", "aof_enabled:", "aof_last_write_status:", "aof_last_bgrewrite_status:", "rdb_last_bgsave_status:", "rdb_last_load_keys_loaded:", "rdb_changes_since_last_save:"}
	for _, line := range strings.Split(info, "\r\n") {
		for _, f := range fields {
			if strings.HasPrefix(line, f) {
				fmt.Println("INFO persistence:", line)
			}
		}
	}

	recovered, err := rdb.DBSize(ctx).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "DBSIZE: ошибка: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("recovered key count (DBSIZE):", recovered)

	if confirmed >= 0 {
		writtenTotal := int64(confirmed + 1)
		lost := writtenTotal - recovered
		// recovered > written физически невозможен на чистом volume: значит в
		// БД лежат ключи dur:* от предыдущей ячейки матрицы (пропущенный
		// `down -v`), и volume не был чистым. Зажимать это в ноль — значит
		// превратить загрязнение стенда в заголовочное «keys lost: 0».
		if lost < 0 {
			fmt.Fprintf(os.Stderr, "ошибка: восстановлено (%d) больше, чем подтверждено записей (%d) — volume не был чистым (остались ключи dur:* от предыдущего прогона, пропущен `down -v`); ячейка матрицы недействительна\n", recovered, writtenTotal)
			os.Exit(1)
		}
		fmt.Printf("keys written (confirmed 0..%d): %d\n", confirmed, writtenTotal)
		fmt.Printf("keys lost: %d\n", lost)

		// хвост: последние несколько ключей перед килом — присутствуют ли,
		// показывает суффиксную (ровную) потерю vs дыры.
		tailN := 10
		present := 0
		for i := confirmed; i > confirmed-tailN && i >= 0; i-- {
			key := fmt.Sprintf("dur:%d", i)
			n, err := rdb.Exists(ctx, key).Result()
			if err != nil {
				fmt.Fprintf(os.Stderr, "EXISTS %s: ошибка: %v\n", key, err)
				continue
			}
			exists := n == 1
			if exists {
				present++
			}
			fmt.Printf("tail check: %s exists=%v\n", key, exists)
		}
		fmt.Printf("tail check summary: %d/%d последних ключей перед килом присутствуют\n", present, tailN)
	} else {
		fmt.Println("(-confirmed не задан — печатаю только DBSIZE, дельту потерь считать по логу durability-loss вручную)")
	}
}

func main() {
	scenario := flag.String("scenario", "", "durability-loss | count-recovered")
	n := flag.Int("n", 5000, "durability-loss: сколько ключей планируем записать (кил — на n/2)")
	container := flag.String("container", "redis-master", "имя docker-контейнера (docker kill/inspect)")
	mode := flag.String("mode", "unspecified", "метка режима персистентности для лога (rdb-only|aof-always|aof-everysec|aof-no|hybrid) — сам режим задаётся через compose/base.yml, не этим флагом")
	confirmed := flag.Int("confirmed", -1, "count-recovered: последний подтверждённый индекс из лога durability-loss (для авто-подсчёта потерь); -1 = не считать дельту")
	flag.Parse()

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addrFromEnv()})
	defer rdb.Close()

	switch *scenario {
	case "durability-loss":
		if err := rdb.Ping(ctx).Err(); err != nil {
			fmt.Fprintln(os.Stderr, "ping failed:", err)
			os.Exit(1)
		}
		durabilityLoss(ctx, rdb, *container, *n, *mode)
	case "count-recovered":
		countRecovered(ctx, rdb, *container, *mode, *confirmed)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: durability-loss | count-recovered")
		os.Exit(1)
	}
}
