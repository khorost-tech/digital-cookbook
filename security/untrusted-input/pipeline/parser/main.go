// Парсер: единственное место, где разбирается содержимое недоверенного файла.
//
// Контейнер парсера запускается с network_mode: none — у него нет сетевого
// стека вообще. Ни интернета, ни очереди, ни соседних сервисов. Задания
// приходят файлами в общий каталог, результаты уходят туда же.
//
// Это отличие от первой версии стенда принципиально. Тогда разбор шёл в
// контейнере, подключённом к внутренней сети ради Redis, и фраза «парсер лишён
// сети» была неточной: internal-сеть убирает выход наружу, но её участники
// продолжают видеть друг друга. Скомпрометированный парсер мог бы читать и
// править очередь. Теперь у него нет даже такой возможности.
//
// ОДНОРАЗОВОСТЬ. На каждое задание запускается отдельный процесс ffprobe, и
// сам парсер по флагу может завершаться после одной задачи (ONE_SHOT) — тогда
// состояние процесса не переносится между файлами. Это важно именно для
// повреждений памяти: испорченная куча уходит вместе с процессом.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const probeTimeout = 30 * time.Second

type parseResult struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Format   string `json:"format,omitempty"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

func probeFile(path string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// Отдельный процесс на каждый файл: повреждение памяти внутри ffprobe
	// не переживает его завершение.
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=format_name,duration",
		"-of", "default=noprint_wrappers=1:nokey=0",
		path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", err
	}

	var format, duration string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "format_name="):
			format = strings.TrimPrefix(line, "format_name=")
		case strings.HasPrefix(line, "duration="):
			duration = strings.TrimPrefix(line, "duration=")
		}
	}
	return format, duration, nil
}

func writeResult(doneDir, taskID string, res parseResult) error {
	payload, err := json.Marshal(res)
	if err != nil {
		return err
	}
	tmp := filepath.Join(doneDir, "."+taskID+".json")
	final := filepath.Join(doneDir, taskID+".json")
	if err := os.WriteFile(tmp, payload, 0o640); err != nil {
		return err
	}
	// Атомарная публикация: супервизор не прочитает половину файла.
	return os.Rename(tmp, final)
}

func processOne(inputDir, doneDir, jobsDir, taskID string) {
	res := parseResult{TaskID: taskID, Status: "ok"}
	format, duration, err := probeFile(filepath.Join(inputDir, taskID))
	if err != nil {
		// Битый или намеренно некорректный файл — штатная ситуация.
		// Важно, что она остаётся здесь и не выходит за пределы парсера.
		res.Status = "failed"
		res.Error = "разбор не удался: " + err.Error()
		log.Printf("задача %s: %v", taskID, err)
	} else {
		res.Format = format
		res.Duration = duration
		log.Printf("задача %s: формат=%s длительность=%s", taskID, format, duration)
	}

	if err := writeResult(doneDir, taskID, res); err != nil {
		log.Printf("запись результата %s: %v", taskID, err)
		return
	}
	_ = os.Remove(filepath.Join(jobsDir, taskID))
}

func main() {
	inputDir := envOr("INPUT_DIR", "/data/input")
	jobsDir := envOr("JOBS_DIR", "/data/jobs")
	doneDir := envOr("DONE_DIR", "/data/done")
	oneShot := os.Getenv("ONE_SHOT") != ""

	if os.Getenv("PROBE_MODE") != "" {
		runProbes()
		return
	}

	log.Println("парсер запущен: сети нет, задания приходят файлами")

	for {
		entries, err := os.ReadDir(jobsDir)
		if err != nil {
			log.Printf("каталог заданий: %v", err)
			time.Sleep(time.Second)
			continue
		}

		handled := false
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue // файл ещё пишется
			}
			processOne(inputDir, doneDir, jobsDir, name)
			handled = true
			if oneShot {
				return
			}
		}

		if !handled {
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
