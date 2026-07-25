# modern-features

Компилируемые сниппеты современных фич языка Java на **JDK 25** (образ
`maven:3.9-eclipse-temurin-25`, реально проверено `java -version` внутри
контейнера: `openjdk version "25.0.3" 2026-04-21 LTS`, `Temurin-25.0.3+9`).

Стенд к статье №2 «Современные фичи языка». Каждый файл — один `main`-класс на
фичу, без внешних зависимостей.

## Сборка и запуск

Хостового Maven нет — всё через Docker (см. общие правила репозитория):

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD":/app -v "$HOME/.m2":/root/.m2 -w /app \
  maven:3.9-eclipse-temurin-25 mvn -q -pl modern-features -am compile

MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD":/app -w /app \
  maven:3.9-eclipse-temurin-25 java -cp modern-features/target/classes \
  tech.khorost.modernfeatures.RecordsDemo
```

Замени `RecordsDemo` на любой из классов ниже.

## Статус фич на JDK 25 (проверено компиляцией и прогоном, не по памяти)

| Фича | Класс | Статус на 25 | С какого JDK | Флаг нужен? |
|---|---|---|---|---|
| records + compact constructors | `RecordsDemo` | finalized | JDK 16 (JEP 395) | нет |
| record patterns (instanceof/switch, вложенные) | `RecordsDemo` | finalized | JDK 21 (JEP 440, preview в 19/20) | нет |
| sealed interfaces/classes | `SealedDemo` | finalized | JDK 17 (JEP 409, preview в 15/16) | нет |
| exhaustive switch над sealed-иерархией | `SealedDemo` | finalized | JDK 21 (JEP 441) | нет |
| pattern matching for instanceof | `PatternMatchingDemo` | finalized | JDK 16 (JEP 394) | нет |
| pattern matching for switch (type patterns, `case null`, guarded `when`) | `PatternMatchingDemo` | finalized | JDK 21 (JEP 441, preview в 17–20) | нет |
| unnamed variables & patterns (`_`) | `UnnamedVariablesDemo` | finalized | JDK 22 (JEP 456, preview в 21 под JEP 443) | нет |
| virtual threads (`Executors.newVirtualThreadPerTaskExecutor`, `Thread.ofVirtual`) | `VirtualThreadsDemo` | finalized | JDK 21 (JEP 444, preview в 19/20) | нет |
| **string templates** (`STR."..."`) | `StringTemplatesStatus` | **REMOVED** | preview в 21 (JEP 430) и 22 (JEP 459, второй preview), снят из feature set в 23, отсутствует в 23/24/25 | н/д — не показан как рабочий код |

Ни одна из показанных фич не требует `--enable-preview` на JDK 25 — все либо
уже finalized (причём давно, все актуальные JEP, отобранные для стенда,
финализировались в диапазоне JDK 16–22, задолго до 25), либо (string templates) намеренно не
показаны как рабочий код, потому что синтаксис удалён.

### String templates — фактическая проверка удаления

Отдельно (вне этого модуля, чтобы не ронять сборку) прогнали через
`javac --release 25` файл со снятым синтаксисом:

```java
String s = STR."Hello \{name}";
```

Результат — дословный вывод компилятора:

```
StCheck.java:4: error: illegal escape character
        String s = STR."Hello \{name}";
                               ^
1 error
```

Важная деталь: компилятор 25 **вообще не распознаёт** `STR."..."` как
шаблонный процессор — он парсит это как обычный строковый литерал и
спотыкается на `\{` как на невалидном escape-символе. Никакого частичного
или деградировавшего распознавания снятого синтаксиса нет. Рабочие
альтернативы на 25 (без всякого preview) — `String.format`, `String#formatted`,
конкатенация `+`, text blocks (`"""..."""`, finalized в JDK 15) в сочетании с
`.formatted(...)` — все показаны в `StringTemplatesStatus.java`.

## Файлы

- `RecordsDemo.java` — records, compact constructor (валидация инварианта),
  record patterns в `instanceof` и `switch` (включая вложенную деконструкцию
  и guarded pattern `when`).
- `SealedDemo.java` — sealed interface (4 permits-наследника) и sealed
  abstract class (2 permits-наследника), exhaustive `switch` без `default`.
- `PatternMatchingDemo.java` — instanceof pattern, `switch` с type patterns,
  `case null`, guarded patterns (`when`).
- `UnnamedVariablesDemo.java` — `_` в catch-параметре, enhanced-for, record
  pattern, lambda-параметре, локальной переменной.
- `VirtualThreadsDemo.java` — `Executors.newVirtualThreadPerTaskExecutor()`
  (1000 задач через `invokeAll`) и `Thread.ofVirtual()` (точечный запуск).
- `StringTemplatesStatus.java` — история и фактический статус string
  templates на 25 (removed) + рабочие альтернативы.
