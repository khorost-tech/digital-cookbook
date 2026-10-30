// Package danger — настоящие дефекты, которые широкие исключения прячут вместе
// с шумом: те же правила, но вход приходит из запроса, а Close — после записи.
package danger

import (
	"io"
	"net/http"
	"os"
)

// Proxy ходит по URL из запроса: запрос к внутренним адресам от имени сервиса (SSRF).
func Proxy(w http.ResponseWriter, r *http.Request) {
	resp, err := http.Get(r.URL.Query().Get("url")) // want gosec noctx
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() // noise errcheck
	_, _ = io.Copy(w, resp.Body)
}

// Download отдаёт файл по пути из запроса: ../ выводит за пределы каталога.
func Download(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(r.URL.Query().Get("path")) // want gosec
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, _ = w.Write(b) // want gosec
}

// Save пишет данные и не проверяет Close: при записи ошибка сброса на диск
// приходит именно из Close — данные потеряны, а вызывающий получил nil.
func Save(w io.WriteCloser, data []byte) error {
	defer w.Close() // want errcheck
	_, err := w.Write(data)
	return err
}
