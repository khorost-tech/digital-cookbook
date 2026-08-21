// API приёма файлов.
//
// Главное свойство этого сервиса — он НЕ ОТКРЫВАЕТ содержимое загруженного
// файла. Принял поток байт, сохранил, положил идентификатор в очередь. Никакого
// парсинга, определения формата, генерации превью — всё это делает отдельный
// воркер в изоляции.
//
// Смысл разделения: у API есть доступ к сети, к очереди и (в реальном сервисе)
// к базе и секретам. Именно поэтому он не должен быть тем местом, где
// выполняется код парсера недоверенного файла.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	maxUploadBytes = 64 << 20 // 64 МиБ — верхняя граница приёма
	queueKey       = "pipeline:tasks"
)

type server struct {
	rdb      *redis.Client
	inputDir string
}

func newTaskID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "только POST", http.StatusMethodNotAllowed)
		return
	}

	id, err := newTaskID()
	if err != nil {
		http.Error(w, "не удалось создать идентификатор", http.StatusInternalServerError)
		return
	}

	// Пишем во временный файл: он станет видимым парсеру только после
	// успешного завершения приёма. Иначе частично записанный файл мог бы
	// попасть в обработку или остаться мусором после ошибки.
	tmp := filepath.Join(s.inputDir, "."+id)
	dst := filepath.Join(s.inputDir, id)

	f, err := os.Create(tmp) //nolint:gosec // путь собран из hex-идентификатора
	if err != nil {
		log.Printf("создание файла: %v", err)
		http.Error(w, "не удалось сохранить", http.StatusInternalServerError)
		return
	}

	// Прибираем за собой при любом отказе ниже.
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	// Читаем на один байт больше лимита: только так можно отличить файл ровно
	// по границе от превышающего её. Простой LimitReader молча обрезал бы
	// загрузку и вернул успех — клиент считал бы, что отдал файл целиком.
	written, err := io.Copy(f, io.LimitReader(r.Body, maxUploadBytes+1))
	if err != nil {
		cleanup()
		log.Printf("запись файла: %v", err)
		http.Error(w, "ошибка приёма", http.StatusInternalServerError)
		return
	}
	if written > maxUploadBytes {
		cleanup()
		log.Printf("отклонён файл больше %d байт", maxUploadBytes)
		http.Error(w, "файл превышает допустимый размер", http.StatusRequestEntityTooLarge)
		return
	}

	// Закрываем ДО постановки в очередь: парсер не должен увидеть файл,
	// данные которого ещё не сброшены на диск.
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		log.Printf("закрытие файла: %v", err)
		http.Error(w, "ошибка приёма", http.StatusInternalServerError)
		return
	}

	// Атомарная публикация: файл появляется под финальным именем целиком.
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		log.Printf("публикация файла: %v", err)
		http.Error(w, "ошибка приёма", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.rdb.LPush(ctx, queueKey, id).Err(); err != nil {
		// Очередь недоступна — принятый файл никому не нужен, убираем.
		_ = os.Remove(dst)
		log.Printf("постановка в очередь: %v", err)
		http.Error(w, "очередь недоступна", http.StatusServiceUnavailable)
		return
	}

	log.Printf("принят файл %s (%d байт), задача поставлена", id, written)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"task_id":%q,"bytes":%d}`+"\n", id, written)
}

// Результат обработки: его пишет воркер, API только отдаёт.
func (s *server) handleResult(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "нужен параметр id", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	val, err := s.rdb.Get(ctx, "pipeline:result:"+id).Result()
	if errors.Is(err, redis.Nil) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, `{"status":"pending"}`)
		return
	}
	if err != nil {
		http.Error(w, "очередь недоступна", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, val)
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}
	inputDir := os.Getenv("INPUT_DIR")
	if inputDir == "" {
		inputDir = "/data/input"
	}
	if err := os.MkdirAll(inputDir, 0o750); err != nil {
		log.Fatalf("каталог приёма: %v", err)
	}

	s := &server{
		rdb:      redis.NewClient(&redis.Options{Addr: redisAddr}),
		inputDir: inputDir,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/result", s.handleResult)
	mux.HandleFunc("/health", s.handleHealth)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Println("API слушает :8080; содержимое файлов не открывается")
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
