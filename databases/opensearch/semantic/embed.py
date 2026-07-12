#!/usr/bin/env python3
"""Внешняя генерация эмбеддингов: sentence-transformers paraphrase-multilingual-MiniLM-L12-v2 (384-dim).

Считает эмбеддинги для всех документов articles-knn (title + body), записывает их в поле
`embedding`, затем эмбеддит поисковый запрос той же моделью и выполняет knn-запрос —
демонстрируя семантический поиск, который лексический match не находит.

Запуск (в контейнере sem-py):  python embed.py
Переменная OS_URL — адрес OpenSearch (по умолчанию http://sem-os:9200).
"""
import json
import os
import urllib.request

from sentence_transformers import SentenceTransformer

OS_URL = os.environ.get("OS_URL", "http://sem-os:9200")
MODEL = "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"   # 384-dim, мультиязычная (вкл. русский)
QUERY = "как быстрее добавлять документы пачками"   # match даёт НЕ ту статью (по слову «документы»), knn — bulk по смыслу


def os_request(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        f"{OS_URL}{path}", data=data, method=method,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def main():
    model = SentenceTransformer(MODEL)

    # 1. эмбеддим документы и записываем вектор в поле embedding
    docs = os_request("POST", "/articles-knn/_search",
                      {"size": 100, "query": {"match_all": {}}})["hits"]["hits"]
    lines = []
    for d in docs:
        text = d["_source"]["title"] + ". " + d["_source"]["body"]
        vec = model.encode(text, normalize_embeddings=True).tolist()
        lines.append(json.dumps({"update": {"_id": d["_id"]}}))
        lines.append(json.dumps({"doc": {"embedding": vec}}))
    os_request_bulk("\n".join(lines) + "\n")
    print(f"embedded {len(docs)} docs (dim {len(vec)})")

    # 2. эмбеддим запрос той же моделью и ищем ближайших соседей
    qvec = model.encode(QUERY, normalize_embeddings=True).tolist()
    res = os_request("POST", "/articles-knn/_search",
                     {"size": 3, "_source": ["title"],
                      "query": {"knn": {"embedding": {"vector": qvec, "k": 3}}}})
    print(f"knn «{QUERY}»:")
    for h in res["hits"]["hits"]:
        print(f"  {round(h['_score'], 4)}  {h['_source']['title']}")


def os_request_bulk(ndjson):
    req = urllib.request.Request(
        f"{OS_URL}/articles-knn/_bulk?refresh=true", data=ndjson.encode(),
        method="POST", headers={"Content-Type": "application/x-ndjson"},
    )
    with urllib.request.urlopen(req) as resp:
        json.load(resp)


if __name__ == "__main__":
    main()
