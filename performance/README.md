# Performance — примеры

Высокая нагрузка и низкая задержка, вероятностные структуры данных.

| Стенд | Описание | Статья |
|---|---|---|
| [`crypto-rsa-regression/`](crypto-rsa-regression/) | Регрессия crypto/rsa в Go 1.20: одинаковые бенчмарки на шести версиях Go, просадка публичных verify/encrypt в 5–7 раз, benchstat и CI-гейт | [статья](https://khorost.tech/performance/go-crypto-rsa-regression/) |
| [`highload-lowlatency/`](highload-lowlatency/) | Highload под SLA < 300 мс: HAProxy L7 (h2c) + пул Go/Java-бэкендов, L4 vs L7 | [статья](https://khorost.tech/performance/latency-budget-and-transport/) |
| [`probabilistic/`](probabilistic/) | Вероятностные структуры: Bloom и родственники | [статья](https://khorost.tech/performance/bloom-filters-probabilistic-structures/) |
| [`testcontainers-template-db/`](testcontainers-template-db/) | Шаблонная база вместо контейнера на каждый тест: CREATE DATABASE ... TEMPLATE, замеры ×10 и ×36, границы приёма (права, FORCE, размер шаблона) | 🔜 скоро |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
