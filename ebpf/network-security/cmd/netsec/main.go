// Загрузчик оси XDP/tc/iptables. Работает в пространстве имён приёмника:
// слушает UDP, считает пакеты по адресу источника, ставит фильтр на интерфейс
// и запускает отправителя в пространстве имён отправителя.
//
//	netsec MODE IFACE N BLOCK,... SRC...
//	MODE: none | xdp-native | xdp-generic | tc | iptables | xdp-update
//	xdp-update — две фазы по N: список BLOCK пуст, затем адреса заносятся в карту
//	без перезагрузки программы.
package main

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel filter ../../bpf/filter.c -- -I/usr/include/aarch64-linux-gnu

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

func key(ip string) uint32 { return binary.LittleEndian.Uint32(net.ParseIP(ip).To4()) }

// rcvbufErrors — счётчик ядра UdpRcvbufErrors: UDP-пакеты, отброшенные из-за полной очереди сокета.
func rcvbufErrors() int {
	b, _ := os.ReadFile("/proc/net/snmp")
	lines := strings.Split(string(b), "\n")
	for i := 0; i+1 < len(lines); i++ {
		if strings.HasPrefix(lines[i], "Udp:") && strings.HasPrefix(lines[i+1], "Udp:") {
			h, v := strings.Fields(lines[i]), strings.Fields(lines[i+1])
			for j := range h {
				if h[j] == "RcvbufErrors" {
					n, _ := strconv.Atoi(v[j])
					return n
				}
			}
		}
	}
	return -1
}

func main() {
	mode, iface := os.Args[1], os.Args[2]
	n := os.Args[3]
	block := strings.Split(os.Args[4], ",")
	srcs := os.Args[5:]
	res := map[string]any{"mode": mode}
	defer func() {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}()
	fail := func(err error) bool {
		if err != nil {
			res["error"] = err.Error()
			return true
		}
		return false
	}
	if fail(rlimit.RemoveMemlock()) {
		return
	}

	pc, err := net.ListenPacket("udp4", ":9000")
	if fail(err) {
		return
	}
	// большой приёмный буфер: опыт мерит фильтр, а не очередь сокета; потери в очереди
	// всё равно считаются отдельно — UdpRcvbufErrors пространства имён приёмника
	pc.(*net.UDPConn).SetReadBuffer(16 << 20)
	var mu sync.Mutex
	recv := map[string]int{}
	go func() {
		buf := make([]byte, 64)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			recv[addr.(*net.UDPAddr).IP.String()]++
			mu.Unlock()
		}
	}()

	var objs filterObjects
	if fail(loadFilterObjects(&objs, nil)) {
		return
	}
	defer objs.Close()
	ifc, err := net.InterfaceByName(iface)
	if fail(err) {
		return
	}
	addBlock := func() {
		for _, ip := range block {
			if ip != "" {
				objs.Blocklist.Put(key(ip), uint8(1))
			}
		}
	}
	var l link.Link
	switch mode {
	case "xdp-native", "xdp-update":
		l, err = link.AttachXDP(link.XDPOptions{Program: objs.XdpFilter, Interface: ifc.Index, Flags: link.XDPDriverMode})
	case "xdp-generic":
		l, err = link.AttachXDP(link.XDPOptions{Program: objs.XdpFilter, Interface: ifc.Index, Flags: link.XDPGenericMode})
	case "tc":
		l, err = link.AttachTCX(link.TCXOptions{Program: objs.TcFilter, Interface: ifc.Index, Attach: ebpf.AttachTCXIngress})
	case "iptables":
		for _, ip := range block {
			if out, e := exec.Command("iptables", "-I", "INPUT", "-s", ip, "-j", "DROP").CombinedOutput(); e != nil {
				err = fmt.Errorf("iptables: %v %s", e, out)
			}
		}
	}
	if fail(err) {
		return
	}
	if l != nil {
		defer l.Close()
	}
	if mode != "xdp-update" {
		addBlock()
	}

	send := func() map[string]int {
		out, err := exec.Command("ip", "netns", "exec", "snd", "/opt/stand/bin/send", "10.99.0.100:9000", n).
			CombinedOutput()
		_ = err
		var sent map[string]int
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "RESULT ") {
				json.Unmarshal([]byte(strings.TrimPrefix(line, "RESULT ")), &sent)
			}
		}
		return sent
	}
	_ = srcs
	phases := []map[string]any{}
	runPhase := func(name string) {
		mu.Lock()
		recv = map[string]int{}
		mu.Unlock()
		// tcpdump на том же интерфейсе: что видит перехват пакетов при каждом фильтре
		dump := exec.Command("tcpdump", "-i", iface, "-nn", "-l", "-q", "udp", "port", "9000")
		dumpOut, _ := dump.StdoutPipe()
		dump.Start()
		seen := map[string]int{}
		var dw sync.WaitGroup
		dw.Add(1)
		go func() {
			defer dw.Done()
			buf, _ := io.ReadAll(dumpOut)
			for _, line := range strings.Split(string(buf), "\n") {
				f := strings.Fields(line)
				if len(f) > 2 && f[1] == "IP" {
					src := f[2][:strings.LastIndex(f[2], ".")]
					seen[src]++
				}
			}
		}()
		time.Sleep(time.Second) // tcpdump успевает открыть сокет
		bufErr0 := rcvbufErrors()
		sent := send()
		time.Sleep(time.Second)
		dump.Process.Signal(os.Interrupt)
		dw.Wait()
		dump.Wait()
		mu.Lock()
		got := map[string]int{}
		for k, v := range recv {
			got[k] = v
		}
		mu.Unlock()
		phases = append(phases, map[string]any{"phase": name, "sent": sent, "received": got, "tcpdump": seen,
			"socket_drops": rcvbufErrors() - bufErr0})
	}
	runPhase("main")
	if mode == "xdp-update" {
		addBlock() // запись в карту — программа уже работает, перезагрузки нет
		runPhase("after-update")
	}
	res["phases"] = phases

	var per []uint64
	sum := func(slot uint32) uint64 {
		var s uint64
		if objs.Counters.Lookup(slot, &per) == nil {
			for _, v := range per {
				s += v
			}
		}
		return s
	}
	res["prog_passed"], res["prog_dropped"] = sum(0), sum(1)
	if mode == "iptables" {
		out, _ := exec.Command("iptables", "-L", "INPUT", "-v", "-x", "-n").CombinedOutput()
		var dropped uint64
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) > 2 && f[2] == "DROP" {
				v, _ := strconv.ParseUint(f[0], 10, 64)
				dropped += v
			}
		}
		res["iptables_dropped"] = dropped
		for _, ip := range block {
			exec.Command("iptables", "-D", "INPUT", "-s", ip, "-j", "DROP").Run()
		}
	}
}
