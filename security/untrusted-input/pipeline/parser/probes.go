// Пробы границ изоляции парсера.
//
// Безобидные проверки: парсер спрашивает у окружения «а это мне можно?» и
// записывает ответ. Здесь нет ни уязвимости, ни попытки её использовать.
//
// Каждая проба осмысленна только в паре: запуск без ограничения и с ним.
// Причём границы проверяются ПООДИНОЧКЕ — иначе отказ chmod нельзя приписать
// именно seccomp, ведь одновременно включены и cap_drop, и read_only, и
// непривилегированный пользователь.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type probeResult struct {
	Name    string `json:"probe"`
	Allowed bool   `json:"allowed"`
	Detail  string `json:"detail"`
}

// Исходящее соединение наружу.
func probeNetworkExternal() probeResult {
	conn, err := net.DialTimeout("tcp", "1.1.1.1:53", 3*time.Second)
	if err != nil {
		return probeResult{"network_external", false, err.Error()}
	}
	_ = conn.Close()
	return probeResult{"network_external", true, "соединение установлено"}
}

// Доступ к соседям по конвейеру: Redis и API. Именно это остаётся возможным
// при internal-сети и именно это отрезает network_mode: none.
func probeNetworkInternal() probeResult {
	targets := map[string]string{
		"redis": "redis:6379",
		"api":   "api:8080",
	}
	reached := []string{}
	for name, addr := range targets {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			reached = append(reached, name)
		}
	}
	if len(reached) == 0 {
		return probeResult{"network_internal", false, "соседи по конвейеру недоступны"}
	}
	return probeResult{"network_internal", true, fmt.Sprintf("доступны: %v", reached)}
}

func probeWriteOutside() probeResult {
	path := "/probe-write-test"
	f, err := os.Create(path) //nolint:gosec // проба границы
	if err != nil {
		return probeResult{"write_outside", false, err.Error()}
	}
	_ = f.Close()
	_ = os.Remove(path)
	return probeResult{"write_outside", true, "файл создан в корне"}
}

// Запись во ВХОДНОЙ каталог: он смонтирован только на чтение, парсер не должен
// иметь возможности подменить разбираемый файл.
func probeWriteInput() probeResult {
	dir := envOr("INPUT_DIR", "/data/input")
	path := filepath.Join(dir, "probe-write-input")
	f, err := os.Create(path) //nolint:gosec // проба границы
	if err != nil {
		return probeResult{"write_input_volume", false, err.Error()}
	}
	_ = f.Close()
	_ = os.Remove(path)
	return probeResult{"write_input_volume", true, "запись во входной том прошла"}
}

func probeChmod() probeResult {
	dir := envOr("DONE_DIR", "/tmp")
	path := filepath.Join(dir, "probe-chmod-test")
	f, err := os.Create(path) //nolint:gosec // проба границы
	if err != nil {
		return probeResult{"chmod", false, "не удалось создать файл: " + err.Error()}
	}
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	if err := os.Chmod(path, 0o700); err != nil {
		return probeResult{"chmod", false, err.Error()}
	}
	return probeResult{"chmod", true, "права изменены"}
}

// Capabilities: пробуем операцию, требующую CAP_CHOWN.
//
// Владелец меняется на ЧУЖОЙ uid, и это не придирка. Первая версия пробы делала
// chown(path, 0, 0) — под root это не изменение владельца, а пустая операция,
// и ядро разрешает её без CAP_CHOWN. Проба проходила по неверной причине:
// с одним лишь cap_drop ALL она показывала «разрешено», хотя capability была
// снята. Поймала это раздельная проверка границ — в полном наборе, где процесс
// работает под 65534, тот же вызов становился настоящим изменением и отказывал.
func probeCapabilities() probeResult {
	const otherUID, otherGID = 1, 1 // daemon: заведомо не тот, под кем идёт проба

	dir := envOr("DONE_DIR", "/tmp")
	path := filepath.Join(dir, "probe-chown-test")
	f, err := os.Create(path) //nolint:gosec // проба границы
	if err != nil {
		return probeResult{"capabilities_chown", false, "не удалось создать файл: " + err.Error()}
	}
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	if err := os.Chown(path, otherUID, otherGID); err != nil {
		return probeResult{"capabilities_chown", false, err.Error()}
	}
	return probeResult{"capabilities_chown", true,
		fmt.Sprintf("владелец изменён на %d:%d", otherUID, otherGID)}
}

// Повышение привилегий через setuid-бинарник (перекрывается no-new-privileges).
func probeNoNewPrivs() probeResult {
	// /proc/self/status показывает состояние флага напрямую.
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return probeResult{"no_new_privs", true, "не удалось прочитать статус: " + err.Error()}
	}
	for _, line := range splitLines(string(data)) {
		if len(line) > 12 && line[:12] == "NoNewPrivs:\t" {
			if line[12:] == "1" {
				return probeResult{"no_new_privs", false, "NoNewPrivs=1 (повышение запрещено)"}
			}
			return probeResult{"no_new_privs", true, "NoNewPrivs=0 (повышение возможно)"}
		}
	}
	return probeResult{"no_new_privs", true, "флаг не найден"}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Одновременно живущие процессы: упирается в pids_limit. Важна именно
// одновременность — мгновенно завершающиеся процессы не накапливаются.
func probeProcessSpawn() probeResult {
	const attempts = 200
	var started []*exec.Cmd
	var lastErr error
	defer func() {
		for _, c := range started {
			if c.Process != nil {
				_ = c.Process.Kill()
				_ = c.Wait()
			}
		}
	}()

	for i := 0; i < attempts; i++ {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			lastErr = err
			break
		}
		started = append(started, cmd)
	}
	if lastErr != nil {
		return probeResult{"process_spawn", false,
			fmt.Sprintf("остановлено на %d одновременных процессах: %v", len(started), lastErr)}
	}
	return probeResult{"process_spawn", true,
		fmt.Sprintf("удержано %d одновременных процессов", len(started))}
}

// Выделение памяти сверх лимита. Ожидаемый исход при mem_limit — OOM-kill,
// то есть процесс не вернётся; отсутствие вывода по этой пробе и есть признак
// сработавшего лимита, что проверяется снаружи по коду завершения.
func probeMemory() probeResult {
	const targetMiB = 512
	chunks := make([][]byte, 0, targetMiB)
	for i := 0; i < targetMiB; i++ {
		chunk := make([]byte, 1<<20)
		for j := 0; j < len(chunk); j += 4096 {
			chunk[j] = 1 // касаемся страниц, иначе память не занимается реально
		}
		chunks = append(chunks, chunk)
	}
	return probeResult{"memory_over_limit", true,
		fmt.Sprintf("выделено %d МиБ без отказа", targetMiB)}
}

// Штатная работа при включённых границах. Обязательная проба: защита, ломающая
// работу, не защищает — её снимут при первом инциденте вместе со всем набором.
func probeNormalWork() probeResult {
	dir := envOr("INPUT_DIR", "/data/input")
	path := filepath.Join(dir, "sample.avi")
	if _, err := os.Stat(path); err != nil {
		return probeResult{"normal_work", false, "нет образца: " + err.Error()}
	}
	format, _, err := probeFile(path)
	if err != nil {
		return probeResult{"normal_work", false, err.Error()}
	}
	return probeResult{"normal_work", true, "ffprobe отработал: " + format}
}

func runProbes() {
	only := os.Getenv("PROBE_ONLY")

	all := map[string]func() probeResult{
		"normal_work": probeNormalWork,
		"network_external":   probeNetworkExternal,
		"network_internal":   probeNetworkInternal,
		"write_outside":      probeWriteOutside,
		"write_input_volume": probeWriteInput,
		"chmod":              probeChmod,
		"capabilities_chown": probeCapabilities,
		"no_new_privs":       probeNoNewPrivs,
		"process_spawn":      probeProcessSpawn,
		"memory_over_limit":  probeMemory,
	}
	order := []string{
		"normal_work",
		"network_external", "network_internal", "write_outside", "write_input_volume",
		"chmod", "capabilities_chown", "no_new_privs", "process_spawn", "memory_over_limit",
	}

	enc := json.NewEncoder(os.Stdout)
	for _, name := range order {
		// PROBE_ONLY позволяет запускать пробы поодиночке: только так отказ можно
		// приписать конкретной границе, а не всему набору сразу.
		if only != "" && only != name {
			continue
		}
		// Память по умолчанию не трогаем: её проба заканчивается OOM-kill.
		if name == "memory_over_limit" && only == "" {
			continue
		}
		res := all[name]()
		_ = enc.Encode(res)
	}
}
