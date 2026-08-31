# Go — примеры

Язык Go вглубь: конкурентность, память, дженерики, net/http, slog, итераторы, рефлексия, ассемблер, ORM.

| Стенд | Описание | Статья |
|---|---|---|
| [`asm/`](asm/) | Go assembly: dot product на AVX2/NEON, avo, ускорение ×3.88 | [статья](https://khorost.tech/go/) |
| [`concurrency/`](concurrency/) | Конкурентность в Go: горутины/каналы, sync, модель памяти, паттерны, отладка гонок | [статья](https://khorost.tech/go/go-concurrency-goroutines-channels/) |
| [`context/`](context/) | Контекст в Go: отмена, дедлайны, WithCancelCause, request-scoped values | [статья](https://khorost.tech/go/go-context/) |
| [`fundamentals/`](fundamentals/) | Основы Go вглубь: ошибки, интерфейсы, слайсы/карты, методы/ресиверы | [статья](https://khorost.tech/go/go-interfaces/) |
| [`goroutine-leak-profile/`](goroutine-leak-profile/) | Пять классов утечек горутин и инструменты диагностики: pprof/goroutine, профиль goroutineleak из Go 1.27 и goleak; трейсбеки с pprof-метками | 🔜 скоро |
| [`iterators/`](iterators/) | Итераторы Go 1.23: iter.Seq/Seq2, iter.Pull, cleanup при break, бенчи | 🔜 скоро |
| [`memory/`](memory/) | Память Go: escape-анализ (go build -gcflags=-m), стек vs куча | [статья](https://khorost.tech/go/go-memory-stack-heap-escape/) |
| [`net-http/`](net-http/) | Сервисы на стандартном net/http: роутинг 1.22, middleware, таймауты, graceful shutdown | [статья](https://khorost.tech/go/go-net-http/) |
| [`orm-gorm-vs-jet/`](orm-gorm-vs-jet/) | ORM в Go: GORM vs go-jet | [статья](https://khorost.tech/go/go-orm-gorm-vs-go-jet/) |
| [`reflect/`](reflect/) | Цена рефлексии: три «закона», чтение/запись полей, теги, бенчи | [статья](https://khorost.tech/go/go-reflect/) |
| [`slog/`](slog/) | Структурированное логирование log/slog: хендлеры, группы, ContextHandler, бенчи | [статья](https://khorost.tech/go/go-slog/) |
| [`testing/`](testing/) | Тестирование в Go: ручные фейки, httptest, интеграционные тесты через testcontainers-go (Postgres + Redis), детектор гонок | [статья](https://khorost.tech/go/go-testing/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
