// Сервер стенда: поднимается с сертификатом одного из случаев.
//
// ПОЧЕМУ СЕРВЕР ОДИН И ОБЩИЙ ДЛЯ ВСЕХ КЛИЕНТОВ.
// Стенд меряет, чем различаются КЛИЕНТЫ на одном и том же сломе. Если бы
// у каждого клиента был свой сервер, расхождение исходов нельзя было бы
// отделить от расхождения серверов. Один сервер — общая точка отсчёта.
//
// ПОЧЕМУ ЦЕПОЧКА СОБИРАЕТСЯ ВРУЧНУЮ, А НЕ ЧЕРЕЗ ГОТОВЫЙ ЗАГРУЗЧИК.
// Часть случаев ломает не сертификат, а то, ЧТО И В КАКОМ ПОРЯДКЕ сервер
// присылает: пропущенный промежуточный, обратный порядок. Готовый
// загрузчик такие случаи молча исправил бы, и половина матрицы
// перестала бы существовать.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

type standCase struct {
	Case       string   `json:"case"`
	Breaks     string   `json:"breaks"`
	ServerCert string   `json:"server_cert"`
	ServerKey  string   `json:"server_key"`
	Chain      []string `json:"chain"`
}

func loadCases(pkiDir string) ([]standCase, error) {
	raw, err := os.ReadFile(filepath.Join(pkiDir, "cases.json"))
	if err != nil {
		return nil, err
	}
	var cases []standCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

// certificateFor собирает цепочку ровно в том порядке, в каком её
// объявил случай: сначала server_cert, затем chain. Порядок — часть
// проверяемого поведения, поэтому он не нормализуется.
func certificateFor(pkiDir string, c standCase) (tls.Certificate, error) {
	var chain [][]byte

	appendPEM := func(name string) error {
		raw, err := os.ReadFile(filepath.Join(pkiDir, name))
		if err != nil {
			return err
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			return fmt.Errorf("%s: не разобрался как PEM", name)
		}
		chain = append(chain, block.Bytes)
		return nil
	}

	if err := appendPEM(c.ServerCert); err != nil {
		return tls.Certificate{}, err
	}
	for _, name := range c.Chain {
		if err := appendPEM(name); err != nil {
			return tls.Certificate{}, err
		}
	}

	keyRaw, err := os.ReadFile(filepath.Join(pkiDir, c.ServerKey))
	if err != nil {
		return tls.Certificate{}, err
	}
	keyBlock, _ := pem.Decode(keyRaw)
	if keyBlock == nil {
		return tls.Certificate{}, fmt.Errorf("%s: ключ не разобрался", c.ServerKey)
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		parsed, err2 := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err2 != nil {
			return tls.Certificate{}, fmt.Errorf("%s: %v", c.ServerKey, err)
		}
		return tls.Certificate{Certificate: chain, PrivateKey: parsed}, nil
	}
	return tls.Certificate{Certificate: chain, PrivateKey: key}, nil
}

func versionFromFlag(name string) (uint16, error) {
	switch name {
	case "", "any":
		return 0, nil
	case "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	}
	return 0, fmt.Errorf("неизвестная версия %q: ожидаются 1.2, 1.3 или any", name)
}

func main() {
	caseName := flag.String("case", "", "имя случая из pki/cases.json")
	addr := flag.String("addr", "127.0.0.1:18443", "адрес прослушивания")
	pkiDir := flag.String("pki", "pki", "каталог с сертификатами")
	minVer := flag.String("min-version", "any", "нижняя граница версии: 1.2, 1.3, any")
	maxVer := flag.String("max-version", "any", "верхняя граница версии: 1.2, 1.3, any")
	requireClient := flag.Bool("require-client-cert", false,
		"требовать сертификат клиента (взаимное рукопожатие)")
	clientCA := flag.String("client-ca", "root.pem",
		"чьи клиентские сертификаты принимаем")
	flag.Parse()

	if *caseName == "" {
		log.Fatal("не задан --case")
	}

	cases, err := loadCases(*pkiDir)
	if err != nil {
		log.Fatalf("не прочитать случаи: %v", err)
	}
	var chosen *standCase
	for i := range cases {
		if cases[i].Case == *caseName {
			chosen = &cases[i]
			break
		}
	}
	if chosen == nil {
		log.Fatalf("случай %q не найден в cases.json", *caseName)
	}

	cert, err := certificateFor(*pkiDir, *chosen)
	if err != nil {
		log.Fatalf("случай %q: %v", *caseName, err)
	}

	minV, err := versionFromFlag(*minVer)
	if err != nil {
		log.Fatal(err)
	}
	maxV, err := versionFromFlag(*maxVer)
	if err != nil {
		log.Fatal(err)
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   minV,
		MaxVersion:   maxV,
	}

	if *requireClient {
		pool := x509.NewCertPool()
		raw, err := os.ReadFile(filepath.Join(*pkiDir, *clientCA))
		if err != nil {
			log.Fatalf("не прочитать %s: %v", *clientCA, err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			log.Fatalf("%s: не добавился в набор доверенных", *clientCA)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	listener, err := tls.Listen("tcp", *addr, cfg)
	if err != nil {
		log.Fatalf("не поднять слушателя: %v", err)
	}
	defer listener.Close()

	// Строка готовности печатается в поток ошибок: сценарий ждёт её,
	// чтобы не гадать, поднялся сервер или нет. Ожидание по таймеру
	// давало бы плавающие прогоны.
	fmt.Fprintf(os.Stderr, "READY %s case=%s\n", *addr, *caseName)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			tlsConn, ok := c.(*tls.Conn)
			if !ok {
				return
			}
			// Причина отказа видна только здесь: клиенту достаётся
			// общее сообщение о сбое рукопожатия. Это и есть предмет
			// отдельной оси стенда, поэтому пишем её явно.
			if err := tlsConn.Handshake(); err != nil {
				fmt.Fprintf(os.Stderr, "SERVER_SAW %v\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "SERVER_SAW ok\n")
			// Отвечаем минимальным HTTP: иначе HTTP-клиенты
			// спотыкаются уже ПОСЛЕ успешного рукопожатия, и их
			// жалоба выглядит как отказ TLS, хотя TLS отработал.
			c.Write([]byte("HTTP/1.1 200 OK\r\n" +
				"Content-Type: text/plain\r\n" +
				"Content-Length: 3\r\n" +
				"Connection: close\r\n\r\nok\n"))
			// Соединение закрывается только после того, как клиент
			// дочитал.
			//
			// Сперва сервер просто закрывал связь сразу после ответа, и
			// клиент иногда упирался в обрыв вместо конца данных:
			// контроль у curl слетал через раз. Одно лишь завершающее
			// сообщение проблему не сняло — связь всё равно рвалась,
			// пока ответ был в пути, и «неожиданный конец при чтении»
			// стал получать уже openssl.
			//
			// Плавающий контроль хуже упавшего: он выглядит
			// случайностью и подталкивает перезапустить прогон вместо
			// того, чтобы чинить.
			tlsConn.CloseWrite()
			// Ждём, пока клиент закроет свою сторону. Срок ограничен:
			// повисший клиент иначе задержал бы весь прогон.
			tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			io.Copy(io.Discard, tlsConn)
		}(conn)
	}
}
