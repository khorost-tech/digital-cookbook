// probe — крошечный сервис для демонстрации различий rootful/rootless Docker.
// При старте пишет файл в смонтированный volume (/data) и отдаёт по /info
// свой uid/gid ВНУТРИ контейнера. Главное наблюдение — снаружи, на хосте:
// в rootful файл в volume принадлежит root, в rootless — вашему пользователю.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"
)

const dataFile = "/data/written-by-container.txt"

type info struct {
	UIDInside     int    `json:"uid_inside"`
	GIDInside     int    `json:"gid_inside"`
	Hostname      string `json:"hostname"`
	DataFile      string `json:"data_file"`
	FileUIDInside uint32 `json:"file_uid_inside"`
	Note          string `json:"note"`
}

// writeProbe пишет файл в volume — его владельца на хосте и нужно смотреть.
func writeProbe() error {
	content := fmt.Sprintf(
		"Записано контейнером %s, uid внутри контейнера = %d\n",
		time.Now().UTC().Format(time.RFC3339), os.Getuid(),
	)
	return os.WriteFile(dataFile, []byte(content), 0o644)
}

func handler(w http.ResponseWriter, _ *http.Request) {
	host, _ := os.Hostname()
	resp := info{
		UIDInside: os.Getuid(),
		GIDInside: os.Getgid(),
		Hostname:  host,
		DataFile:  dataFile,
		Note:      "Сравните uid_inside с владельцем файла на хосте: ls -ln data/",
	}
	if fi, err := os.Stat(dataFile); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			resp.FileUIDInside = st.Uid
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}

func main() {
	if err := writeProbe(); err != nil {
		fmt.Fprintf(os.Stderr, "не удалось записать %s: %v\n", dataFile, err)
	}
	http.HandleFunc("/info", handler)
	const addr = ":8080"
	fmt.Printf("probe слушает %s, uid внутри контейнера = %d\n", addr, os.Getuid())
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
