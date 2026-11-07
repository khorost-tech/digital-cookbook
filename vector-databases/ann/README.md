# vector-databases/ann — алгоритмы ANN против точного перебора

Стенд к статье [ANN вглубь: HNSW, IVF, PQ](https://khorost.tech/databases/vector-ann-algorithms-deep/).
Алгоритмы — без СУБД, чтобы ручки были видны без обвязки движка.

Данные — общий датасет серии `vector-databases/dataset`: BEIR Quora, 522 931
документ, эмбеддинги `multilingual-e5-small` (384 измерения, нормированы).
Истина — точный перебор top-100 для 1000 тестовых запросов. Все recall — recall@10
по этим запросам.

| ось | что меряет | контроль |
|---|---|---|
| `brute` | время точного перебора от N | выдача = истина на 200 запросах |
| `hnsw` | своя HNSW: M, ef_construction, ef_search; память связей, недостижимые узлы | recall не убывает с ef |
| `ivf` | своя IVF-Flat: nlist, nprobe, нерепрезентативное обучение | nprobe = nlist → recall 1 |
| `quant` | SQ8 и PQ (m = 48, 96), пересчёт кандидатов по исходным векторам | размер кодов = расчёт |
| `metric` | cosine, L2, dot на нормированных и ненормированных векторах | — |
| `crosscheck` | те же параметры в Faiss 1.10.0; jvector 4.0.1 (Java) | — |

Кроме времени, `bench` считает **число вычисленных расстояний на запрос** — мера
работы, которая от железа не зависит. Поиск меряется в один поток, построение —
на всех ядрах.

## Запуск

```bash
(cd ../dataset && python3 scripts/make-dataset.py)   # один раз, ~70 мин
bash scripts/run-all.sh
```

Одна ось: `python3 scripts/run-hnsw.py`; один вариант без записи фикстуры:
`python3 scripts/run-hnsw.py m16-efc200`. Всё работает в контейнерах: Go 1.27,
Python с Faiss, Maven с Java 21 — по дайджестам из `images.json`.

## Устройство

- `go/vec` — расстояния, чтение датасета, recall, точный перебор;
- `go/hnsw` — HNSW (Malkov, Yashunin): слои, эвристика выбора соседей,
  параллельное построение, `Reachable()` — достижимость узлов;
- `go/ivf` — k-means (Ллойд) и IVF-Flat;
- `go/quant` — SQ8 и PQ с асимметричным скором (ADC) и пересчётом;
- `go/cmd/bench` — замеры, одна строка JSON на команду;
- `faiss/check.py` — те же конфигурации в Faiss;
- `java/` — jvector на тех же данных.

Гипотезы — в `expected.json`, записаны до прогона; пробы на FiQA, после которых
поменялся код, — там же, в `known_from_pre_run_probes`.

## Проверки

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/sabotage.py
```

Фикстуры — `fixtures/*.jsonl`, сводки сессий — `fixtures/manifests/`, маркер
полного прогона — `fixtures/RUN-COMPLETE`.
