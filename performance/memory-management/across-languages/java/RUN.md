# Java: управление памятью (стенд к статье «Управление памятью в разных языках»)

Java-часть стенда. Три опоры: трассирующий GC собирает цикл (контраст с
подсчётом ссылок), детерминированная раскладка объектов и autoboxing через JOL,
качественное сравнение пауз G1 vs ZGC на одной аллокационной нагрузке.

## Тулчейн и запуск

- JDK 21 (у автора — portable Temurin в `$HOME/jdk21`); Maven 3.9.16.
- Зависимость JOL `org.openjdk.jol:jol-core:0.17` (Maven скачивает при первой сборке).

Скрипты **портабельны**: путь берут из своего расположения, тулчейн — из окружения.
На обычной машине (JDK 21 + Maven в PATH, доступный `~/.m2`) достаточно:

```bash
bash build.sh   # компиляция
bash run.sh     # прогон трёх опор + сбор gcpauses-*.txt
```

Переопределения через env (нужны на среде автора — WSL, `~/.m2` под root, drvfs на `/mnt/*`):

- `JAVA_HOME`  — JDK 21 (иначе пробуется `$HOME/jdk21`, затем `java` из PATH);
- `MAVEN_REPO` — локальный репозиторий Maven (штатный `~/.m2` под root недоступен → `$HOME/.m2-osv`);
- `BUILD_DIR`  — куда собирать: на drvfs Maven портит `target/*.class`, поэтому уводим на нативную FS.

```bash
MAVEN_REPO="$HOME/.m2-osv" BUILD_DIR="$HOME/osvbuild/mm-java" JAVA_HOME="$HOME/jdk21" bash build.sh
MAVEN_REPO="$HOME/.m2-osv" BUILD_DIR="$HOME/osvbuild/mm-java" JAVA_HOME="$HOME/jdk21" bash run.sh
```

За файрволом прокси задавайте штатно (settings.xml / `MAVEN_OPTS`) — прод-специфичного
прокси в скриптах больше нет.

### java -version (дословно)

```
openjdk version "21.0.11" 2026-04-21 LTS
OpenJDK Runtime Environment Temurin-21.0.11+10 (build 21.0.11+10-LTS)
OpenJDK 64-Bit Server VM Temurin-21.0.11+10 (build 21.0.11+10-LTS, mixed mode, sharing)
```

---

## Опора 1 — трассирующий GC собирает циклическую структуру

`Cycle` строит 200 000 пар узлов `Node` со взаимными ссылками (`a.other=b`,
`b.other=a`) — 400 000 узлов, каждый в цикле длины 2. На каждый узел
регистрируется `java.lang.ref.Cleaner`. Действие Cleaner **не захватывает сам
Node** — только общий `AtomicInteger` и примитивный `id` (иначе замыкание
удержало бы узел достижимым, и он не был бы собран).

Замер used-heap до/после (`ManagementFactory.getMemoryMXBean()
.getHeapMemoryUsage().getUsed()`), обнуление корней, несколько раундов
`System.gc()` + короткие ожидания, чтобы Cleaner отработал.

Запуск: `java -Xmx2g -cp target/classes:jol-core-0.17.jar tech.khorost.mm.Cycle`

Наблюдаемый вывод (дословно):

```
built pairs=200000 nodes=400000
used_before_bytes=37537344
used_after_bytes=1324600
reclaimed_mb=34.54
cleaners_fired=400000 of 400000
```

**Вывод.** Циклический граф собран целиком: освобождено ~34.5 МБ, сработали
все 400 000 Cleaner'ов — трассирующий сборщик видит недостижимость от корней
независимо от взаимных ссылок. Подсчёт ссылок (`Rc`/`shared_ptr` в C++/Rust)
на таком цикле оставил бы счётчик > 0 и утёк бы — прямой контраст.

---

## Опора 2 — раскладка объекта и autoboxing (JOL, детерминированные байты)

`Layout` печатает `ClassLayout.parseInstance(node).toPrintable()` для
`Cycle.Node` и сравнивает `GraphLayout` для `Integer[]` (боксированные) против
`int[]` той же длины (n = 10 000).

Запуск: `java -cp target/classes:jol-core-0.17.jar tech.khorost.mm.Layout`

Caveat: JOL печатает `# WARNING: Unable to get Instrumentation. Dynamic Attach
failed.` — jol-core 0.17 не содержит `Premain-Class`, поэтому подключить его
как `-javaagent` нельзя. JOL переходит на Unsafe-раскладчик; размеры остаются
детерминированными и корректными (сходятся с инструментальным режимом для этих
простых типов).

Раскладка `Cycle.Node` (дословно):

```
tech.khorost.mm.Cycle$Node object internals:
OFF  SZ                         TYPE DESCRIPTION               VALUE
  0   8                              (object header: mark)     0x0000000000000001 (non-biasable; age: 0)
  8   4                              (object header: class)    0x0101eee8
 12   4                          int Node.id                   42
 16   4   tech.khorost.mm.Cycle.Node Node.other                (object)
 20   4                              (object alignment gap)
Instance size: 24 bytes
Space losses: 0 bytes internal + 4 bytes external = 4 bytes total
```

12 байт заголовка (mark 8 + сжатый class-pointer 4), `int id` 4 байта, сжатая
ссылка `other` 4 байта, 4 байта выравнивания → 24 байта на узел. Ссылки сжаты
(compressed oops, 3-bit shift).

Autoboxing (дословно):

```
== Autoboxing: n=10000 ==
Integer[] total_bytes=200016
int[]     total_bytes=40016
ratio=5.00x
overhead_bytes=160000

== GraphLayout footprint: Integer[] ==
     COUNT       AVG       SUM   DESCRIPTION
         1     40016     40016   [Ljava.lang.Integer;
     10000        16    160000   java.lang.Integer
     10001              200016   (total)

== GraphLayout footprint: int[] ==
     COUNT       AVG       SUM   DESCRIPTION
         1     40016     40016   [I
         1               40016   (total)
```

**Вывод.** `int[10000]` = 40 016 байт (16 заголовок + 10 000×4). `Integer[10000]`
= 200 016 байт: сам массив ссылок тоже 40 016 байт, плюс 10 000 отдельных
объектов `Integer` по 16 байт = 160 000 байт накладных. Ровно **5.00x** и
160 000 байт лишнего только из-за боксинга — цена «всё есть объект» против
примитивов, лежащих плотно в C++/Rust/Go.

---

## Опора 3 — паузы GC, один прогон, качественно

Один и тот же аллокационно-тяжёлый ворклоад (`GcWorkload`: 12 млн
короткоживущих `byte[]`, ~100 000 удерживаются в кольцевом буфере) прогоняется
**дважды на одной нагрузке**: под G1 и под ZGC. Паузы читаются из GC-лога.

Замечание по логированию: паузы G1 (stop-the-world Young Evacuation) видны уже
под тегом `gc`; крошечные stop-the-world паузы ZGC логируются под тегом
`gc+phases`. Чтобы обе опоры содержали строки пауз, запускаю с
`-Xlog:gc,gc+phases` (надмножество указанного `-Xlog:gc`). Это **замер одного
прогона на данной машине**, а не свойство языка — на другом железе/heap числа
будут иными.

Запуск:

```bash
java -Xmx512m -XX:+UseG1GC  -Xlog:gc,gc+phases:file=gcpauses-g1.txt  ... tech.khorost.mm.GcWorkload
java -Xmx512m -XX:+UseZGC   -Xlog:gc,gc+phases:file=gcpauses-zgc.txt ... tech.khorost.mm.GcWorkload
```

Паузы G1 — `gcpauses-g1.txt` (дословный фрагмент, 8 пауз):

```
[0.049s][info][gc       ] GC(0) Pause Young (Normal) (G1 Evacuation Pause) 26M->17M(512M) 6.907ms
[0.058s][info][gc       ] GC(1) Pause Young (Normal) (G1 Evacuation Pause) 40M->30M(512M) 4.667ms
[0.076s][info][gc       ] GC(2) Pause Young (Normal) (G1 Evacuation Pause) 71M->43M(512M) 5.828ms
[0.247s][info][gc       ] GC(3) Pause Young (Normal) (G1 Evacuation Pause) 344M->53M(512M) 5.730ms
[0.302s][info][gc       ] GC(4) Pause Young (Normal) (G1 Evacuation Pause) 343M->53M(512M) 3.512ms
[0.358s][info][gc       ] GC(5) Pause Young (Normal) (G1 Evacuation Pause) 343M->53M(512M) 3.795ms
[0.416s][info][gc       ] GC(6) Pause Young (Normal) (G1 Evacuation Pause) 343M->54M(512M) 3.546ms
[0.475s][info][gc       ] GC(7) Pause Young (Normal) (G1 Evacuation Pause) 344M->53M(512M) 3.372ms
```

Паузы ZGC — `gcpauses-zgc.txt` (дословный фрагмент, 15 пауз в 5 циклах):

```
[0.235s][info][gc,phases] GC(0) Pause Mark Start 0.007ms
[0.241s][info][gc,phases] GC(0) Pause Mark End 0.006ms
[0.243s][info][gc,phases] GC(0) Pause Relocate Start 0.003ms
[0.336s][info][gc,phases] GC(1) Pause Mark Start 0.006ms
[0.340s][info][gc,phases] GC(1) Pause Mark End 0.006ms
[0.342s][info][gc,phases] GC(1) Pause Relocate Start 0.004ms
[0.436s][info][gc,phases] GC(2) Pause Mark Start 0.007ms
[0.440s][info][gc,phases] GC(2) Pause Mark End 0.007ms
[0.442s][info][gc,phases] GC(2) Pause Relocate Start 0.003ms
[0.536s][info][gc,phases] GC(3) Pause Mark Start 0.020ms
[0.540s][info][gc,phases] GC(3) Pause Mark End 0.006ms
[0.542s][info][gc,phases] GC(3) Pause Relocate Start 0.004ms
[0.636s][info][gc,phases] GC(4) Pause Mark Start 0.005ms
[0.640s][info][gc,phases] GC(4) Pause Mark End 0.006ms
[0.641s][info][gc,phases] GC(4) Pause Relocate Start 0.004ms
```

**Вывод (для этого прогона).** G1 останавливает мир на 3.3–6.9 мс на каждую
эвакуацию молодого поколения — вся работа ЭТОЙ young-эвакуации идёт под паузой
(но у G1 есть и конкурентные фазы, например маркировка старого поколения). ZGC (в JDK 21
`-XX:+UseZGC` — непоколенческий) делает основную работу конкурентно, оставляя
только три коротких stop-the-world рубежа на цикл по ~0.003–0.020 мс. Разница
пауз в этом прогоне — около трёх порядков. Это компромисс сборщиков (throughput
vs latency), а не «Java такая»: у G1 целевая пауза настраивается
(`-XX:MaxGCPauseMillis`, дефолт 200 мс — цель, не потолок), а абсолютные числа
привязаны к машине, heap 512 МБ и данному ворклоаду. Устойчиво лишь направление:
у pause-ориентированных сборщиков STW-окна много меньше.

---

## Файлы

- `pom.xml` — JDK 21 (`release 21`), зависимость `jol-core:0.17`.
- `src/main/java/tech/khorost/mm/Cycle.java` — опора 1.
- `src/main/java/tech/khorost/mm/Layout.java` — опора 2.
- `src/main/java/tech/khorost/mm/GcWorkload.java` — опора 3 (ворклоад).
- `gcpauses-g1.txt`, `gcpauses-zgc.txt` — сырые GC-логи прогона опоры 3.
- `build.sh`, `run.sh` — обёртки воспроизведения.
