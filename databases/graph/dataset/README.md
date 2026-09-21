# dataset — генератор графа платформы

Детерминированный генератор одного графа, который грузится в три угла стенда:
нативный Neo4j, Apache AGE (граф поверх PostgreSQL) и обычные реляционные таблицы
(honest baseline на рекурсивных CTE). Один и тот же `--seed` даёт побайтово одинаковый
граф — на этом держится воспроизводимость замеров.

## Модель графа

Узлы: `User`, `Team`, `Role`, `Resource`, `Service`, `Package` (id глобально уникальны).
Рёбра: `MEMBER_OF` (user→team), `HAS_ROLE` (user/team→role), `GRANTS` (role→resource),
`COLLABORATES` (user↔user, каноническая форма `a<b`), `DEPENDS_ON` (service→service и
package→package), `OWNS` (team→service), `INTERACTS` (user→resource).

Один граф покрывает паттерны всех четырёх доменов:

| Домен | Паттерн обхода |
|---|---|
| Dependency graph | reachability / impact по `DEPENDS_ON`, обнаружение циклов |
| Access graph | propagation прав `MEMBER_OF`→`HAS_ROLE`→`GRANTS` |
| Соцграф / рекомендации | shortest path и рекомендации по `COLLABORATES`/`INTERACTS` |
| Fraud | кольца в `COLLABORATES` |

Гарантии структуры (для стабильных demo/бенчей): намеренный цикл среди первых трёх
сервисов и пакетов, хаб-ресурс (`id = ResLo`) с высоким fan-out, простая цепочка
`COLLABORATES` длиной `--depth`.

## Запуск

```bash
go run . --seed 42 --out ./out          # дефолтный масштаб (users=2000, ...)
go run . --users 200 --teams 20 --roles 30 --resources 80 \
         --services 40 --packages 120 --fanout 5 --depth 6 --seed 42 --out ./out
```

Выход в `out/`: `relational.sql` (COPY-блоки), `neo4j.cypher` (для `cypher-shell`),
`age.sql` (для `psql`). Загрузка — см. корневой [README](../README.md).

## Тесты

```bash
go test ./...
```

Проверяют детерминированность (одинаковый seed → одинаковый checksum), влияние seed
и структурные гарантии (цикл, хаб).
