# Rust: управление памятью — стенд к статье «across-languages»

std-only, без внешних крейтов (сеть при сборке не нужна). Тулчейн: **cargo 1.96.1 / rustc 1.96.1**.

Стенд иллюстрирует две опоры модели памяти Rust на сценарии «узел графа со
взаимной ссылкой» (цикл `node <-> node`):

- **Опора 1.** Подсчёт ссылок (`Rc`) — это НЕ трассирующий GC: цикл сильных
  ссылок он не собирает, память течёт. Починка — разрыв цикла через `Weak`.
- **Опора 2.** Характерный use-after-free НЕ КОМПИЛИРУЕТСЯ: borrow checker
  ловит на этапе сборки то, что в C++ роняет программу в рантайме.

## Дерево

```
rust/
  Cargo.toml
  RUN.md
  src/
    bin/
      cycle.rs                 # опора 1: Rc-цикл течёт (DROPS=0) -> Weak чинит (DROPS=2)
  borrow_error/                # ВНЕ src/ — чтобы `cargo build` оставался зелёным
    uaf.rs                     # опора 2: use-after-free, заведомо не проходит borrow checker
    borrow-error.txt           # захваченный stderr rustc (E0502) + код возврата
    capture.sh                 # репродуктор borrow-error.txt (запускать из login-шелла WSL)
```

## Запуск (WSL, пользователь khap)

Грабли drvfs: сборка на `/mnt/g` может ломать запись артефактов. Исходники —
источник истины в репо; для сборки/запуска копируем на нативную FS.

```bash
# 1) копия на нативную FS
wsl -u khap -- bash -lc 'rm -rf ~/osvbuild/mm-rust && \
  cp -r /mnt/g/7/Projects/Khorost/digital-cookbook-staging/performance/memory-management/across-languages/rust ~/osvbuild/mm-rust'

# 2) версия тулчейна
wsl -u khap -- bash -lc 'cd ~/osvbuild/mm-rust && rustc --version'

# 3) сборка проекта (borrow_error/ вне src/ -> сборка ЗЕЛЁНАЯ)
wsl -u khap -- bash -lc 'cd ~/osvbuild/mm-rust && cargo build'

# 4) опора 1: обе ветки Drop
wsl -u khap -- bash -lc 'cd ~/osvbuild/mm-rust && cargo run --quiet --bin cycle'

# 5) опора 2: компиляция use-after-free (ОЖИДАЕТСЯ ошибка E0502, exit 1)
#    Запуск ИМЕННО как файл из login-шелла — см. «Замечание о границе оболочек».
wsl -u khap -- bash -lc 'bash ~/osvbuild/mm-rust/borrow_error/capture.sh'
```

## Наблюдаемый вывод (дословно)

### rustc --version

```
rustc 1.96.1 (31fca3adb 2026-06-26)
```

### cargo build (проект без borrow_error/ — ЗЕЛЁНЫЙ)

```
   Compiling mm-rust v0.1.0 (/home/khap/osvbuild/mm-rust)
    Finished `dev` profile [unoptimized + debuginfo] target(s) in 0.59s
```

### cargo run --bin cycle (обе ветки Drop)

```
== Случай A: обе связи сильные (Rc<RefCell<..>>) — ЦИКЛ ТЕЧЁТ ==
    До сброса: strong(A)=2, strong(B)=2
    DROPS после drop(a); drop(b) = 0  (ожидание: 0 — УТЕЧКА)

== Случай B: обратная связь слабая (Weak) — ЦИКЛ РАЗОРВАН ==
    До сброса: strong(A)=1, weak(A)=1, strong(B)=2, weak(B)=0
    Drop WeakNode(A)
    Drop WeakNode(B)
    DROPS после drop(a); drop(b) = 2  (ожидание: 2 — освобождены оба)

Итог: Rc — это подсчёт ссылок, а НЕ трассирующий GC.
Цикл сильных ссылок он не собирает — обратную связь разрывают через Weak.
```

Контрольные величины: **DROPS = 0** (цикл сильных `Rc` — деструктор не
вызывается, утечка) и **DROPS = 2** (обратная ссылка через `Weak` разрывает
цикл, оба узла освобождены).

### rustc --edition 2021 borrow_error/uaf.rs (ОЖИДАЕТСЯ ошибка borrow-checker)

```
error[E0502]: cannot borrow `v` as mutable because it is also borrowed as immutable
  --> borrow_error/uaf.rs:23:5
   |
19 |     let first = &v[0];
   |                  - immutable borrow occurs here
...
23 |     v.push(4);
   |     ^^^^^^^^^ mutable borrow occurs here
...
26 |     println!("first = {}", first);
   |                            ----- immutable borrow later used here

error: aborting due to 1 previous error

For more information about this error, try `rustc --explain E0502`.
```

**Код возврата rustc: `1`**, бинарь `uaf` НЕ создан. Тот же паттерн
(`auto& r = v[0]; v.push_back(x); use(r);`) в C++ молча компилируется и
роняет программу в рантайме — здесь его ловит компилятор.

## Замечание о границе оболочек Windows -> WSL (важно для воспроизведения)

При вызове `wsl -u khap -- bash -lc '... $? ...'` инлайновый `$?` (и любые
`$VAR`) в аргументе манглятся ВНЕШНЕЙ оболочкой Windows: `$?` схлопывается в
код последней внешней команды (обычно 0), `$VAR` — в пустоту. Из-за этого
прямой захват кода возврата rustc в командной строке ложно показывал `0`,
хотя компилятор реально падает с `1`.

Проверка propagation: `wsl -u khap -- bash -c 'false; R=$?; echo "$R"'`
печатал пусто/0 вместо `1`.

Решение: вся логика с `$?` вынесена в файл `borrow_error/capture.sh` и
запускается как СКРИПТ (`bash <файл>`) из login-шелла (`bash -lc`). Тогда:
`$?` вычисляется внутренним bash при чтении файла (без внешнего манглинга),
а login-шелл экспортирует PATH с `~/.cargo/bin` дочернему процессу.
Non-login `bash -c` без `-l` дополнительно не видит rustc (PATH) -> `127`.
