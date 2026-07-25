# build-packaging

Стенд к статье #7 серии про JVM/Java: **один и тот же простой сервис**,
собранный и упакованный **пятью способами**, с реальными числами по
**startup / peak RSS / размеру образа**. Данные для SVG #7.

## Сервис

Plain-Java HTTP-сервис на `com.sun.net.httpserver` (JDK-встроенный,
модуль `jdk.httpserver`) — `GET /` отдаёт «hello», `GET /health` отдаёт
`OK`, в лог пишется маркер `Started build-packaging service…`. **Без единой
внешней зависимости** (только `java.base` + `jdk.httpserver`).

Почему не Spring Boot: осознанный выбор в рамках стенда. GraalVM native для
Spring Boot требует reflection-конфигов (`reflect-config.json` и компания) и
AOT-обработки; для стенда, где цель — честно сравнить startup/RSS/size пяти
режимов, это лишний шум и риск не собрать native вовсе. Plain-Java снимает
эту проблему: `native-image --no-fallback` компилируется без единого
reflect-config. Тезис статьи (native стартует за десятки мс, ест меньше
памяти) от стека сервиса не зависит.

## Пять режимов

| Режим | Dockerfile | Что демонстрирует |
|---|---|---|
| **fat** — JVM fat-jar | `Dockerfile.fat` | Базовый случай: обычный executable jar на стоковом `temurin:25-jre` |
| **layered** — JVM layered | `Dockerfile.layered` | Разложение по слоям Docker (редко меняющийся `lib/` + часто меняющийся `app/`). **Про кеш слоёв, НЕ про startup** — байткод тот же, что у fat |
| **appcds** — JVM + AppCDS | `Dockerfile.appcds` | Application Class-Data Sharing: тренировочный прогон на этапе сборки образа дампит архив классов → быстрее старт JVM |
| **native** — GraalVM native-image | `Dockerfile.native` | Нативный бинарник (AOT), без JVM в рантайме |
| **jlink** — custom runtime | `Dockerfile.jlink` | Урезанный JRE только из нужных JDK-модулей (`jdeps` → `jlink`) → меньше рантайм-образ |

## Тулчейн

- Сборка jar: `maven:3.9-eclipse-temurin-25` (хостового Maven нет).
- JVM-рантаймы: `eclipse-temurin:25-jre` (fat/layered/appcds).
- **GraalVM native-image: `ghcr.io/graalvm/native-image-community:25`**
  (GraalVM CE 25.0.2+10.1, JVMCI JDK 25.0.2). Тянется с ghcr.io без прокси,
  native-image для JDK 25 работает из коробки — fallback на Liberica NIK/24
  НЕ понадобился.
- jlink-рантайм и native-рантайм — на `debian:bookworm-slim` (native-бинарник
  динамически слинкован с glibc, `scratch`/`distroless-static` не подходит).

## Сборка jar

Хостового Maven нет — через Docker-образ Maven с JDK 25:

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl build-packaging -am package
```

## Прогон замеров

`bench.sh` собирает все 5 образов и снимает startup/RSS/size:

```bash
cd build-packaging
bash bench.sh 5     # 5 прогонов на режим
```

**Методика (важно).** startup и RSS меряются **внутри контейнера**
(`bench-inside.sh`), без публикации порта на хост. Причина — port-forwarder
Docker Desktop на Windows добавляет к первому коннекту случайные 20–80 секунд,
что полностью топит startup-числа (JVM стартует за сотни мс, а мерилось бы
«87 c»). Опрос `/health` и чтение `VmHWM` перенесены внутрь контейнера — это
убирает артефакт хоста. Размер образа меряется снаружи (`docker image
inspect`, несжатый размер со слоями).

- **startup (мс)** — от старта процесса до первого `200 OK` на `GET /health`
  (опрос из того же контейнера, `bash /dev/tcp`).
- **peak RSS (МБ)** — `VmHWM` процесса сервиса из `/proc/$PID/status`
  (историчный пик резидентной памяти — та же метрика, что в `../concurrency`).
- **размер (МБ)** — несжатый размер образа (`docker image inspect`).

## Результаты (характерный прогон)

Docker Desktop на Windows, 16 vCPU. **Числа host-зависимы** — важен порядок
величин и относительное сравнение внутри одного прогона, не абсолютные цифры.
Startup/RSS — 5 прогонов на режим.

| Режим | startup (мс) | peak RSS (МБ) | размер образа (МБ) |
|---|---:|---:|---:|
| fat (JVM fat-jar) | 131–178 (~150) | ~65 | 111.4 |
| layered (JVM) | 131–145 (~139) | ~65 | 111.4 |
| appcds (JVM+AppCDS) | 109–126 (~118) | ~64 | 111.8 |
| **native (GraalVM)** | **9–17 (~12)** | **~16** | **32.1** |
| jlink (custom runtime) | 297–356 (~320) | ~68 | 48.7 |

Сырой прогон — см. ниже.

<details>
<summary>Сырой вывод bench.sh (5 прогонов на режим)</summary>

```
fat run 1..5:     startup_ms= 177 131 178 137 142   peak_rss ~65 MB
layered run 1..5: startup_ms= 141 139 131 141 145   peak_rss ~65 MB
appcds run 1..5:  startup_ms= 123 126 119 113 109   peak_rss ~64 MB
native run 1..5:  startup_ms=  11  10   9  17  12    peak_rss ~16 MB
jlink run 1..5:   startup_ms= 325 320 356 297 299   peak_rss ~68 MB

размеры образов:
  fat     111.4 MB
  layered 111.4 MB
  appcds  111.8 MB
  native   32.1 MB
  jlink    48.7 MB
```

</details>

## Что подтвердилось, что нет

- **native стартует в разы быстрее и ест меньше памяти — подтвердилось,
  ярко.** ~12 мс против ~150 мс у fat-jar (в ~12 раз быстрее) и ~16 МБ RSS
  против ~65 МБ (в ~4 раза меньше). Плюс самый маленький образ (32 МБ). Это
  главный ассерт стенда, и он выполнен с запасом.

- **AppCDS ускоряет JVM-startup — подтвердилось, умеренно.** ~118 мс против
  ~150 мс у fat-jar (≈20% быстрее) при том же RSS и почти том же размере
  (архив классов ~1.5 МБ добавляется в образ). Эффект реальный, но скромный —
  сервис крошечный (два класса + `jdk.httpserver`), а AppCDS тем ценнее, чем
  больше классов грузит приложение. На «толстом» Spring Boot выигрыш был бы
  заметнее.

- **layered — про кеш слоёв Docker, не про startup — подтвердилось.**
  startup и RSS у layered неотличимы от fat (~139 vs ~150 мс, тот же ~65 МБ) —
  и не должны отличаться: исполняется тот же байткод в том же JRE. Смысл
  режима — при правке только `app/`-кода пересобирается/перекачивается один
  слой, а не весь jar. Размер образа тот же (111 МБ).

- **jlink уменьшает рантайм-образ — подтвердилось (48.7 vs 111 МБ, в ~2.3
  раза меньше). НО startup неожиданно вырос — ~320 мс против ~150 у fat.**
  Разгадка (продиагностировано, не догадка): стоковый `temurin:25-jre`
  везёт **дефолтный CDS-архив** `lib/server/classes.jsa` (~14.5 МБ), который
  fat/layered/appcds используют автоматически и стартуют быстрее. `jlink` по
  умолчанию **не** генерирует CDS-архив для custom runtime — его там нет
  (проверено `-Xlog:cds`: «Specified shared archive file not found»), плюс
  модули сжаты `--compress zip-6` (декомпрессия при загрузке классов). Итог:
  jlink жмёт образ, но теряет CDS и потому стартует медленнее «жирного» JRE.
  Честный вывод для статьи: **jlink сам по себе — про размер, не про
  скорость**; чтобы получить и то и другое, нужно к jlink-руктайму добавить
  CDS (`jlink --generate-cds-archive`, доступно с JDK 24+, или отдельный
  AppCDS-архив как в режиме appcds). Здесь оставлен «наивный» jlink без CDS
  как поучительный контрпример — не подгоняя число под ожидание.

## Файлы

```
build-packaging/
  pom.xml                 # под-модуль parent POM, jar с Main-Class
  src/main/java/tech/khorost/buildpackaging/
    lib/ResponseFormatter.java   # редко меняющийся код (для layered-слоя)
    app/App.java                  # HTTP-сервис (часто меняющийся код)
  Dockerfile.fat          # режим 1
  Dockerfile.layered      # режим 2
  Dockerfile.appcds       # режим 3 (training-run на этапе сборки)
  Dockerfile.native       # режим 4 (GraalVM native-image)
  Dockerfile.jlink        # режим 5 (jdeps → jlink custom runtime)
  bench.sh                # сборка 5 образов + замер startup/RSS/size
  bench-inside.sh         # замер внутри контейнера (обходит port-forwarder)
```
