# Сжатие данных — стенд

Живой стенд к статье о сжатии данных на [khorost.tech](https://khorost.tech).
Даёт числа для сравнения кодеков сжатия на общем корпусе и на нескольких
профилях раскладки данных: построчная JSON-выгрузка (NDJSON), поток мелких
однотипных событий и таблица в двух раскладках (построчной и колоночной).

**Фаза 1 завершена.** Датасет и профили (`dataset/`), харнесс замера с
адаптивной калибровкой числа проходов (`codecs/measure.go`), шесть
сценариев кодеков (`codecs/`: `matrix`, `zstd-impl`, `dictionary`,
`columnar`, `incompressible`, `http-transport` в паре с `httpdemo/`),
модуль расчётов (`analysis/`) и статический гейт. Все живые числа сведены в
[`FIXTURES.md`](./FIXTURES.md) — единственный источник цифр для статей
серии; манифест сырых данных — [`evidence/README.md`](./evidence/README.md).

## Тулчейн и версии

- Go `1.26.3` (`windows/amd64` на этой машине; `go.mod` пинит `go 1.26.3`)
- `GOPROXY=https://go.khorost.tech,direct` — собственный кеширующий прокси
- PostgreSQL `18.4` — образ для `compose/origin.yml` (заготовка на будущее,
  Task 1 БД не поднимает и не проверяет — корпус генерируется детерминированно
  в Go, без обращения к БД)

## Датасет

`dataset/` — детерминированный генератор корпуса `products`, дословная копия
алгоритма стенда `performance/inmemory/dataset` (см. комментарий в
`dataset/gen.go`). Копия намеренная: модуль `inmemory` объявлен как
`package main` и не может быть импортирован напрямую, а `replace`-директива
означала бы, что правка одного стенда молча меняет числа другого. Расхождение
копий ловится тестом `TestProductsJSONDeterministic` и гейтом
`ops/verify-static.sh`.

Параметры по умолчанию: `seed=42`, `products=200_000`, `users=5_000`,
`viewsPerUser=20` (последние два не используются генерацией `Product`,
сохранены для совместимости сигнатуры с `inmemory`).

### Профили

- `products.ndjson` — построчный NDJSON всего корпуса.
- `events-small.ndjson` — `-events` (по умолчанию 200 000) мелких событий
  одного JSON-шаблона (`product_viewed`) — профиль для словарного сжатия.
- `table-row.bin` — та же таблица, построчная раскладка (`\t`-разделитель).
- `table-col.bin` — та же таблица, колоночная раскладка (те же значения,
  сгруппированные по полю) — изолирует эффект раскладки от эффекта кодека.

### Генерация профилей

```bash
cd performance/compression/dataset
export GOPROXY=https://go.khorost.tech,direct
go test ./...
go run . -out ../profiles
```

Фактический прогон (эта машина, `go1.26.3 windows/amd64`):

```
products.ndjson: 36493651 байт
events-small.ndjson: 20621183 байт
table-row.bin: 11203432 байт
table-col.bin: 11203432 байт
```

Контрольная сумма корпуса (`TestProductsJSONDeterministic`,
`sha256(products.ndjson)`):

```
1f73c2ea439c043b8ead2dc3daee85b2ac2f1b7080587e76d81ff6830f83d65d
```

размер — `36493651` байт. Значение совпадает с контрольной суммой корпуса
стенда `performance/inmemory` на тех же параметрах (`seed=42, products=200000,
users=5000, viewsPerUser=20`) — прямое подтверждение, что копия генератора
не разошлась с оригиналом.

## Источник (заготовка, Task 1 не проверяет)

`compose/origin.yml` — `postgres:18.4`, порт `15434:5432` (не конфликтует с
`inmemory` — `15432`/`15433`). Понадобится последующим задачам серии; в этой
задаче не поднимается.

```bash
cd performance/compression
docker compose -f compose/origin.yml up -d
docker exec compression-origin pg_isready -U postgres -d shop
```

## Статический гейт

```bash
cd performance/compression
./ops/verify-static.sh; echo "RC=$?"
```

Проверяет: `go vet` по всем модулям стенда (`dataset`, и последующим
`codecs`/`analysis`/`httpdemo`, когда появятся), `go test` по модулям с
тестами, отсутствие `image:...:latest` в `compose/`, exec-бит на
`ops/*.sh`, и что контрольная сумма корпуса не разошлась с зафиксированным
значением (`1f73c2ea439c043b8ead2dc3daee85b2ac2f1b7080587e76d81ff6830f83d65d`).

Фактический прогон на этой машине:

```
== go vet по всем модулям
== go test по модулям с тестами
== запрет 'latest' в compose
== exec-бит на ops/*.sh
== корпус не разошёлся со стендом inmemory
RC=0
```

## Железо (для будущих замеров скорости кодеков)

Снято командой `Get-CimInstance Win32_Processor` / `Win32_ComputerSystem`
(PowerShell, `lscpu` на Windows недоступен):

```
Name                      : AMD Ryzen 7 5800X3D 8-Core Processor
NumberOfCores             : 8
NumberOfLogicalProcessors : 16

TotalRAM_GB : 127.91
```

## Сценарии кодеков

Все команды ниже — из `performance/compression`. `codecs`, `httpdemo`
тянут cgo-зависимости (`DataDog/zstd`, `valyala/gozstd`) — на этой машине
без `gcc` собираются и запускаются только в контейнере:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/w -w /w/codecs \
  -e GOPROXY=https://go.khorost.tech,direct golang:1.26.3 \
  go build -o codecs-linux .
```

Бинарник собирается один раз, до начала замеров — компиляция не должна
попадать в окно измерения. Дальше каждый сценарий:

```bash
# matrix — 12 кодек-уровней на общем корпусе, голый хост (не контейнер)
cd codecs && go build -o codecs.exe . && ./codecs.exe -scenario matrix -profiles ../profiles -out ../scratchout/matrix-run1

# zstd-impl / dictionary / columnar / incompressible — в контейнере
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/w -w /w \
  -e GOPROXY=https://go.khorost.tech,direct golang:1.26.3 \
  ./codecs-linux -scenario zstd-impl -profiles profiles -out scratchout/zstd-impl-run1 -reps 7

# http-transport — два контейнера, живой сетевой путь
docker compose -f compose/httpdemo.yml up -d
./ops/bandwidth-sweep.sh   # публикует scratchout/http-transport.csv по всем 8 полосам
docker compose -f compose/httpdemo.yml down
```

Ожидаемый вывод — stderr со строками `кодек ... байт ratio=...` (размеры
детерминированы, см. `FIXTURES.md`) и CSV-файлы `<scenario>.csv`/
`<scenario>-reps.csv` в каталоге `-out`. Логи и сведения по каждому
сценарию, фактически опубликованные в этой фазе, — `evidence/README.md`.

**Методика прогревочного прогона** (см. `FIXTURES.md`, раздел «Границы
метода»): первый прогон каждой серии (`matrix`, `zstd-impl`,
`incompressible`) отбрасывается как прогревочный, публикуются только
зачётные. `scratchout/` — рабочий каталог прогонов (маркеры, сырые логи,
CSV каждого отдельного прогона, включая прогревочные и отладочные) — **в
git не входит** (`.gitignore`: `scratchout*/`), только сведённый результат
в `evidence/` отслеживается репозиторием.

## Структура каталогов

```
performance/compression/
  dataset/    генератор корпуса и профилей
  codecs/     6 сценариев кодеков (matrix, zstd-impl, dictionary, columnar,
              incompressible) — cgo-зависимости, только в контейнере
  analysis/   сведение результатов замеров (формула порога, стоимость)
  httpdemo/   сервер+клиент для сценария http-transport
  compose/    origin.yml (заготовка), httpdemo.yml
  ops/        verify-static.sh (статический гейт), bandwidth-sweep.sh
  evidence/   сведённые CSV и представительные логи — коммитятся явными
              исключениями из .gitignore (см. evidence/README.md)
  scratchout*/  рабочие каталоги прогонов — НЕ коммитятся
  FIXTURES.md   единственный источник чисел для статей серии
```
