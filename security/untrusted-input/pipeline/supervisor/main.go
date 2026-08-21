// Супервизор: единственный, кто разговаривает с очередью.
//
// Он НЕ разбирает содержимое файлов. Его работа — взять задачу, положить
// задание в общий каталог, дождаться результата от парсера и подтвердить
// обработку. Парсер живёт в отдельном контейнере, у которого нет сети вообще.
//
// Такое разделение появилось после честной проверки первой версии стенда:
// тогда разбор шёл в контейнере, подключённом к сети с Redis, и утверждение
// «парсер лишён сети» было неверным. Сеть с пометкой internal убирает выход
// наружу, но участники такой сети продолжают видеть друг друга — скомпрометированный
// парсер мог бы читать и править очередь.
//
// СЕМАНТИКА ОЧЕРЕДИ. Задача берётся через BLMove в отдельный список processing,
// а не через BRPop. Разница принципиальна: BRPop удаляет элемент сразу, и падение
// между чтением и записью результата теряет задачу навсегда. Здесь задача
// остаётся в processing до явного подтверждения, а зависшие возвращаются обратно.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	queueKey      = "pipeline:tasks"
	processingKey = "pipeline:processing"
	poisonKey     = "pipeline:poison"
	attemptsKey   = "pipeline:attempts"
	resultPrefix  = "pipeline:result:"

	// Ответ парсера — это несколько полей; всё, что заметно больше, уже не ответ.
	// Парсер пишет в общий том, поэтому его вывод считается недоверенным.
	maxResultBytes = 64 << 10
)

// Сколько ждём результат от парсера, прежде чем счесть задачу зависшей, и
// насколько задача может пролежать в processing, прежде чем вернётся в очередь.
// Оба значения вынесены в env: стенду нужны секунды, чтобы сценарий падения
// проверялся за обозримое время, рабочему сервису — минуты.
var (
	parseDeadline = durationOr("PARSE_DEADLINE", 45*time.Second)
	staleAfter    = durationOr("STALE_AFTER", 2*time.Minute)
	sweepInterval = durationOr("SWEEP_INTERVAL", 15*time.Second)
)

func durationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

type parseResult struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Format   string `json:"format,omitempty"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

type supervisor struct {
	rdb      *redis.Client
	jobsDir  string
	doneDir  string
	// Счётчик попыток живёт в Redis (hash pipeline:attempts), а не здесь:
	// иначе перезапуск супервизора обнулял бы его, и лимит попыток
	// не ограничивал бы ничего.
	maxTries int
}

// Задание для парсера — файл в общем каталоге. Никакой сети между супервизором
// и парсером нет: только файловая система.
func (s *supervisor) submitJob(taskID string) error {
	tmp := filepath.Join(s.jobsDir, "."+taskID)
	final := filepath.Join(s.jobsDir, taskID)
	if err := os.WriteFile(tmp, []byte(taskID), 0o640); err != nil {
		return err
	}
	// Переименование атомарно: парсер не увидит файл наполовину записанным.
	return os.Rename(tmp, final)
}

// Чтение ответа парсера. Каталог общий с недоверенной зоной, поэтому
// проверять надо не путь, а то, что реально открылось.
//
// ПОРЯДОК ЗДЕСЬ — ЧАСТЬ ЗАЩИТЫ. Сначала открываем, потом проверяем открытый
// дескриптор через Fstat. Проверять именем до открытия недостаточно: между
// проверкой и открытием парсер успевает подменить объект, а проверенное имя
// к тому моменту описывает уже не то, что мы держим в руках.
//
// Флаги открытия закрывают два разных обхода:
//   - O_NOFOLLOW — символическая ссылка, ведущая в файловую систему супервизора;
//   - O_NONBLOCK — именованный канал (FIFO). От него O_NOFOLLOW не спасает,
//     а open на FIFO без читателя с другой стороны блокируется навсегда:
//     супервизор просто зависнет, и никакой таймаут выше его не спасёт,
//     потому что он застрянет в системном вызове.
//
// Размер проверяется до чтения — иначе предел не ограничивает ничего: огромный
// файл сначала целиком оказался бы в памяти. Но и само чтение ограничено:
// размер, снятый Fstat, описывает момент проверки, а файл можно дописать.
func readAnswer(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // путь из hex-идентификатора
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	// Fstat, а не Stat по имени: проверяем именно то, что открыли.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("ответ парсера не обычный файл: %s", info.Mode().Type())
	}
	if info.Size() > maxResultBytes {
		return nil, fmt.Errorf("ответ парсера %d байт при пределе %d", info.Size(), maxResultBytes)
	}

	// На байт больше предела: только так превышение отличается от файла ровно
	// по границе. Простое чтение «до предела» молча обрезало бы ответ.
	data, err := io.ReadAll(io.LimitReader(f, maxResultBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResultBytes {
		return nil, fmt.Errorf("ответ парсера превысил предел %d байт при чтении", maxResultBytes)
	}
	return data, nil
}

// Разбор и проверка ответа. Отделено от чтения, чтобы проверялось тестами
// без файловой системы.
func validateAnswer(data []byte, taskID string) (*parseResult, error) {
	var res parseResult
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // лишние поля — признак не того ответа
	if err := dec.Decode(&res); err != nil {
		return nil, fmt.Errorf("результат нечитаем: %w", err)
	}
	// После первого объекта не должно быть ничего: иначе ответ вида
	// «{валидный}{чужой}» прошёл бы по первому объекту, а всё остальное
	// молча пропало бы из виду.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("в ответе больше одного объекта")
	}
	// Идентификатор в ответе должен совпадать с выданным заданием: иначе
	// парсер отвечает за чужую задачу.
	if res.TaskID != taskID {
		return nil, fmt.Errorf("ответ на чужую задачу: %q вместо %q", res.TaskID, taskID)
	}
	if res.Status != "ok" && res.Status != "failed" {
		return nil, fmt.Errorf("неизвестный статус %q", res.Status)
	}
	return &res, nil
}

// Ответ парсера приходит файлом в общий том. Парсер — недоверенная зона, значит
// недоверен и его ответ: скомпрометированный парсер пишет в тот же каталог и
// может подсунуть ответ любого размера, с чужим идентификатором или с полями,
// которых не бывает. Поэтому здесь не «разобрать JSON», а проверить.
func (s *supervisor) waitResult(taskID string) (*parseResult, error) {
	path := filepath.Join(s.doneDir, taskID+".json")
	deadline := time.Now().Add(parseDeadline)
	for time.Now().Before(deadline) {
		// Lstat здесь — только чтобы понять, появился ли ответ. Никаких
		// выводов о том, ЧТО именно лежит по этому пути, из него не делается:
		// решает Fstat уже открытого дескриптора внутри readAnswer.
		if _, err := os.Lstat(path); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}

		data, err := readAnswer(path)
		_ = os.Remove(path) // разбирается один раз, что бы в нём ни было
		if err != nil {
			return nil, err
		}
		return validateAnswer(data, taskID)
	}
	return nil, errors.New("парсер не ответил в отведённое время")
}

// Возврат задачи из processing обратно в очередь — ОДНОЙ атомарной операцией.
//
// Наивная пара LRem + LPush имеет окно: если супервизор упадёт между ними,
// задача исчезнет из обеих очередей. Lua-скрипт выполняется в Redis целиком,
// поэтому такого промежуточного состояния не существует. Заодно здесь же
// растёт счётчик попыток — он живёт в Redis, а не в памяти процесса, иначе
// перезапуск супервизора обнулял бы его и poison-очередь ничего не ограничивала.
var requeueScript = redis.NewScript(`
  local processing = KEYS[1]
  local ready      = KEYS[2]
  local poison     = KEYS[3]
  local attemptsK  = KEYS[4]
  local task       = ARGV[1]
  local maxTries   = tonumber(ARGV[2])

  if redis.call('LREM', processing, 1, task) == 0 then
    return 'absent'
  end

  local n = redis.call('HINCRBY', attemptsK, task, 1)
  if n > maxTries then
    redis.call('LPUSH', poison, task)
    redis.call('HDEL', attemptsK, task)
    return 'poison'
  end

  redis.call('LPUSH', ready, task)
  return 'requeued:' .. n
`)

// Возврат зависших задач: если парсер или супервизор упали, задача не должна
// остаться в processing навсегда.
func (s *supervisor) requeueStale(ctx context.Context) {
	items, err := s.rdb.LRange(ctx, processingKey, 0, -1).Result()
	if err != nil || len(items) == 0 {
		return
	}
	for _, taskID := range items {
		seen, err := s.rdb.Get(ctx, "pipeline:started:"+taskID).Result()
		if err == nil && seen != "" {
			continue // ещё в работе
		}
		// Отметки о начале нет — обработка прервалась вместе с супервизором.
		s.requeue(ctx, taskID, "обработка прервана")
	}
}

func (s *supervisor) handle(ctx context.Context, taskID string) {
	// Отметка «взята в работу» с коротким TTL: по её отсутствию находим зависшие.
	_ = s.rdb.Set(ctx, "pipeline:started:"+taskID, "1", staleAfter).Err()

	var res *parseResult
	err := s.submitJob(taskID)
	if err != nil {
		err = fmt.Errorf("не удалось передать задание парсеру: %w", err)
	} else {
		res, err = s.waitResult(taskID)
	}

	if err != nil {
		// Парсер не ответил или ответил не по форме. Это НЕ вердикт о файле,
		// а отказ обработки: задача возвращается в очередь, а не помечается
		// проваленной. Ровно ради этого различия и заведены счётчик попыток
		// с poison-очередью — иначе одно падение парсера навсегда хоронило бы
		// нормальный файл.
		log.Printf("задача %s: %v — возвращаю в очередь", taskID, err)
		s.requeue(ctx, taskID, err.Error())
		_ = s.rdb.Del(ctx, "pipeline:started:"+taskID).Err()
		return
	}

	// Парсер ответил по форме: это вердикт, его и записываем.
	payload, _ := json.Marshal(res)
	if err := s.rdb.Set(ctx, resultPrefix+taskID, string(payload), time.Hour).Err(); err != nil {
		// Результат не сохранён — подтверждать нельзя, иначе задача пропадёт.
		log.Printf("запись результата %s: %v", taskID, err)
		return
	}

	// Подтверждение: только теперь задача уходит из processing.
	if err := s.rdb.LRem(ctx, processingKey, 1, taskID).Err(); err != nil {
		log.Printf("подтверждение %s: %v", taskID, err)
	}
	_ = s.rdb.Del(ctx, "pipeline:started:"+taskID).Err()
	// Задача доведена до результата — счётчик попыток больше не нужен.
	_ = s.rdb.HDel(ctx, attemptsKey, taskID).Err()
	log.Printf("задача %s завершена: %s", taskID, res.Status)
}

// Возврат одной задачи в очередь с учётом числа попыток. Общий путь и для
// зависших задач, и для отказавшей обработки.
func (s *supervisor) requeue(ctx context.Context, taskID, reason string) {
	out, err := requeueScript.Run(ctx, s.rdb,
		[]string{processingKey, queueKey, poisonKey, attemptsKey},
		taskID, s.maxTries).Result()
	if err != nil {
		log.Printf("возврат задачи %s: %v", taskID, err)
		return
	}
	verdict, _ := out.(string)
	switch {
	case verdict == "poison":
		log.Printf("задача %s превысила %d попыток — в poison-очередь", taskID, s.maxTries)
		payload, _ := json.Marshal(parseResult{
			TaskID: taskID, Status: "failed",
			Error: "превышено число попыток обработки: " + reason,
		})
		_ = s.rdb.Set(ctx, resultPrefix+taskID, string(payload), time.Hour).Err()
	case verdict != "absent":
		log.Printf("задача %s возвращена в очередь (%s)", taskID, verdict)
	}
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}
	jobsDir := os.Getenv("JOBS_DIR")
	if jobsDir == "" {
		jobsDir = "/data/jobs"
	}
	doneDir := os.Getenv("DONE_DIR")
	if doneDir == "" {
		doneDir = "/data/done"
	}
	for _, d := range []string{jobsDir, doneDir} {
		if err := os.MkdirAll(d, 0o770); err != nil {
			log.Fatalf("каталог %s: %v", d, err)
		}
	}

	s := &supervisor{
		rdb:      redis.NewClient(&redis.Options{Addr: redisAddr}),
		jobsDir:  jobsDir,
		doneDir:  doneDir,
		maxTries: 3,
	}

	log.Println("супервизор запущен: очередь здесь, разбор файлов — в парсере без сети")

	ctx := context.Background()
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			s.requeueStale(ctx)
		}
	}()

	for {
		// BLMove вместо BRPop: задача перекладывается в processing и остаётся
		// там до подтверждения. Падение между взятием и результатом больше не
		// означает потерю задачи.
		taskID, err := s.rdb.BLMove(ctx, queueKey, processingKey, "right", "left", 5*time.Second).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			log.Printf("очередь: %v", err)
			time.Sleep(time.Second)
			continue
		}
		s.handle(ctx, taskID)
	}
}
