# go-asm — стенд к серии «Go assembly»

Сквозной пример: скалярное произведение `[]float64` в четырёх воплощениях.

| Каталог | Что | Статья |
|---|---|---|
| `dotprod/dot_generic_impl.go` | golden (чистый Go) | №1 |
| `reading/` | дизасм-листинги | №2 |
| `dotprod/dot_amd64.s` | рукописный AVX2 | №3 |
| `dotprod/dot_arm64.s` | рукописный NEON | №3 |
| `dotprod/avo/` | avo-генерация | №4 |

## Проверка

- amd64: `go test ./...` + `go test -bench . ./dotprod/` (числа реальны).
- arm64: `GOARCH=arm64 go build ./... && GOARCH=arm64 go vet ./...` (исполнение — на arm-железе).

## Бенчмарки и листинги (артефакты для статей)

Реальные цифры и дизасм-листинги уже сняты и лежат в репозитории —
использовать в статьях как есть, не перегенерировать без необходимости:

- `dotprod/dot_bench_test.go` — бенчмарки `dotGeneric` vs `dotAVX2` (amd64,
  n = 8/256/1024/65536).
- `reading/listings/bench.txt` — сырой вывод
  `go test -run '^$' -bench BenchmarkDot -benchmem -count 6 ./dotprod/`.
- `reading/listings/bench-benchstat.txt` — сводка `benchstat` по нему.
- `reading/listings/dotGeneric.txt` — `-S`-дизасм чистого Go (никакой
  автовекторизации, только `MULSD`/`ADDSD`).
- `reading/listings/dotAVX2_objdump.txt` — objdump рукописного AVX2
  (`VFMADD231PD`/`VFMADD231SD`). См. `reading/README.md` про известное
  ограничение `go tool objdump` с VEX FMA3 на этом хосте и как листинг
  чинится через Capstone.

Хост, на котором сняты числа: AMD Ryzen 7 5800X3D, Go 1.26.3, windows/amd64.
Пересобрать листинги/бенчи: `./reading/gen_listings.sh` и команды выше.

Серия статей: https://khorost.tech/go/ (series go-asm).
