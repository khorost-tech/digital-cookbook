# C++ — управление памятью (стенд к статье «across-languages»)

Три сценария на `std::shared_ptr` / `std::weak_ptr`, собранные и запущенные в
Docker-образе `gcc:13` с AddressSanitizer + LeakSanitizer. Весь вывод ниже —
дословный, снят с реальных прогонов.

## Тулчейн (контрольная величина)

```
g++ (GCC) 13.4.0
Copyright (C) 2023 Free Software Foundation, Inc.
```

## Как запускать

C-компилятора на хосте нет — всё идёт через контейнер. Флаги
`--cap-add=SYS_PTRACE --security-opt seccomp=unconfined` обязательны: без них
LeakSanitizer не может сканировать процесс. Дополнительно передаём
`ASAN_OPTIONS=detect_leaks=1` явно (на Linux это и так дефолт, но фиксируем).

Проще всего — портабельный `build.sh`: путь монтирования он берёт из своего
расположения (работает из любой директории, на Linux/macOS и в Git Bash),
`MSYS_NO_PATHCONV=1` выставляет сам, а сборка **fail-loud** — падение компиляции
роняет скрипт (ненулевой код от санитайзера на найденном дефекте — ожидаем и
печатается как `program-exit=N`, не маскируется).

```sh
sh build.sh              # собрать и прогнать все три сценария
sh build.sh cycle_leak   # один сценарий (cycle_leak | cycle_fixed | uaf)
```

Под капотом — `docker run` с образом `gcc:13` и флагами
`--cap-add=SYS_PTRACE --security-opt seccomp=unconfined`; см. сам `build.sh`.

---

## Опора 1 — цикл `shared_ptr` ТЕЧЁТ

`cycle_leak.cpp`: `Node A` и `Node B`, `A->other = B`, `B->other = A` — обе
связи владеющие (`shared_ptr`). После `a.reset(); b.reset()` внешние handle'ы
ушли, но узлы держат друг друга: `use_count` каждого остаётся 1, деструкторы
не вызываются.

**Наблюдаемый счётчик деструкторов: `0`** (ожидание 2 — цикл не собран).

LeakSanitizer при выходе отчитывается об утечке 2 аллокаций (по 40 байт —
inplace-блок `make_shared`: control block + `Node`):

```
Node(A) constructed
Node(B) constructed
before reset: a.use_count=2 b.use_count=2
destructors called: 0 (expected 2, but cycle leaks -> 0)

=================================================================
==12==ERROR: LeakSanitizer: detected memory leaks

Indirect leak of 40 byte(s) in 1 object(s) allocated from:
    #0 ... in operator new(unsigned long) (libasan.so.8+0xdc108)
    ...
    #8 ... in std::make_shared<Node, char const (&) [2]>(char const (&) [2]) .../bits/shared_ptr.h:1010
    #9 0x4013ab in make_cycle /src/cycle_leak.cpp:45
    #10 0x401657 in main /src/cycle_leak.cpp:68

Indirect leak of 40 byte(s) in 1 object(s) allocated from:
    ...
    #9 0x401378 in make_cycle /src/cycle_leak.cpp:44
    #10 0x401657 in main /src/cycle_leak.cpp:68

SUMMARY: AddressSanitizer: 80 byte(s) leaked in 2 allocation(s).
```

Код возврата: **1** (LSan завершает процесс ненулевым кодом при утечке).

> Почему в исходнике есть `make_cycle()` и `clobber_stack()`: LeakSanitizer
> консервативно сканирует стек как корни. Если строить цикл прямо в `main`,
> сырое значение указателя на `Node` остаётся в стековом кадре, LSan считает
> объект достижимым и утечку НЕ показывает (ложноотрицательный результат —
> именно это и произошло в первом прогоне). Вынос в отдельную функцию + затирка
> стека убирают остаточные указатели. Утечка при этом настоящая: это защита от
> корневого сканера, а не «подкрутка» результата.

---

## Опора 1 — починка через `weak_ptr`

`cycle_fixed.cpp`: обратная связь — `std::weak_ptr` (`B --weak--> A`). Она не
владеет, `use_count` A не удерживает. После `reset()` счётчики доходят до 0,
деструкторы вызываются.

**Наблюдаемый счётчик деструкторов: `2`.** ASan/LSan — чисто, код возврата 0.

```
Node(A) constructed
Node(B) constructed
before reset: a.use_count=1 b.use_count=2
b->back.lock() -> Node(A) alive, use_count=2
~Node(A) destroyed (destroyed=1)
~Node(B) destroyed (destroyed=2)
destructors called: 2 (expected 2)
```

Обратите внимание: `a.use_count=1` (только внешний handle — weak-ссылка не
считается), `b.use_count=2` (внешний handle + `a->strong`). `a.reset()` уводит
A в 0 -> деструктор A рвёт `a->strong` -> B падает до 1 -> `b.reset()` -> B в 0.

---

## Опора 2 — use-after-free ловится в РАНТАЙМЕ

`uaf.cpp`: **ровно тот же паттерн, что в Rust** (`rust/borrow_error/uaf.rs`) —
держим указатель на элемент `std::vector` (`&v[0]`), затем `v.push_back(4)`
реаллоцирует буфер (размер превысил ёмкость), старый буфер освобождается, и мы
читаем висячий указатель. В Rust это ошибка компиляции (`E0502`, бинарь не
создан); в C++ компилируется молча, а ошибку в рантайме ловит AddressSanitizer:

```
before push: *first = 1, size=3, cap=3
=================================================================
==12==ERROR: AddressSanitizer: heap-use-after-free on address 0x602000000010 ...
READ of size 4 at 0x602000000010 thread T0
    #0 ... in main /src/uaf.cpp:27

0x602000000010 is located 0 bytes inside of 12-byte region [0x602000000010,0x60200000001c)
freed by thread T0 here:            (реаллокация вектора освободила старый буфер)
    #0 ... in operator delete(...) (libasan.so.8+...)
    ...
    #  ... in std::vector<int>::push_back(...) .../bits/vector.tcc
    #  ... in main /src/uaf.cpp:25

previously allocated by thread T0 here:   (первичный буфер из 3 int = 12 байт)
    #0 ... in operator new(...) (libasan.so.8+...)
    ...
    #  ... in main /src/uaf.cpp:22

SUMMARY: AddressSanitizer: heap-use-after-free /src/uaf.cpp:27 in main
==12==ABORTING
```

Код возврата: **1** (`ABORTING`). Регион ровно `12-byte` = 3 × `int` — старый
буфер вектора. ASan даёт три стека: где прочитали (use, строка 27), где
освободили (free — `push_back`, строка 25), где выделили (alloc, строка 22).

---

## Caveats по запуску санитайзеров в контейнере

- **SYS_PTRACE + seccomp=unconfined обязательны.** Без них LeakSanitizer падает
  с ошибкой ptrace и не может отчитаться об утечках.
- **Преобразование путей Git Bash.** На Windows/Git Bash без `MSYS_NO_PATHCONV=1`
  Docker получает `working directory 'C:/Program Files/Git/src' is invalid`.
- **Буферизация stdout.** LSan вызывает `_exit()` после проверки утечек, а ASan
  — `abort()` на ошибке доступа; оба минуют штатный сброс буферов stdio.
  Поэтому в `cycle_leak.cpp` и `uaf.cpp` стоит явный `fflush(stdout)` — без него
  строки программы (счётчик, `before push`) теряются и в выводе виден только
  отчёт санитайзера.
- **Ложноотрицательный LSan на стеке.** См. врезку в опоре 1: цикл, построенный
  прямо в `main`, LSan может не увидеть из-за остаточных указателей в стеке.
