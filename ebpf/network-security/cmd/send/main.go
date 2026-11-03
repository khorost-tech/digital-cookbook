// Отправитель — эталон оси: шлёт ровно N UDP-пакетов с каждого адреса
// источника и печатает, сколько отправил.
//
//	send DST:PORT N         адреса источника — все адреса 10.99.0.x интерфейса в этом пространстве имён
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func sources() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip := a.(*net.IPNet).IP.To4()
		if ip != nil && ip[0] == 10 && ip[1] == 99 {
			out = append(out, ip.String())
		}
	}
	return out
}

func main() {
	dst, _ := net.ResolveUDPAddr("udp4", os.Args[1])
	n, _ := strconv.Atoi(os.Args[2])
	sent := map[string]int{}
	for _, src := range sources() {
		conn, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.ParseIP(src)}, dst)
		if err != nil {
			panic(err)
		}
		for i := 0; i < n; i++ {
			if _, err := conn.Write([]byte("probe")); err == nil {
				sent[src]++
			}
			if i%200 == 199 {
				time.Sleep(time.Millisecond) // не переполнить очередь приёмника — потери только от фильтра
			}
		}
		conn.Close()
	}
	b, _ := json.Marshal(sent)
	fmt.Printf("RESULT %s\n", b)
}
