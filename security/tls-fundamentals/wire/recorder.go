// Посредник первого пакета от клиента.
//
// Стоит между клиентом и сервером стенда и сохраняет ПЕРВЫЕ БАЙТЫ,
// которые клиент отправил, — приветствие клиента. Оно уходит до того,
// как появится хоть какой-то ключ, то есть открытым текстом.
//
// ПОЧЕМУ ПРОКСИ, А НЕ ПРОСТО ПРИЁМНИК. Если оборвать соединение сразу
// после записи, клиент получит сбой, и останется вопрос: а состоялось ли
// бы рукопожатие вообще. Сквозная передача снимает его — соединение
// доходит до настоящего сервера и работает.
//
// Байты сохраняются как есть, шестнадцатеричным дампом. Разбор — отдельно
// и на другом языке: имя, найденное разбором, не должно быть тем же
// именем, которое мы сами передали клиенту в аргументах.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)


// readRecord читает ОДНУ запись целиком: заголовок из пяти байт, в нём
// длина, затем ровно столько тела.
//
// Раньше здесь было одно чтение из сокета, и записанное зависело от
// того, как сеть порезала поток: приветствие curl вышло 1567 байт на
// одной платформе и 1460 на другой. Второе — типичный размер сегмента,
// то есть приветствие приехало разрезанным, а мы записали первый кусок
// и назвали его целым.
func readRecord(conn net.Conn) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := int(header[3])<<8 | int(header[4])
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

// hasServerHelloDone сообщает, что сервер старой версии закончил свою
// часть обмена: дальше слово за клиентом, и ждать больше нечего.
func hasServerHelloDone(record []byte) bool {
	if len(record) < 6 || record[0] != 0x16 {
		return false
	}
	body := record[5:]
	for i := 0; i+4 <= len(body); {
		length := int(body[i+1])<<16 | int(body[i+2])<<8 | int(body[i+3])
		if body[i] == 14 {
			return true
		}
		i += 4 + length
	}
	return false
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18800", "адрес посредника")
	upstream := flag.String("upstream", "127.0.0.1:18443", "настоящий сервер")
	out := flag.String("out", "fixtures/wire-hello.hex", "куда сохранить байты клиента")
	outServer := flag.String("out-server", "", "куда сохранить первый ответ сервера")
	limit := flag.Int("limit", 4096, "сколько байт первого пакета сохранить")
	flag.Parse()

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("не поднять посредник: %v", err)
	}
	defer listener.Close()

	fmt.Fprintf(os.Stderr, "READY %s -> %s\n", *listen, *upstream)

	conn, err := listener.Accept()
	if err != nil {
		log.Fatalf("не принять соединение: %v", err)
	}
	defer conn.Close()

	first, err := readRecord(conn)
	if err != nil {
		log.Fatalf("не прочитать приветствие клиента: %v", err)
	}
	n := len(first)

	if err := os.WriteFile(*out, []byte(hex.EncodeToString(first)), 0o644); err != nil {
		log.Fatalf("не сохранить байты: %v", err)
	}
	fmt.Fprintf(os.Stderr, "RECORDED %d байт в %s\n", n, *out)

	// Дальше — сквозная передача, чтобы соединение дошло до сервера.
	server, err := net.Dial("tcp", *upstream)
	if err != nil {
		log.Fatalf("не дозвониться до сервера: %v", err)
	}
	defer server.Close()

	if _, err := server.Write(first); err != nil {
		log.Fatalf("не передать первый пакет: %v", err)
	}

	// Первый ответ сервера сохраняется отдельно: в нём лежит случайное
	// поле, куда сервер кладёт метку о понижении версии. Метка — это
	// БАЙТЫ, а не рассуждение, и увидеть их можно только здесь.
	if *outServer != "" {
		// Записи читаются подряд, пока сервер не завершит свою часть
		// обмена или пока поток не замолчит. Срок ожидания сторожит
		// только конец: СОСТАВ прочитанного задаёт сервер, а не сеть.
		var reply []byte
		for len(reply) < *limit {
			server.SetReadDeadline(time.Now().Add(2 * time.Second))
			record, err := readRecord(server)
			if err != nil {
				break
			}
			reply = append(reply, record...)
			if hasServerHelloDone(record) {
				break
			}
		}
		server.SetReadDeadline(time.Time{})
		m := len(reply)
		if m == 0 {
			log.Fatalf("сервер не прислал ничего")
		}
		if err := os.WriteFile(*outServer,
			[]byte(hex.EncodeToString(reply)), 0o644); err != nil {
			log.Fatalf("не сохранить ответ сервера: %v", err)
		}
		fmt.Fprintf(os.Stderr, "RECORDED_SERVER %d байт в %s\n", m, *outServer)
		if _, err := conn.Write(reply); err != nil {
			log.Fatalf("не передать ответ клиенту: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(server, conn); server.Close() }()
	go func() { defer wg.Done(); io.Copy(conn, server); conn.Close() }()
	wg.Wait()
}
