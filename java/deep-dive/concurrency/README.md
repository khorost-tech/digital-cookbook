# concurrency

Стенд к статье про модели конкурентности на JVM: один и тот же I/O-bound
сценарий прогоняется тремя способами — **virtual threads (Loom)**,
**platform threads (пул)**, **Reactor**. Данные для SVG #5 серии.

Kotlin coroutines этим же сценарием измеряет отдельный Gradle/Kotlin-модуль
(`kotlin/`) — числа сводятся в статье, здесь корутины не измеряются
(осознанный выбор: Kotlin через Maven — лишняя возня, а
Gradle-модуль уже выделен под весь Kotlin-контент).

## Сценарий

- N = 10 000 «одновременных» задач.
- Заглушка I/O: `Thread.sleep(100)` для блокирующих моделей,
  `Mono.delay(Duration.ofMillis(100))` для Reactor.
- Метрики: throughput (задач/сек), latency p50/p99 (submit → completion),
  peak RSS процесса (`/proc/self/status` → `VmHWM`, историчный пик, не
  текущее значение), heap used/committed (`MemoryMXBean`).

## Режимы

Каждый режим — **отдельный процесс JVM** (важно для честного peak RSS: без
этого память одного режима смешивалась бы с памятью другого внутри общей
кучи/процесса).

| mode             | Executor                                       | Что демонстрирует |
|-------------------|-------------------------------------------------|--------------------|
| `vt`               | `Executors.newVirtualThreadPerTaskExecutor()`    | Базовый случай: все N задач одновременно, ОС-потоков почти не тратится |
| `platform`         | `Executors.newFixedThreadPool(200)`              | Throughput упирается в размер пула — очередь (N/K партий) |
| `platform-large`   | `Executors.newFixedThreadPool(N)`                | Throughput почти как у VT, но ценой ~N платформенных потоков — растёт RSS |
| `reactor`          | `Flux.range(N).flatMap(Mono.delay, concurrency=N)` | Высокий throughput без потока на задачу, но через реактивную цепочку |

## Сборка

Хостового Maven нет — только через Docker (JDK 25 внутри образа):

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl concurrency -am package
```

Собирает shaded-jar `concurrency/target/concurrency.jar` (Main-Class:
`tech.khorost.concurrency.Bench`).

## Прогон

Один режим:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -m 4g \
  -v "$(pwd)/concurrency/target:/app" eclipse-temurin:25-jdk \
  java -Xms64m -Xmx1g -jar /app/concurrency.jar vt 10000 100
#                                                 ^^   ^^^^^ ^^^
#                                               mode    n   sleepMs
```

Все четыре режима сразу:

```bash
cd concurrency && MSYS_NO_PATHCONV=1 bash run-all.sh 10000 100
```

## Результаты (характерный прогон)

Docker Desktop на Windows, контейнер `eclipse-temurin:25-jdk` с `-m 4g`,
флаги JVM одинаковые для всех режимов (`-Xms64m -Xmx1g`). **Числа
host-зависимы** — важен порядок величин и относительное сравнение внутри
одного прогона, не абсолютные цифры.

| Модель                        | Throughput (задач/сек) | p50 latency | p99 latency | Peak RSS   |
|--------------------------------|------------------------:|------------:|------------:|-----------:|
| Virtual threads                |                 49 484   |    101.6 мс |    105.8 мс |    ~89 МБ  |
| Platform threads, пул 200      |                  1 940   |  2 499.0 мс |  4 941.0 мс |    ~97 МБ  |
| Platform threads, пул 10 000   |                  3 296   |    100.4 мс |    102.8 мс |   ~366 МБ  |
| Reactor (`flatMap`, N=10 000)  |                 16 494   |    100.3 мс |    154.2 мс |    ~98 МБ  |

Повторные прогоны (см. ниже) держатся в пределах ±10% по throughput/latency;
`platform-large` RSS колебался заметнее — 366..427 МБ между прогонами (создание
10 000 нативных потоков — дорогая и не полностью детерминированная операция).

<details>
<summary>Сырой вывод (2 прогона на режим)</summary>

```
=== vt, run 1 ===
wall_ms=202.1 throughput=49484.1 p50=101.6ms p99=105.8ms peak_rss=90812kB

=== vt, run 2 ===
wall_ms=188.6 throughput=53032.0 p50=103.1ms p99=110.1ms peak_rss=88028kB

=== platform (K=200), run 1 ===
wall_ms=5154.5 throughput=1940.0 p50=2499.0ms p99=4941.0ms peak_rss=99384kB

=== platform (K=200), run 2 ===
wall_ms=5123.1 throughput=1951.9 p50=2496.5ms p99=4949.7ms peak_rss=96208kB

=== platform-large (K=10000), run 1 ===
wall_ms=3034.4 throughput=3295.5 p50=100.4ms p99=102.8ms peak_rss=373956kB

=== platform-large (K=10000), run 2 ===
wall_ms=3160.0 throughput=3164.5 p50=100.5ms p99=102.4ms peak_rss=426816kB

=== reactor, run 1 ===
wall_ms=606.3 throughput=16493.9 p50=100.3ms p99=154.2ms peak_rss=97604kB

=== reactor, run 2 ===
wall_ms=589.7 throughput=16958.0 p50=100.3ms p99=157.5ms peak_rss=101132kB
```

</details>

## Что подтвердилось, что нет

- **VT: высокий throughput при низкой памяти — подтвердилось.** ~49 500
  задач/сек при peak RSS ~89 МБ — лучший результат по обеим осям
  одновременно, без единой строчки реактивного кода.
- **Platform threads с малым пулом упираются в throughput — подтвердилось.**
  Пул 200 даёт throughput ~1 940 (≈ N / (N/K × sleepMs) — арифметика
  партиями по 200 штук), а p50/p99 latency улетают в секунды из-за очереди —
  задача может просидеть в очереди дольше, чем длится сам I/O.
- **Platform threads с пулом ~N упираются в память — подтвердилось, но
  мягче ожидаемого.** RSS вырос с ~90 МБ до ~370–430 МБ (в ~4 раза) —
  заметно, но не крах. 10 000 платформенных потоков в контейнере с лимитом
  4 ГБ и default `-Xss` (1 МБ виртуальных, но не все страницы стека реально
  тронуты) — это ещё не предел; на реальном проде разница будет
  чувствительнее при десятках/сотнях тысяч потоков или более тяжёлых стеках.
  Для статьи это честная иллюстрация тренда «дороже», а не сценарий
  «падает» — так и зафиксировано, без подгонки под более драматичный ассерт.
- **Reactor: высокий throughput, память сопоставима с VT — подтвердилось**
  по throughput (~16 500, третье место после VT) и по памяти (~98 МБ,
  на уровне VT/platform-200). Интересный нюанс: throughput Reactor заметно
  ниже VT при том же сценарии «все N сразу» — видимо, накладные расходы
  `flatMap`/`Schedulers.parallel()` таймер-колеса на N=10 000 таймеров
  весомее, чем у виртуальных потоков. Это не противоречит тезису статьи
  (Reactor эффективен по памяти, но код сложнее), но нюанс throughput стоит
  проговорить явно, а не молчать.
