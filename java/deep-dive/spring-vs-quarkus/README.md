# spring-vs-quarkus

Стенд к статье #4 серии про JVM/Java: **два эквивалентных REST-сервиса** —
Spring Boot 3.5.3 и Quarkus 3.22.3 — с реальными числами по **startup /
peak RSS** в JVM-режиме. Данные для SVG #4.

## Сервисы

Оба приложения делают одно и то же: `GET /hello` отдаёт `hello`,
`GET /health` отдаёт health-статус фреймворка.

| | Spring Boot | Quarkus |
|---|---|---|
| Версия | 3.5.3 | 3.22.3 |
| REST | `spring-boot-starter-web` (Tomcat) | `quarkus-rest` (JAX-RS, Vert.x) |
| Health | `spring-boot-starter-actuator`, ремаппнут `management.endpoints.web.base-path=/` (`/actuator/health` → `/health`) | `quarkus-smallrye-health`, ремаппнут `quarkus.smallrye-health.root-path=/health` (`/q/health` → `/health`) |
| Класс | `spring/.../App.java` | `quarkus/.../HelloResource.java` |

Health вынесен на один и тот же путь `/health` у обеих сторон намеренно —
`bench-inside.sh` опрашивает единый путь одним и тем же кодом для честного
сравнения.

## Тулчейн

- Сборка: `maven:3.9-eclipse-temurin-25` (хостового Maven нет).
- JVM-рантайм у обоих: **один и тот же** `eclipse-temurin:25-jre` — разница
  в startup/RSS ниже приходится только на фреймворк, не на базовый образ.
- Версии Spring Boot / Quarkus наследуются из `dependencyManagement`
  родителя (`java-deep-dive/pom.xml`): `spring-boot.version=3.5.3`,
  `quarkus.version=3.22.3`.

## Сборка

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl spring-vs-quarkus/spring,spring-vs-quarkus/quarkus -am package
```

Spring даёт fat-jar `spring/target/app.jar` (spring-boot-maven-plugin
`repackage`). Quarkus даёт fast-jar раскладку
`quarkus/target/quarkus-app/{quarkus-run.jar,lib,app,quarkus}` (дефолт
Quarkus Maven-плагина).

## Прогон замеров

```bash
cd spring-vs-quarkus
bash bench.sh 5     # 5 прогонов на фреймворк
```

**Методика (важно, как в `../build-packaging`).** startup и RSS меряются
**внутри контейнера** (`bench-inside.sh`), без публикации порта на хост.
Причина — port-forwarder Docker Desktop на Windows добавляет к первому
коннекту случайные 20–80 секунд, что полностью топит startup-числа. Опрос
`/health` и чтение `VmHWM` перенесены внутрь контейнера.

- **startup (мс)** — от старта процесса до первого `200 OK` на `GET /health`
  (опрос из того же контейнера, `bash /dev/tcp`).
- **peak RSS (МБ)** — `VmHWM` процесса сервиса из `/proc/$PID/status`
  (историчный пик резидентной памяти — та же метрика, что в
  `../concurrency` и `../build-packaging`).
- `GET /hello` проверен вручную у обеих сторон (200 OK, тело `hello`) —
  сервисы функционально эквивалентны, замер честный.

## Результаты (характерный прогон)

Docker Desktop на Windows, 16 vCPU, JDK 25 (`eclipse-temurin:25-jre`
у обоих). **Числа host-зависимы** — важен порядок величин и относительное
сравнение, не абсолютные цифры. 10 прогонов на фреймворк (канонический
`bench.sh 5` + 5 дополнительных подтверждающих прогонов каждой стороны).

| Фреймворк | startup (мс) | peak RSS (МБ) | размер образа (МБ) |
|---|---:|---:|---:|
| **Spring Boot 3.5.3 (JVM)** | 2830–3858 (~3350, 1 выброс 6206 исключён) | 306–344 (~326) | 131.5 |
| **Quarkus 3.22.3 (JVM)** | 1168–1442 (~1270, 1 выброс 4019 исключён) | 140–142 (~141) | 125.1 |

RSS-диапазон снят только по 5 каноническим прогонам (`bench.sh 5`, сырой
вывод ниже) — 5 подтверждающих прогонов фиксировали только startup, без
RSS-колонки (см. следующий блок), поэтому в RSS-диапазон и среднее они не
входят.

Quarkus стартует в **~2.6 раза быстрее** и держит **~2.3 раза меньше** RSS,
чем Spring Boot, в JVM-режиме. Оба выброса (Spring 6206 мс, Quarkus 4019 мс)
воспроизведены по одному разу каждый в отдельных сериях прогонов и не
повторились в 5 дополнительных подтверждающих запусках той же стороны —
похоже на разовый шум планировщика Docker Desktop / хоста, а не системную
особенность фреймворка. Для Quarkus это подтверждается и по памяти: RSS
именно этого выброса (142.2 МБ) остаётся в обычном диапазоне стороны —
аномалия только в timing. Для Spring-выброса (6206 мс) RSS отдельно не
снимался — он попал в подтверждающий блок, где пишется только startup,
поэтому по памяти для этого конкретного прогона утверждать нечего.

<details>
<summary>Сырой вывод — канонический прогон bench.sh 5</summary>

```
spring run 1: startup_ms=3062 peak_rss_kb=337568 (~329.7 MB)
spring run 2: startup_ms=3152 peak_rss_kb=325356 (~317.7 MB)
spring run 3: startup_ms=2830 peak_rss_kb=313532 (~306.2 MB)
spring run 4: startup_ms=3858 peak_rss_kb=352000 (~343.8 MB)
spring run 5: startup_ms=3835 peak_rss_kb=338880 (~330.9 MB)
quarkus run 1: startup_ms=1442 peak_rss_kb=143444 (~140.1 MB)
quarkus run 2: startup_ms=1334 peak_rss_kb=144412 (~141.0 MB)
quarkus run 3: startup_ms=1269 peak_rss_kb=144088 (~140.7 MB)
quarkus run 4: startup_ms=1330 peak_rss_kb=143848 (~140.5 MB)
quarkus run 5: startup_ms=4019 peak_rss_kb=145664 (~142.2 MB)   <- выброс, см. ниже

jdd-svq-spring:  131.5 MB (137926616 bytes)
jdd-svq-quarkus: 125.1 MB (131203459 bytes)
```

</details>

<details>
<summary>Сырой вывод — 5 подтверждающих прогонов каждой стороны</summary>

```
spring:  3413  3465  6206*  3467  3506     (* выброс)
quarkus: 1256  1168  1243   1283  1271
```

Только startup — этот блок гонялся отдельно, RSS для него не снимался и не
публикуется. Все peak RSS числа в этом README — из канонического блока
`bench.sh 5` выше (5 прогонов на сторону).

</details>

## Сопоставление с таблицей вводной статьи (#1)

Таблица #1 давала «на пальцах»: Spring Boot JVM 2–5 с / 200–400 МБ, Quarkus
JVM 0.8–1.5 с / 80–150 МБ.

- **Spring Boot: startup ~3.35 с, RSS ~326 МБ — оба внутри диапазона #1.**
- **Quarkus: startup ~1.27 с — внутри диапазона #1.** RSS ~141 МБ — тоже
  внутри диапазона (80–150), но у верхней границы: сервис хоть и минимальный,
  но REST + health уже тянут Vert.x/Netty и SmallRye — RSS ожидаемо не у
  нижней границы диапазона (та скорее для совсем голого Quarkus без
  REST-стека).
- Порядок величины подтверждён по обеим метрикам у обеих сторон — таблица
  #1 не подгонялась, числа реальные с этого стенда.

## Native — не в этом прогоне

Native-сборка (GraalVM `mvn -Pnative`) **сознательно не сделана в этом
прогоне** — по явному допущению в рамках стенда («не застревай в native»,
JVM-числа — приоритетные данные для SVG #4). Причины пропуска:

- **Quarkus native** технически дешевле (`quarkus.native.container-build=true`,
  без локального GraalVM), но добавляет ещё один build ~3–8 минут поверх
  уже собранного JVM-стенда — предельная ценность для тезиса статьи #4
  (Spring vs Quarkus **в JVM-режиме**) невелика: native-эффект (десятки мс
  startup, единицы-десятки МБ RSS) уже наглядно показан на этом же
  тулчейне в `../build-packaging` (Dockerfile.native, режим `native`:
  ~12 мс / ~16 МБ у GraalVM native-image).
- **Spring Boot native** заметно тяжелее: требует AOT-обработки
  (`spring-boot:process-aot`) и либо buildpacks (`pack`/`paketobuildpacks`,
  не установлен в этом окружении), либо ручной GraalVM native-image plugin
  с risk по reflection-конфигам для `spring-boot-starter-web` +
  `actuator` — на порядок больше время и риск не собраться вовсе, чем
  ценность для JVM-ориентированного тезиса этой статьи.

Если для статьи понадобится качественная реплика «Quarkus native ещё
быстрее» — тезис можно подкрепить ссылкой на уже измеренный native-режим
в `../build-packaging` без повторного стенда.

## Файлы

```
spring-vs-quarkus/
  pom.xml                  # aggregator (packaging=pom), под-модуль java-deep-dive
  spring/
    pom.xml                  # spring-boot-starter-web + actuator, repackage -> app.jar
    src/main/java/tech/khorost/springvsquarkus/spring/App.java
    src/main/resources/application.properties   # health -> /health
    Dockerfile
  quarkus/
    pom.xml                  # quarkus-rest + smallrye-health
    src/main/java/tech/khorost/springvsquarkus/quarkus/HelloResource.java
    src/main/resources/application.properties   # health -> /health
    Dockerfile
  bench.sh                 # сборка 2 образов + замер startup/RSS/size
  bench-inside.sh          # замер внутри контейнера (обходит port-forwarder)
```
