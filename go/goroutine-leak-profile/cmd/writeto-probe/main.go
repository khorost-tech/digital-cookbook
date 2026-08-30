// cmd/writeto-probe — наблюдательная проба поведения профиля goroutineleak
// на нескольких снимках WriteTo() подряд. Она НИЧЕГО не проверяет и ничем
// не падает (кроме отсутствия самого профиля или ошибки записи) — это не
// тест и не сверка, а средство для читателя: запустить на своей машине,
// своей платформе и своей версии Go и увидеть сырые числа своими глазами,
// вместо того чтобы верить пересказу в статье.
//
// Поднимает РОВНО ДВА класса — mutex-holder-gone и send-no-receiver
// (leaks.PerClass*2 = 4 горутины суммарно) — и печатает заголовок профиля
// («goroutineleak profile: total N») после каждого из нескольких подряд
// идущих WriteTo(). Именно эта проба — источник чисел «count=2, затем
// count=4», на которые ссылаются статья, results/00-findings.txt, README.md
// и cmd/leaks/main.go, когда говорят о поведении первого/второго снятия на
// Windows. Сырой вывод сохранён в results/04-writeto-probe.txt.
package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"time"

	"tech.khorost/goroutine-leak-cookbook/leaks"
)

// snapshotCount — сколько раз подряд снять профиль. Три: чтобы увидеть не
// только рост между первым и вторым снимком, но и то, меняется ли что-то
// дальше.
const snapshotCount = 3

func main() {
	// Переиспользуем классы из leaks.All() — без дублирования их кода.
	// Явно выбираем только два: mutex-holder-gone и send-no-receiver.
	var selected []leaks.Leak
	for _, l := range leaks.All() {
		if l.Name == "mutex-holder-gone" || l.Name == "send-no-receiver" {
			selected = append(selected, l)
		}
	}
	if len(selected) != 2 {
		fmt.Fprintln(os.Stderr, "ОШИБКА: ожидались ровно два класса (mutex-holder-gone, send-no-receiver) в leaks.All()")
		os.Exit(1)
	}

	for _, l := range selected {
		l.Start()
	}
	fmt.Printf("проба: классы mutex-holder-gone + send-no-receiver (%d горутин суммарно, %d снимков подряд)\n\n",
		leaks.PerClass*len(selected), snapshotCount)

	// Дать горутинам дойти до блокировки, прежде чем снимать первый профиль —
	// иначе «профиль ничего не нашёл» значило бы «мы поспешили», а не то, что
	// мы здесь наблюдаем.
	time.Sleep(500 * time.Millisecond)

	prof := pprof.Lookup("goroutineleak")
	if prof == nil {
		fmt.Fprintln(os.Stderr, "goroutineleak недоступен — нужен Go 1.27+")
		os.Exit(1)
	}

	for i := 1; i <= snapshotCount; i++ {
		var buf bytes.Buffer
		if err := prof.WriteTo(&buf, 1); err != nil {
			fmt.Fprintf(os.Stderr, "ошибка снятия снимка %d: %v\n", i, err)
			os.Exit(1)
		}
		header := firstLine(buf.String())
		fmt.Printf("снимок %d: %s\n", i, header)
	}

	fmt.Println("\nЭта проба ничего не утверждает и не падает по числам — она только")
	fmt.Println("показывает то, что происходит на этой машине и этой версии Go прямо")
	fmt.Println("сейчас. Если числа отличаются от зафиксированных в results/00-findings.txt,")
	fmt.Println("это не ошибка пробы — это повод пересмотреть текст статьи и стенда.")
}

// firstLine возвращает первую строку текстового профиля — заголовок вида
// «goroutineleak profile: total N», который и есть предмет наблюдения.
func firstLine(profile string) string {
	if i := strings.IndexByte(profile, '\n'); i >= 0 {
		return profile[:i]
	}
	return profile
}
