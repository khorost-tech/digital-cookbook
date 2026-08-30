// Поднимает все классы утечек, снимает профиль goroutineleak и сверяет число
// найденных утечек с известным заранее. Дополнительно поднимает контрольный
// класс (мьютекс держит живая горутина) и проверяет, что детектор НЕ считает
// его утечкой — это демонстрация правила достижимости, а не ещё один класс.
//
// Профиль стал общедоступен в Go 1.27: GOEXPERIMENT goroutineleakprofile удалён.
// Проверка — точное равенство: «примерно столько» не отличило бы работающий
// профиль от совпадения.
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

func main() {
	want := 0
	for _, l := range leaks.All() {
		n := l.Start()
		fmt.Printf("класс %-28s поднято утечек: %d\n", l.Name, n)
		want += n
	}

	control := leaks.Control()
	control.Start()
	fmt.Printf("класс %-28s поднято горутин: %d (контрольный, НЕ утечка)\n", control.Name, leaks.PerClass)

	// Горутинам нужно дойти до блокировки, иначе профиль снимется раньше, чем
	// утечка состоится, и «профиль ничего не нашёл» будет означать «мы поспешили».
	time.Sleep(500 * time.Millisecond)

	prof := pprof.Lookup("goroutineleak")
	if prof == nil {
		fmt.Fprintln(os.Stderr, "ОШИБКА: профиль goroutineleak недоступен — нужен Go 1.27+")
		os.Exit(1)
	}

	// ВАЖНО: WriteTo сам запускает цикл детекции (runtime/pprof.go зовёт
	// runtime_goroutineLeakGC() внутри обработчика goroutineleak) — отдельный
	// runtime.GC() перед снятием не нужен и раньше стоял здесь по неверной
	// причине (см. results/00-findings.txt).
	//
	// Но одного WriteTo часто мало: это НАБЛЮДЕНИЕ, а не документированный
	// контракт API. Проверено (Linux, WSL, go1.27.0): первый WriteTo() в
	// свежем процессе дал count=0, второй подряд — count=2 (два держателя
	// на mutexHolderGone); 1-3 обычных runtime.GC() перед одним WriteTo() не
	// заменяют второй цикл детекции — тоже count=0. Отдельная проба на ДВУХ
	// классах (mutex-holder-gone + send-no-receiver, 4 горутины суммарно, не
	// все пять классов этого стенда) — см. cmd/writeto-probe и
	// results/04-writeto-probe.txt — на Windows и на Linux показала: первый
	// снимок нашёл только один класс (count=2), второй — оба (count=4),
	// третий не добавил ничего. Точный механизм НЕ установлен — правдоподобная причина
	// в том, что findGoroutineLeaks() (runtime/mgc.go) возобновляет фазу
	// маркировки, пока видит горутины «возможно достижимыми», и лишь второй
	// проход докручивает их до _Gleaked, но точный критерий экспериментом не
	// вскрыт (см. results/00-findings.txt). Отсюда — осторожность ЭТОГО
	// диагностического сценария, а не правило для всех, кто зовёт профиль:
	// если первый снимок выглядит пустым или неполным, стоит снять ещё раз,
	// прежде чем делать вывод, что утечек нет. Контракт API этого не обещает.
	var discard bytes.Buffer
	if err := prof.WriteTo(&discard, 1); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА первого снятия профиля: %v\n", err)
		os.Exit(1)
	}

	// ВАЖНО: Count() читает значение, «последний раз сообщённое рантаймом»
	// (runtime.goroutineleakcount — work.goroutineLeak.count), а не запускает
	// цикл поиска утечек сам. Цикл обнаружения запускает именно WriteTo —
	// вызов Count() ДО WriteTo() читает состояние ДО обнаружения. Поэтому
	// порядок обратный «естественному»: сначала пишем профиль, потом читаем
	// счётчик, и делаем это после ВТОРОГО WriteTo (см. выше).
	var buf bytes.Buffer
	if err := prof.WriteTo(&buf, 1); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА записи профиля: %v\n", err)
		os.Exit(1)
	}
	got := prof.Count()

	fmt.Printf("\nожидалось утечек: %d\nпрофиль нашёл:    %d\n", want, got)
	fmt.Printf("\n--- профиль ---\n%s\n", buf.String())

	failed := false
	if got != want {
		fmt.Fprintf(os.Stderr, "ОТКАЗ: профиль нашёл %d утечек вместо %d\n", got, want)
		failed = true
	}

	// Контрольный класс НЕ должен попасть в профиль утечек: его держатель
	// жив (спит по таймеру), а детектор не винит горутину, ждущую примитив,
	// который ещё может отпустить живой владелец. Попадание сюда — провал
	// проверки правила достижимости, а не «нашлась ещё одна утечка».
	if strings.Contains(buf.String(), "mutexHeldByLiveGoroutine") {
		fmt.Fprintln(os.Stderr, "ОТКАЗ: контрольный класс mutex-held-by-live-goroutine найден в профиле утечек — нарушено правило достижимости")
		failed = true
	}

	if failed {
		os.Exit(1)
	}
}
