// Тесты файлового канала между супервизором и парсером.
//
// Проверяется именно то, ради чего этот канал устроен сложнее, чем
// «прочитать JSON»: каталог общий с недоверенной зоной, поэтому ответ может
// быть не файлом, не одним объектом, не той длины и не на ту задачу.
//
// Тесты — не украшение: FIFO и подмену объекта между проверкой и чтением
// руками не воспроизвести, а именно они ломают наивную реализацию.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testTask = "aabbccddeeff00112233445566778899"

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatalf("подготовка файла: %v", err)
	}
	return p
}

func TestReadAnswerRegularFile(t *testing.T) {
	p := writeFile(t, t.TempDir(), "ok.json", `{"task_id":"x","status":"ok"}`)
	data, err := readAnswer(p)
	if err != nil {
		t.Fatalf("обычный файл должен читаться: %v", err)
	}
	if string(data) != `{"task_id":"x","status":"ok"}` {
		t.Fatalf("прочитано не то: %q", data)
	}
}

// Файл ровно на пределе принимается, на байт больше — отвергается.
// Это и есть смысл чтения maxResultBytes+1: без него граница неразличима.
func TestReadAnswerSizeBoundary(t *testing.T) {
	dir := t.TempDir()

	atLimit := writeFile(t, dir, "at.json", strings.Repeat("a", maxResultBytes))
	if _, err := readAnswer(atLimit); err != nil {
		t.Fatalf("файл ровно на пределе должен приниматься: %v", err)
	}

	overLimit := writeFile(t, dir, "over.json", strings.Repeat("a", maxResultBytes+1))
	if _, err := readAnswer(overLimit); err == nil {
		t.Fatal("файл больше предела должен отвергаться")
	}
}

func TestReadAnswerRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "secret", "содержимое супервизора")
	link := filepath.Join(dir, "answer.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("символические ссылки недоступны: %v", err)
	}

	if _, err := readAnswer(link); err == nil {
		t.Fatal("символическая ссылка не должна открываться")
	}
}

// Главный тест этого набора. FIFO не отсекается через O_NOFOLLOW, а open
// на нём без писателя блокируется навсегда — наивная реализация здесь
// зависает, и никакой таймаут снаружи не помогает: процесс стоит в syscall.
//
// Тест обязан ЗАВЕРШИТЬСЯ. Поэтому чтение уводится в горутину: зависание
// проявится как истечение срока, а не как повисший навсегда прогон тестов.
func TestReadAnswerRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "answer.json")
	if err := syscall.Mkfifo(fifo, 0o640); err != nil {
		t.Skipf("FIFO недоступен: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := readAnswer(fifo)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("именованный канал не должен приниматься как ответ")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("чтение заблокировалось на FIFO — O_NONBLOCK не сработал")
	}
}

func TestReadAnswerRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "answer.json")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatalf("подготовка каталога: %v", err)
	}
	if _, err := readAnswer(sub); err == nil {
		t.Fatal("каталог не должен приниматься как ответ")
	}
}

// Подмена объекта между предварительной проверкой по имени (Lstat в waitResult)
// и открытием в readAnswer.
//
// ЧЕСТНАЯ ОГОВОРКА О ГРАНИЦАХ ЭТОГО ТЕСТА. Он падает, если убрать O_NOFOLLOW,
// и это проверено. Но он НЕ различает Fstat открытого дескриптора и повторный
// Lstat по имени: подмену отсекает флаг открытия, до проверки типа дело не
// доходит. Замена f.Stat() на os.Lstat(path) оставляет тест зелёным.
//
// Окно между open и проверкой типа — доли микросекунды внутри одной функции;
// детерминированно воспроизвести его из теста нечем, а гонка «в цикле, авось
// поймается» дала бы плавающий тест, то есть худший из возможных исходов.
// Поэтому Fstat здесь обоснован построением, а не измерением, и выдавать этот
// тест за доказательство его необходимости нельзя.
func TestReadAnswerSurvivesSwapAfterLstat(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "answer.json", `{"task_id":"x","status":"ok"}`)

	// Проверка по имени — как это делает waitResult перед readAnswer.
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	// Подмена: обычный файл заменён символической ссылкой.
	if err := os.Remove(path); err != nil {
		t.Fatalf("подмена: %v", err)
	}
	secret := writeFile(t, dir, "secret", "содержимое супервизора")
	if err := os.Symlink(secret, path); err != nil {
		t.Skipf("символические ссылки недоступны: %v", err)
	}

	// Решение принимается по открытому дескриптору, а не по прошлой проверке.
	if _, err := readAnswer(path); err == nil {
		t.Fatal("подменённый объект не должен приниматься")
	}
}

func TestValidateAnswer(t *testing.T) {
	ok, _ := json.Marshal(parseResult{TaskID: testTask, Status: "ok", Format: "avi"})

	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"правильный ответ", string(ok), false},
		{"статус failed тоже допустим", `{"task_id":"` + testTask + `","status":"failed","error":"битый файл"}`, false},
		{"чужая задача", `{"task_id":"deadbeef","status":"ok"}`, true},
		{"неизвестный статус", `{"task_id":"` + testTask + `","status":"взломано"}`, true},
		{"неизвестное поле", `{"task_id":"` + testTask + `","status":"ok","exec":"rm -rf /"}`, true},
		{"два объекта", `{"task_id":"` + testTask + `","status":"ok"}{"task_id":"чужой","status":"ok"}`, true},
		{"объект и мусор после него", `{"task_id":"` + testTask + `","status":"ok"} лишнее`, true},
		{"пустой ответ", ``, true},
		{"не JSON", `не json вовсе`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateAnswer([]byte(c.payload), testTask)
			if c.wantErr && err == nil {
				t.Fatalf("ожидалась ошибка, ответ принят: %s", c.payload)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("ответ должен приниматься, получено: %v", err)
			}
		})
	}
}

// Значения таймингов берутся из окружения — на этом держится возможность
// укоротить их для стенда, не трогая умолчания для рабочего сервиса.
func TestDurationOr(t *testing.T) {
	const key = "TEST_DURATION_OR"

	if got := durationOr(key, 42*time.Second); got != 42*time.Second {
		t.Fatalf("без переменной ожидалось значение по умолчанию, получено %v", got)
	}

	t.Setenv(key, "7s")
	if got := durationOr(key, 42*time.Second); got != 7*time.Second {
		t.Fatalf("значение из окружения не подхвачено: %v", got)
	}

	// Мусор не должен ломать запуск: берётся значение по умолчанию.
	t.Setenv(key, "не длительность")
	if got := durationOr(key, 42*time.Second); got != 42*time.Second {
		t.Fatalf("при неразборчивом значении ожидалось умолчание, получено %v", got)
	}
}
