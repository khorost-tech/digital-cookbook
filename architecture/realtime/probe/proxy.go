package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// countProxy — TCP-прокси, который считает байты в обе стороны и соединения
// (ось 1) и по команде рвёт все свои соединения и не пускает новые (ось 4:
// разрыв подписки инстанса на шину, пока остальные с шиной связаны).
type countProxy struct {
	target          string
	up, down, nconn atomic.Int64
	cut             atomic.Bool
	mu              sync.Mutex
	open            map[net.Conn]struct{}
}

func runProxy(listen, target, control string) {
	p := &countProxy{target: target, open: map[net.Conn]struct{}{}}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"bytes_up": p.up.Load(), "bytes_down": p.down.Load(),
				"conns": p.nconn.Load(), "cut": p.cut.Load()})
		})
		mux.HandleFunc("/reset", func(w http.ResponseWriter, _ *http.Request) {
			p.up.Store(0)
			p.down.Store(0)
			p.nconn.Store(0)
			w.WriteHeader(204)
		})
		mux.HandleFunc("/cut", func(w http.ResponseWriter, _ *http.Request) {
			p.cut.Store(true)
			p.mu.Lock()
			for c := range p.open {
				c.Close()
			}
			p.mu.Unlock()
			w.WriteHeader(204)
		})
		mux.HandleFunc("/restore", func(w http.ResponseWriter, _ *http.Request) {
			p.cut.Store(false)
			w.WriteHeader(204)
		})
		log.Fatal(http.ListenAndServe(control, mux))
	}()
	log.Printf("прокси %s → %s, управление %s", listen, target, control)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		if p.cut.Load() {
			c.Close()
			continue
		}
		go p.serve(c)
	}
}

func (p *countProxy) track(c net.Conn, on bool) {
	p.mu.Lock()
	if on {
		p.open[c] = struct{}{}
	} else {
		delete(p.open, c)
	}
	p.mu.Unlock()
}

func (p *countProxy) serve(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		client.Close()
		return
	}
	p.nconn.Add(1)
	p.track(client, true)
	p.track(server, true)
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn, n *atomic.Int64) {
		buf := make([]byte, 32<<10)
		for {
			k, err := src.Read(buf)
			if k > 0 {
				n.Add(int64(k))
				if _, werr := dst.Write(buf[:k]); werr != nil {
					break
				}
			}
			if err != nil {
				if err != io.EOF {
					break
				}
				if tc, ok := dst.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
				break
			}
		}
		done <- struct{}{}
	}
	go pipe(server, client, &p.up)
	go pipe(client, server, &p.down)
	<-done
	<-done
	client.Close()
	server.Close()
	p.track(client, false)
	p.track(server, false)
}
