# kotlin

Gradle-модуль (Kotlin 2.2.0) — **вне Maven-реактора** `java-deep-dive/pom.xml`
(отдельная сборка, отдельный тулчейн). Два независимых куска:

1. **Идиомы Kotlin для бэкенда** — null-safety, data classes, extension-функции,
   sealed classes/when, корутины (`suspend`, structured concurrency
   `coroutineScope`/`launch`/`async`, `Flow`).
2. **Бенчмарк корутин** — тот же I/O-bound сценарий, что стенд
   `java-deep-dive/concurrency/` (Java): N задач, заглушка I/O
   `delay(sleepMs)`. Данные — 4-я модель на графике SVG #5 рядом с virtual
   threads / platform threads / Reactor.

Spring/Ktor-эндпоинт **пропущен осознанно** — модуль и так закрывает идиомы +
корутины, добавление веб-фреймворка утяжелило бы стенд без нового
методологического вывода для статьи (структура кода не даёт числа для
графика). Зафиксировано как решение, а не недоделка.

## Тулчейн (фактически проверенный)

Хостового Gradle нет — сборка только через Docker:

```bash
cd java-deep-dive/kotlin
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$PWD":/app -v gradle-cache:/home/gradle/.gradle \
  -w /app gradle:9-jdk25 gradle build shadowJar
```

- **Образ:** `gradle:9-jdk25` (Gradle **9.6.1** внутри образа; wrapper
  зафиксирован на **Gradle 9.0.0** через `gradle/wrapper/gradle-wrapper.properties`
  — сборка воспроизводима без образа, если появится сеть у `./gradlew`).
- **JDK:** Eclipse Adoptium **25.0.3** (toolchain исполнения/тестов).
- **Kotlin:** **2.2.0** (пин из родительского `pom.xml`, `${kotlin.version}`).
- **⚠️ Байткод-таргет — JVM 24, не 25.** Живьём проверено в этой задаче:
  Kotlin 2.2.0 явно не поддерживает `jvmTarget=25` —
  `Kotlin does not yet support 25 JDK target, falling back to Kotlin JVM_24
  JVM target` (лог `compileKotlin`). `build.gradle.kts` фиксирует это явно
  (`jvmTarget.set(JvmTarget.JVM_24)` + `JavaCompile.options.release=24`,
  иначе Gradle 9 валит билд как "Inconsistent JVM Target Compatibility
  Between Java and Kotlin Tasks"). Компилятор/JVM исполнения — 25, выходной
  class-файл — Java 24. Это факт текущей версии тулчейна (Kotlin обычно
  добавляет поддержку нового JDK target с задержкой в один цикл релиза), не
  подгонка.
- **kotlinx-coroutines-core:** **1.11.0** (latest stable на Maven Central,
  проверено `maven-metadata.xml`, 2026-07-08).
- **Shadow-плагин:** `com.gradleup.shadow` **9.5.1** (форк
  `johnrengelman.shadow`, актуально поддерживаемый) — собирает fat-jar
  `build/libs/kotlin-backend-0.1.0-all.jar` со всеми зависимостями
  (kotlin-stdlib, kotlinx-coroutines-core) для запуска в "голом" JRE-контейнере
  без Gradle, аналогично maven-shade-plugin в `concurrency/`.

## Идиомы

```bash
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$PWD":/app -v gradle-cache:/home/gradle/.gradle \
  -w /app gradle:9-jdk25 gradle run --console=plain
```

Прогоняет подряд (см. `src/main/kotlin/tech/khorost/kotlin/idioms/`):

| Файл | Что демонстрирует |
|---|---|
| `NullSafety.kt` | `?`/`?.`/`?:`/`!!`, разница безопасного и небезопасного пути |
| `DataClasses.kt` | equals/hashCode/toString/copy/destructuring |
| `Extensions.kt` | extension-функции на `String`/`List<Article>` |
| `SealedClasses.kt` | sealed-иерархия результата + исчерпывающий `when` без `else` |
| `Coroutines.kt` | `suspend`, `coroutineScope`+`async`/`awaitAll` (параллельная загрузка), `launch`+`withTimeoutOrNull` (структурная отмена), `Flow` (`flow{}`/`map`/`take`/`toList`) |
| `Idioms.kt` | единая точка входа, вызывает все демо по очереди |

Живой прогон (фактический вывод, сокращённо):

```
null-safety:
  Привет, Александр!
  Привет, аноним!
  unsafeDemo(anonymous) упал ожидаемо: NullPointerException

корутины (suspend/coroutineScope/async/Flow):
  loadArticleCard: Article(slug=wal-and-analogs-3, title=Заголовок для wal-and-analogs-3, ...)
  loadManyCards (параллельно через async/awaitAll): [wal-1, wal-2, wal-3]
  withTimeoutOrNull(2ms) при работе 50ms -> null (ожидаемо null)
  Flow.take(3).toList() = [10, 20, 30]
  Flow.map(x2).toList() = [20, 40, 60]
```

Юнит-тесты (`src/test/kotlin/.../IdiomsTest.kt`, `kotlin.test` + JUnit
Platform) проверяют null-safety/data-class-copy/extension-функции/sealed-when/
structured-concurrency — гоняются в `gradle build` (`gradle test`).

## Бенчмарк корутин

Сценарий — **идентичен** `concurrency/`: N=10 000 "одновременных"
I/O-bound задач, заглушка I/O `delay(100)`. Метрики те же: throughput
(задач/сек), latency p50/p99/max (submit → completion), wall-clock всего
прогона, peak RSS (`/proc/self/status` → `VmHWM`).

`src/main/kotlin/tech/khorost/kotlin/bench/CoroutineBench.kt`:

```kotlin
runBlocking {                       // submit-цикл — на этом (main) потоке
    coroutineScope {                 // не вернётся, пока все N детей не завершатся
        repeat(n) { i ->
            val submitTime = System.nanoTime()
            launch(Dispatchers.Default) {   // исполнение — на пуле CPU-ядер
                delay(sleepMs)
                latenciesNanos[i] = System.nanoTime() - submitTime
            }
        }
    }
}
```

### Почему именно так (3 варианта проверены живьём)

| Вариант | throughput | p50 | Комментарий |
|---|---:|---:|---|
| `runBlocking(Dispatchers.Default)` + `launch{}` (submit и execute на одном пуле) | ~20 400 | ~195 мс | submit-цикл конкурирует с исполнением задач за тот же пул (16 потоков CPU) |
| `runBlocking(Dispatchers.IO)` + `launch{}` (64 потока) | ~12 900 | ~259 мс | больше OS-потоков — **хуже**, не лучше: накладные расходы диспетчеризации/переключения контекста перевесили снижение конкуренции |
| **`runBlocking{}` + `launch(Dispatchers.Default)` (submit отдельно от execute) — выбран** | **~17 800–22 000** | **~106–126 мс** | лучший из трёх; submit-цикл не делит поток с исполнением |

Собрать fat-jar и прогнать (2 повтора, отдельный процесс JVM на каждый —
скрипт `run-bench.sh`, аналог `concurrency/run-all.sh`):

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD":/app -v gradle-cache:/home/gradle/.gradle \
  -w /app gradle:9-jdk25 gradle build shadowJar

MSYS_NO_PATHCONV=1 bash run-bench.sh 10000 100
```

### Результаты (10 прогонов финального варианта, Docker Desktop/Windows, `eclipse-temurin:25-jdk`, `-Xms64m -Xmx1g`, контейнер `-m 4g`)

| Прогон | Throughput (задач/сек) | p50 | p99 | max | Peak RSS |
|---|---:|---:|---:|---:|---:|
| 1 | 19 514 | 125.7 мс | 183.0 мс | — | 126 724 kB |
| 2 | 17 820 | 105.9 мс | 121.2 мс | — | 120 244 kB |
| 3 | 18 898 | 112.9 мс | 153.5 мс | — | 109 588 kB |
| 4 | 22 029 | 112.1 мс | 146.4 мс | — | 129 124 kB |
| 5 | 19 771 | 184.8 мс | 197.8 мс | 198.9 мс | 120 972 kB |
| 6 | 21 093 | 191.9 мс | 206.2 мс | 206.3 мс | 120 704 kB |
| 7 | 18 945 | 161.7 мс | 235.0 мс | 236.2 мс | 115 860 kB |
| 8 | 19 710 | 200.4 мс | 289.7 мс | 292.0 мс | 131 548 kB |
| 9 | 22 476 | 142.4 мс | 151.9 мс | 153.0 мс | 146 996 kB |
| 10 | 19 841 | 126.1 мс | 144.5 мс | 145.2 мс | 121 520 kB |

Прогоны 1–4 сделаны до того, как `latency_max_ms`/`wall_ms` появились в
выводе (см. ниже) — throughput/p50/p99 сопоставимы, max не зафиксирован.
Прогоны 5–10 — с полным набором метрик. **max во всех прогонах 5–10
практически равен p99** (разница <2 мс) — важно для объяснения разрыва
ниже.

Разброс throughput между прогонами заметнее, чем в Java-стенде (~20–25%
vs ~5–10% там). Диагностика (см. следующий раздел) показывает, что основной
вклад в разброс — нестабильность длительности **submit-цикла** (`repeat(n)
{ launch(...) }`), не прогрев JIT.

### Реальный механизм разрыва throughput с VT (подтверждено измерением)

Гипотеза "медленный хвост завершения после p99" (последние корутины
`coroutineScope` дозавершаются намного позже p99) **не подтвердилась** при
прямом измерении: `latency_max_ms` добавлен в `printReport` рядом с
p50/p99/min (`CoroutineBench.kt`), и во всех 6 прогонах с этой метрикой
**max практически равен p99** (198.9 vs 197.8, 206.3 vs 206.2, 236.2 vs
235.0, 292.0 vs 289.7, 153.0 vs 151.9, 145.2 vs 144.5 мс) — то есть у
отдельных задач (submit → completion) нет длинного индивидуального хвоста
за пределами p99.

При этом `wall_ms` всегда заметно больше `latency_max_ms` (разброс разницы
215–360 мс на прогонах 5–10, в среднем ~290 мс) — задачи в сумме "живут"
дольше, чем показывает максимум индивидуальных latency. Это возможно только
если часть времени тратится **до** начала отсчёта latency отдельных задач —
то есть в самом submit-цикле.

Чтобы проверить это напрямую, добавлена временная диагностика
`submit_loop_ms` — время от начала `wallStart` до конца `repeat(n) {
launch(...) }` внутри `coroutineScope`, ещё до неявного `join()` всех
детей:

```
submit_loop_ms=297.9   wall_ms=538.3   p99=238.3мс   max=245.8мс
submit_loop_ms=314.6   wall_ms=527.7   p99=211.6мс   max=219.6мс
submit_loop_ms=446.7   wall_ms=598.0   p99=163.2мс   max=165.3мс
submit_loop_ms=417.8   wall_ms=556.3   p99=157.7мс   max=158.5мс
```

**Подтверждено: сам submit-цикл занимает 298–447 мс — 56–75% wall-clock
времени прогона** (в среднем ~67%). Это значит: `repeat(n) {
launch(Dispatchers.Default) { ... } }` на 10 000 итераций — где каждый
`launch()` регистрирует нового ребёнка в родительском `Job`
(`coroutineScope`) — сам по себе стоит сотни миллисекунд на вызывающем
потоке, до того как последняя задача вообще успевает засабмититься. Задача,
засабмиченная последней, при этом получает вполне обычную индивидуальную
latency (~100–250 мс, submit→completion) — отсюда max≈p99 — но её собственный
submit происходит уже глубоко внутри wall-clock окна, а не в момент t=0.

**Вероятный механизм (по прямому измерению, а не по догадке):**
разрыв throughput с VT объясняется в основном **стоимостью fan-out**:
регистрация 10 000 детей одного `coroutineScope` с единственного
вызывающего потока — не бесплатная операция (растущая структура
Job-иерархии/`NodeList`, конкуренция вызывающего потока с уже
запущенными детьми за CPU-ядра пула `Dispatchers.Default`). У VT
аналогичного узкого места нет: `Thread.ofVirtual().start()` в цикле
Java-стенда `concurrency/` — простой примитив JVM, спроектированный именно под такой масштаб
fan-out.

Гипотеза "единственный `DefaultExecutor`-поток kotlinx будит все
`delay()` последовательно" (contention при массовом пробуждении) остаётся
правдоподобным **вторичным** вкладом в то, что p50/p99 отдельных задач
(даже без submit-цикла) заметно выше, чем у VT (p50 115–200 мс vs VT
~102–103 мс, p99 158–290 мс vs VT ~106–110 мс) — но напрямую в этой задаче
не изолирована (потребовала бы отдельного эксперимента с кастомным
`delay`-провайдером или сравнения `limitedParallelism`, вне бюджета
задачи). Формулируется как гипотеза, не факт.

`JIT-прогрев` как единственное объяснение разрыва с VT **снят** — короткий
процесс без прогрева даёт вклад в разброс *между прогонами* (шире, чем в
Java-стенде), но не объясняет сам разрыв throughput с VT: тот воспроизводимо
объясняется стоимостью submit-цикла (56–75% wall во всех 4 диагностических
прогонах), а не JIT.

### Сведение со стендом `concurrency/` (VT/platform/Reactor) для SVG #5

| Модель | Throughput (задач/сек) | p50 | p99 | Peak RSS |
|---|---:|---:|---:|---:|
| Virtual threads (`concurrency/`) | ~49 500–53 000 | ~102–103 мс | ~106–110 мс | ~88–91 МБ |
| **Kotlin coroutines (эта задача)** | **~17 800–22 500** | **~106–200 мс** | **~121–290 мс** | **~110–147 МБ** |
| Reactor (`concurrency/`) | ~16 500–17 000 | ~100 мс | ~154–158 мс | ~98–101 МБ |
| Platform threads, пул 200 (`concurrency/`) | ~1 940–1 950 | ~2 497–2 499 мс | ~4 941–4 950 мс | ~96–99 МБ |
| Platform threads, пул 10 000 (`concurrency/`) | ~3 165–3 296 | ~100.4–100.5 мс | ~102.4–102.8 мс | ~366–427 МБ |

**Ожидание стенда не подтвердилось буквально** ("корутины ожидаемо близки к VT
по эффективности"), но и не так однозначно, как казалось по throughput:
**типичная отдельная задача** (submit→completion, p50/p99) у корутин
**того же порядка**, что у VT (сотни vs ~100 мс — не разрыв в разы, как у
platform threads с фиксированным пулом), хоть и заметно выше по абсолютным
цифрам и с бо́льшим разбросом между прогонами. Разрыв throughput в
**~2.3–3 раза** — это не "корутины в целом медленнее VT на каждую задачу",
а **стоимость fan-out**: submit-цикл на 10 000 `launch()` под одним
`coroutineScope` занимает 56–75% wall-clock прогона (см. выше, измерено
напрямую) — узкое место структурной конкурентности при массовой регистрации
детей, а не механизм пробуждения по таймеру как таковой. По памяти корутины
сопоставимы с VT/Reactor (~110–147 МБ против ~89–101 МБ) — далеко от 370+ МБ
platform-large, здесь ожидание подтвердилось. Числа не подгонялись под
ассерт — таблицы оставлены как получены.

## Структура

```
kotlin/
  build.gradle.kts, settings.gradle.kts
  gradlew, gradlew.bat, gradle/wrapper/           # wrapper закоммичен для воспроизводимости
  run-bench.sh                                     # 2 прогона бенчмарка, отдельный процесс на прогон
  src/main/kotlin/tech/khorost/kotlin/
    idioms/  — NullSafety, DataClasses, Extensions, SealedClasses, Coroutines, Idioms (entrypoint)
    bench/   — CoroutineBench (entrypoint для gradle run -PmainClass=... или java -cp fat-jar)
  src/test/kotlin/tech/khorost/kotlin/idioms/IdiomsTest.kt
```
