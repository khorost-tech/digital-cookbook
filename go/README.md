# Go — примеры

Язык Go вглубь: конкурентность, память, дженерики, net/http, slog, итераторы, рефлексия, ассемблер, ORM.

| Стенд | Описание | Статья |
|---|---|---|
| [`asm/`](asm/) | Go assembly: dot product на AVX2/NEON, avo, ускорение ×3.88 | [статья](https://khorost.tech/go/) |
| [`concurrency/`](concurrency/) | Конкурентность в Go: горутины/каналы, sync, модель памяти, паттерны, отладка гонок | [статья](https://khorost.tech/go/go-concurrency-goroutines-channels/) |
| [`context/`](context/) | Контекст в Go: отмена, дедлайны, WithCancelCause, request-scoped values | [статья](https://khorost.tech/go/go-context/) |
| [`net-http/`](net-http/) | Сервисы на стандартном net/http: роутинг 1.22, middleware, таймауты, graceful shutdown | 🔜 скоро |
| [`orm-gorm-vs-jet/`](orm-gorm-vs-jet/) | ORM в Go: GORM vs go-jet | [статья](https://khorost.tech/go/go-orm-gorm-vs-go-jet/) |
| [`reflect/`](reflect/) | Цена рефлексии: три «закона», чтение/запись полей, теги, бенчи | 🔜 скоро |
| [`slog/`](slog/) | Структурированное логирование log/slog: хендлеры, группы, ContextHandler, бенчи | [статья](https://khorost.tech/go/go-slog/) |
| [`testing/`](testing/) | Тестирование в Go: ручные фейки, httptest, интеграционные тесты через testcontainers-go (Postgres + Redis), детектор гонок | [статья](https://khorost.tech/go/go-testing/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
