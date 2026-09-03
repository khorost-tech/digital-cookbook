// Клиент стенда на Go.
//
// Контракт строки общий для всех четырёх клиентов: kind, case, client,
// outcome, detail.
//
// СБОЙ ПРОБЫ ОТДЕЛЁН ОТ НЕДОВЕРИЯ КЛИЕНТА. Если не удалось прочитать
// файл доверенного центра или достучаться до порта — это про нас, и
// такая строка не должна попадать в таблицу как «клиент не доверяет
// сертификату». Различаются они по типу ошибки, а не по тексту: текст
// у библиотек меняется между версиями, и опираться на него нельзя.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

type result struct {
	Kind    string `json:"kind"`
	Case    string `json:"case"`
	Client  string `json:"client"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
	// Отделяет «не договорились» от «договорились, а потом отказали».
	// В новых версиях протокола сервер проверяет клиентский сертификат
	// уже после того, как клиент счёл рукопожатие завершённым.
	HandshakeOK bool `json:"handshake_ok"`
}

func emit(r result) {
	raw, _ := json.Marshal(r)
	fmt.Println(string(raw))
}

func main() {
	kind := flag.String("kind", "chain", "вид пробы")
	caseName := flag.String("case", "", "имя случая")
	addr := flag.String("addr", "127.0.0.1:18443", "адрес сервера")
	serverName := flag.String("servername", "stand.local", "имя для SNI и проверки")
	caFile := flag.String("ca", "pki/root.pem", "доверенный центр")
	clientCert := flag.String("client-cert", "", "сертификат клиента для взаимного рукопожатия")
	clientKey := flag.String("client-key", "", "ключ клиента")
	flag.Parse()

	r := result{Kind: *kind, Case: *caseName, Client: "go"}

	// Доверенный центр передаётся явно: системное хранилище сделало бы
	// результат зависимым от машины, и фикстура перестала бы
	// воспроизводиться у читателя.
	pemBytes, err := os.ReadFile(*caFile)
	if err != nil {
		r.Outcome, r.Detail = "error", fmt.Sprintf("не прочитать %s: %v", *caFile, err)
		emit(r)
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		r.Outcome, r.Detail = "error", fmt.Sprintf("%s: не добавился в набор доверенных", *caFile)
		emit(r)
		return
	}

	cfg := &tls.Config{RootCAs: pool, ServerName: *serverName}

	if *clientCert != "" {
		pair, err := tls.LoadX509KeyPair(*clientCert, *clientKey)
		if err != nil {
			r.Outcome, r.Detail = "error", fmt.Sprintf("сертификат клиента: %v", err)
			emit(r)
			return
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", *addr, cfg)
	if err != nil {
		// Различаем по ТИПУ ошибки, а не по её тексту: текст библиотеки
		// меняется между версиями, и матрица, опирающаяся на него,
		// однажды тихо поедет.
		var invalid x509.CertificateInvalidError
		var unknown x509.UnknownAuthorityError
		var hostname x509.HostnameError
		var recordErr *tls.RecordHeaderError
		switch {
		case errors.As(err, &invalid), errors.As(err, &unknown),
			errors.As(err, &hostname), errors.As(err, &recordErr):
			r.Outcome, r.Detail = "rejected", err.Error()
		default:
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "dial" {
				// До сервера не дошли вовсе — это про нас.
				r.Outcome, r.Detail = "error", err.Error()
			} else {
				// Рукопожатие началось и было отвергнуто.
				r.Outcome, r.Detail = "rejected", err.Error()
			}
		}
		emit(r)
		return
	}

	defer conn.Close()

	r.HandshakeOK = true

	// Читаем ПОСЛЕ рукопожатия. Отказ по клиентскому сертификату
	// приходит отдельным сообщением, и проба, которая не пробует читать,
	// объявит успех там, где соединение уже мертво.
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: stand.local\r\n\r\n")); err != nil {
		r.Outcome, r.Detail = "rejected", "после рукопожатия: "+err.Error()
		emit(r)
		return
	}
	buf := make([]byte, 64)
	if _, err := conn.Read(buf); err != nil {
		r.Outcome, r.Detail = "rejected", "после рукопожатия: "+err.Error()
		emit(r)
		return
	}

	r.Outcome, r.Detail = "connected", ""
	emit(r)
}
