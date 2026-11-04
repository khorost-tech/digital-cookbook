// Тот же сервис стенда, что и service/main.cpp, на Go.
//
// ПОЧЕМУ ЭТО ВАЖНО ДОСЛОВНО. Ось сравнивает размеры трёх бинарников, и
// сравнение имеет смысл ровно до тех пор, пока это три реализации ОДНОГО
// сервиса. Поэтому здесь воспроизводится не «похожее поведение», а
// побайтно тот же ответ: те же заголовки, в том же порядке, то же тело,
// тот же JSON с теми же ключами в том же порядке. Проверка тождества на
// оси сравнивает полный ответ HTTP посимвольно.
//
// ПОЧЕМУ kind РАВЕН "package", А НЕ "sibling". Ключ kind принадлежит
// контракту ПРОБЫ сервиса, а не строке фикстуры этой оси: строку с
// kind="sibling" пишет измеритель, а сервис отвечает то же, что отвечает
// сервис на C++. Поменяй здесь kind — и ответы перестанут совпадать
// побайтно, то есть развалится главный контроль оси.
//
// ПОЧЕМУ БЕЗ net/http. По той же причине, по какой у C++ нет
// HTTP-фреймворка: любая надстройка меняла бы сразу и размер, и способ
// сборки. Здесь голый TCP и вручную собранный ответ — как и там.
//
// ПОЧЕМУ БЕЗ ЕДИНОЙ ВНЕШНЕЙ ЗАВИСИМОСТИ. Это условие оси. Доказывается
// оно не тем, что go.mod пуст (его написал я), а тем, что сборка идёт
// при GOPROXY=off: любая зависимость сверх стандартной библиотеки в
// таких условиях уронила бы её.
//
// ЧЕМ ЭТОТ СОСЕД ОТЛИЧАЕТСЯ ПО УСТРОЙСТВУ, А НЕ ПО ОТВЕТУ. Ответы трёх
// плеч совпадают побайтно, но РАБОТАЮТ они не одинаково: net.LookupHost
// при CGO_ENABLED=0 разрешает имена собственным резолвером Go,
// вкомпилированным в бинарник, — он сам читает /etc/resolv.conf и сам
// говорит с сервером имён по DNS. Позвать getaddrinfo он не может:
// без cgo бинарник не умеет звать C вовсе. Плечи C++ и Rust, наоборот,
// зовут getaddrinfo той самой musl, с которой слинкованы, и main.rs
// объявляет этот паритет прямо.
//
// Тождество сервиса на этой оси доказано СОВПАДЕНИЕМ ОТВЕТОВ, а не
// совпадением работы, и разница механизма — того же рода, что признанная
// примесь fmt: приписывать разницу байтов «языку» нельзя ещё и потому,
// что часть веса бинарника Go занимает резолвер, которого у соседей
// внутри нет. Разница названа полем name_resolution в
// images/sibling-variants.json, печатается разбором и держится уликой
// CGO_ENABLED=[0], прочитанной из образа, а не этим комментарием.
package main

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// healthzBody подменяется линковщиком (-X main.healthzBody=...) и
// существует ради ОТРИЦАТЕЛЬНОГО КОНТРОЛЯ оси: плечо go-divergent
// собирается из этого же исходника тем же Dockerfile'ом и отличается
// ровно значением этой строки. Оно ОБЯЗАНО провалить проверку
// тождества — иначе проверка неотличима от той, что всегда отвечает
// «совпало».
var healthzBody = "ok"

// probeName подменяется линковщиком (-X main.probeName=...) и существует
// ради ВТОРОГО отрицательного контроля оси: плечо go-resolve-divergent
// собирается из этого же исходника тем же Dockerfile'ом и отличается
// ровно значением этой строки. Оно обязано разойтись с плечом go по
// сравниваемому телу /resolve — при совпадающем ответе /healthz.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ РУЧКА. Контроль по healthzBody роняет только одну из
// двух веток проверки тождества. Вторая — сравнение тела /resolve —
// оставалась уронена лишь мутацией копии фикстуры, то есть проверялась
// без живого замера. Значение по умолчанию совпадает с тем, что
// печатают плечи C++ и Rust, поэтому измеряемое плечо go остаётся
// побайтно тождественным им.
var probeName = "resolve"

func jsonEscape(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch ch {
		case '"':
			out.WriteString("\\\"")
		case '\\':
			out.WriteString("\\\\")
		case '\n':
			out.WriteString("\\n")
		case '\r':
			out.WriteString("\\r")
		case '\t':
			out.WriteString("\\t")
		default:
			if ch < 0x20 {
				fmt.Fprintf(&out, "\\u%04x", ch)
			} else {
				out.WriteByte(ch)
			}
		}
	}
	return out.String()
}

func jsonLine(probe, outcome, detail string) string {
	variant := os.Getenv("STAND_VARIANT")
	if variant == "" {
		variant = "unknown"
	}
	return fmt.Sprintf(
		`{"kind":"package","variant":"%s","probe":"%s","outcome":"%s","detail":"%s"}`,
		jsonEscape(variant), probe, outcome, jsonEscape(detail))
}

// resolve повторяет смысл resolve() из service/main.cpp: имя либо
// разрешилось, либо нет. Текст ошибки у Go свой — сравнивать между
// языками его нельзя, и разбор сравнивает исход, а не текст: поле detail
// исключено из сравниваемого тела наравне с variant.
//
// МЕХАНИЗМ ЗДЕСЬ НЕ ТОТ, ЧТО У СОСЕДЕЙ. net.LookupHost при
// CGO_ENABLED=0 работает встроенным резолвером Go, а не getaddrinfo
// libc, — см. верхний комментарий файла.
func resolve(host string) string {
	if _, err := net.LookupHost(host); err != nil {
		return jsonLine(probeName, "refused", err.Error())
	}
	return jsonLine(probeName, "resolved", "")
}

func queryValue(target, key string) string {
	pos := strings.Index(target, key+"=")
	if pos < 0 {
		return ""
	}
	start := pos + len(key) + 1
	rest := target[start:]
	end := strings.IndexAny(rest, "& ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func main() {
	port := "8080"
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--port=") {
			port = strings.TrimPrefix(arg, "--port=")
		}
	}

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не занять порт %s\n", port)
		os.Exit(2)
	}
	// Строка готовности — та же, что у C++: прогон ждёт её, а не спит по
	// таймеру.
	fmt.Fprintf(os.Stderr, "READY %s\n", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		buf := make([]byte, 2048)
		n, _ := conn.Read(buf)
		body := healthzBody
		if n > 0 {
			request := string(buf[:n])
			if strings.HasPrefix(request, "GET /resolve") {
				host := queryValue(request, "host")
				if host == "" {
					host = "example.com"
				}
				body = resolve(host)
			}
		}
		response := fmt.Sprintf(
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n"+
				"Content-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(body), body)
		conn.Write([]byte(response))
		conn.Close()
	}
}
