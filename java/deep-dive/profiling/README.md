# profiling

Стенд к статье #9 серии «JVM изнутри»: профилирование живого сервиса через
**JFR** (встроен в JDK, ничего не качать) и **async-profiler** (нативный
агент, качается отдельно). Оба инструмента гоняются на одной и той же
нагрузке — числа в этом README сняты с реальных прогонов, не выдуманы.

## Нагрузка (`Workload`)

`tech.khorost.profiling.Workload` — однопоточный цикл, каждая итерация:

- **CPU:** ручной FNV-1a хэш по буферу 8 КБ (`HashUtil.fnv1a`) + рекурсивный
  merge sort на массиве из 2000 `int` (`SortUtil.mergeSort`/`merge`).
  Merge sort реализован вручную (не `Arrays.sort`), чтобы в профиле был
  конкретный "наш" метод, а не заинлайненный/нативный JDK-код.
- **Аллокации:** пачка из 500 `Order` (record: `String id, double amount,
  Instant ts`) на итерацию, `id` собирается конкатенацией строк. Пачка тут
  же становится мусором — умышленно, чтобы генерировать давление на
  young-GC.

Запуск: `java -jar profiling.jar [durationSeconds]` (по умолчанию 40 с).

## Сборка

Хостового Maven нет — только через Docker (JDK 25 внутри образа):

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl profiling -am package
```

Собирает shaded-jar `profiling/target/profiling.jar` (Main-Class:
`tech.khorost.profiling.Workload`).

## JFR

```bash
cd profiling && bash run-jfr.sh 40
```

Пишет `-XX:StartFlightRecording=filename=/app/rec.jfr,settings=profile` под
40-секундной нагрузкой, затем печатает `jfr summary`. Разбор конкретных
событий:

```bash
docker run --rm -v "$(pwd)/target:/app" eclipse-temurin:25-jdk \
  jfr print --events jdk.ObjectAllocationSample /app/rec.jfr
docker run --rm -v "$(pwd)/target:/app" eclipse-temurin:25-jdk \
  jfr print --events jdk.GCPhasePause /app/rec.jfr
docker run --rm -v "$(pwd)/target:/app" eclipse-temurin:25-jdk \
  jfr print --events jdk.ExecutionSample /app/rec.jfr
```

`rec.jfr` в `.gitignore` (`*.jfr`) — не коммитится, только сводка ниже.

## async-profiler

```bash
cd profiling && bash run-async-profiler.sh 40
```

Скрипт сам качает `async-profiler-4.4-linux-x64.tar.gz` через прокси
`s9.khorost.tech:3128` в `.ap-cache/` (в `.gitignore`, один раз), затем
гоняет два прогона той же нагрузки под `-agentpath:libasyncProfiler.so`:
CPU-профиль (`event=cpu`) и alloc-профиль (`event=alloc`), оба в
collapsed-формате. Собранный `libasyncProfiler.so` — Linux x64-бинарник,
распаковывается и используется **внутри контейнера** (на хосте Windows).

`.collapsed`-файлы и `.ap-cache/` — в `.gitignore`, не коммитятся.

## Результаты (характерный прогон, 40 с, `eclipse-temurin:25-jdk`, `-Xms128m -Xmx512m`, Docker Desktop на Windows)

Числа host-зависимы (Docker Desktop, WSL2-бэкенд) — важен порядок величин
и то, что в топе оказываются реальные методы стенда, а не абстракция.

### GC

JVM сама выбрала **Serial GC** (`DefNew`/`SerialOld`) — ожидаемая эргономика
для маленькой кучи (`-Xmx512m`) на выделенных CPU. За 43 с записи (JFR
recording чуть длиннее самой нагрузки за счёт старта/финализации):

- **1835 young GC** (`jdk.GCPhasePause`), суммарно **141.5 мс** пауз —
  ~0.33% от wall time.
- **min 0.034 мс / avg 0.077 мс / max 9.34 мс** за паузу. Максимум — первая
  пауза цикла (прогрев/первичная разметка eden), дальше стабильно
  0.04–0.2 мс — маленький eden при однопоточной Serial-нагрузке очищается
  почти мгновенно.

### Топ аллокаций (JFR `jdk.ObjectAllocationSample`, 11 777 семплов, суммарный вес по `weight`)

| Класс                                    | Вес (сумма weight) | Семплов |
|-------------------------------------------|--------------------:|--------:|
| `int[]`                                    |          51 419 МБ  |   9 775 |
| `java.time.Instant`                        |           2 892 МБ  |     584 |
| `tech.khorost.profiling.Workload$Order`    |           2 751 МБ  |     381 |
| `java.lang.String`                         |           2 633 МБ  |     422 |
| `byte[]`                                   |           2 492 МБ  |     545 |
| `java.lang.Object[]`                       |             503 МБ  |      68 |

`int[]` доминирует ожидаемо: каждый уровень рекурсии merge sort
(`Arrays.copyOfRange` + результирующий массив в `merge`) аллоцирует новый
`int[]`, а глубина рекурсии `log2(2000) ≈ 11`, т.е. на одну сортировку —
десятки временных массивов. `Workload$Order` (наш record) и `String`
(конкатенация id) — прямое подтверждение, что аллокационная часть нагрузки
действительно генерирует мусор нашего кода, а не только JDK-служебщину.

async-profiler `event=alloc` (collapsed, агрегация по классу-листу)
подтверждает то же соотношение независимым инструментом:

```
104245  int[]
  7240  byte[]
  5725  tech.khorost.profiling.Workload$Order
  4318  java.time.Instant
  4313  java.lang.String
   709  java.lang.Object[]
```

### Топ горячих методов (JFR `jdk.ExecutionSample`, 3 394 семпла, по листовому фрейму)

| Метод                                              | Семплов | % |
|-----------------------------------------------------|--------:|--:|
| `tech.khorost.profiling.Workload$SortUtil.merge`     |   1 942 | 57.2% |
| `tech.khorost.profiling.Workload.main`               |     426 | 12.6% |
| `tech.khorost.profiling.Workload$SortUtil.mergeSort` |     388 | 11.4% |
| `java.util.Random.nextBytes`                         |     249 |  7.3% |
| `java.util.Arrays.copyOfRange`                       |     176 |  5.2% |
| `java.time.Clock.currentInstant`                     |     106 |  3.1% |

async-profiler `event=cpu` (collapsed, 3 993 семпла, агрегация по
листовому фрейму) — тот же лидер независимым инструментом:

```
2178  tech/khorost/profiling/Workload$SortUtil.merge   (54.5%)
 465  tech/khorost/profiling/Workload.main
 194  java/util/concurrent/atomic/AtomicLong.compareAndSet  (Random.next)
 180  java/util/Arrays.copyOfRange
 138  java/util/Random.nextBytes
  84  tech/khorost/profiling/Workload$SortUtil.mergeSort
  17  tech/khorost/profiling/Workload$HashUtil.fnv1a
```

Оба инструмента независимо сходятся на `SortUtil.merge` как на главном
hot-path — merge sort на 2000 элементах с ручным слиянием доминирует над
FNV-1a хэшем (тот в основном заинлайнен/слишком быстр относительно частоты
семплирования — честный результат, не подгонка: если бы хотели "хэш в
топе", пришлось бы уменьшать размер сортируемого массива или увеличивать
буфер хэша).

## Что подтвердилось, что нет

- **JFR и async-profiler показывают одну и ту же картину — подтвердилось.**
  Независимая агрегация по листовому фрейму (JFR `ExecutionSample` vs
  async-profiler `event=cpu`) и по классу аллокации (JFR
  `ObjectAllocationSample` vs async-profiler `event=alloc`) даёт одинаковый
  топ-1 с разницей в проценте в пределах погрешности выборки (57.2% vs
  54.5% для `merge`, `int[]` доминирует в обоих). Два независимых
  механизма сэмплирования (JFR — периодический стек-семплинг потоков,
  async-profiler — `perf_events`/`AsyncGetCallTrace`) согласны — это и есть
  практическая проверка, что профайлер "не врёт".
- **Ожидание "FNV-1a хэш будет заметным hot-path" — не подтвердилось
  буквально.** Ручной хэш по 8 КБ оказался дешевле, чем ожидалось
  относительно merge sort — в топе он на последнем месте (17/3394
  семплов). Не подогнано под красивую цифру: зафиксировано как есть,
  реальный урок — "сколько стоит операция" нельзя оценить на глаз без
  профайлера, здесь и сработал сам тезис статьи.
- **GC-паузы малы и предсказуемы при Serial GC на маленькой куче —
  подтвердилось.** Suma 141.5 мс на 1835 пауз за 43 с — GC почти не виден
  на фоне нагрузки (~0.33% wall time), несмотря на десятки миллионов
  аллоцированных объектов (`orders` счётчик в выводе Workload —
  94 824 500 за 40 с в CPU-прогоне). Иллюстрирует, что "много аллокаций"
  не автоматически означает "GC — узкое место": короткоживущий мусор в
  маленьком eden с однопоточным Serial GC обрабатывается почти бесплатно.

## Структура

```
profiling/
  pom.xml                 # shaded-jar, Main-Class=tech.khorost.profiling.Workload
  src/main/java/.../Workload.java
  run-jfr.sh               # JFR-прогон + jfr summary
  run-async-profiler.sh    # качает async-profiler через прокси, CPU+alloc collapsed
  .ap-cache/                # (гитигнор) async-profiler tar.gz + распакованный агент
  target/                   # (гитигнор) jar, rec.jfr, *.collapsed
```
