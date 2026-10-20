# Python — примеры

Конкурентность и GIL, web-фреймворки, доступ к данным, упаковка и тестирование на Python.

| Стенд | Описание | Статья |
|---|---|---|
| [`concurrency/`](concurrency/) | GIL, asyncio, потоки и процессы: где GIL связывает счётную задачу, что даёт сборка без GIL и чем за неё платят; профилирование cProfile против py-spy | [статья](https://khorost.tech/python/python-asyncio-and-gil/) |
| [`data-access/`](data-access/) | SQLAlchemy 2.0 против голого asyncpg: сколько стоит каждый слой доступа и когда эта цена перестаёт существовать; N+1 в запросах, а не в миллисекундах; что видит автогенерация Alembic и возвращает ли откат схему назад | [статья](https://khorost.tech/python/python-data-access-sqlalchemy/) |
| [`tooling/`](tooling/) | uv, Poetry и pip на одном наборе зависимостей в трёх состояниях кэша; шесть стратегий сборки образа при побайтово одинаковом ответе; кто ловит подсунутый дефект — моки, фейк на SQLite, настоящая база в контейнере и property-based тест | [статья](https://khorost.tech/python/python-packaging-uv-poetry/) |
| [`typing-and-data/`](typing-and-data/) | Кто ловит подсунутый дефект — mypy, mypy --strict, pyright, pydantic на границе и голое выполнение; и кто не полез в данные, которые ответу не нужны: лишние разделы Parquet сделаны нечитаемыми, дошедший до ответа тем самым доказал, что в них не заглядывал. Времени этот стенд не меряет вовсе | [статья](https://khorost.tech/python/python-modern-typing/) |
| [`web/`](web/) | FastAPI, Django и Flask на побайтово одинаковых ответах: во что обходится сам фреймворк и когда это перестаёт быть заметно; воркеры, корректное завершение, отказ на старте | [статья](https://khorost.tech/python/python-web-frameworks/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
