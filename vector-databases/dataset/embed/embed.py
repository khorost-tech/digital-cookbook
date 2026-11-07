"""Эмбеддинги корпуса BEIR и его тестовых запросов моделью e5.

    embed.py <корпус>      → <корпус>/corpus.f32, corpus.ids, queries.f32, queries.ids

Векторы — сырые float32 little-endian, строка за строкой, нормированные (длина 1);
идентификаторы — по одному в строке, в том же порядке. Префиксы e5: «passage: »
для документов, «query: » для запросов. Запросы — только из qrels/test.tsv.
"""

import json
import os
import sys
import time

import numpy as np
import torch
from sentence_transformers import SentenceTransformer

torch.set_num_threads(os.cpu_count())
torch.manual_seed(0)


def read_jsonl(path):
    with open(path, encoding="utf-8") as f:
        for line in f:
            yield json.loads(line)


def test_query_ids(root):
    ids = set()
    with open(os.path.join(root, "qrels", "test.tsv"), encoding="utf-8") as f:
        next(f)
        for line in f:
            ids.add(line.split("\t")[0])
    return ids


def encode(model, texts):
    return model.encode(texts, batch_size=64, normalize_embeddings=True,
                        convert_to_numpy=True, show_progress_bar=False).astype("<f4")


def write(root, name, ids, vecs):
    vecs.tofile(os.path.join(root, name + ".f32"))
    with open(os.path.join(root, name + ".ids"), "w", encoding="utf-8") as f:
        f.write("\n".join(ids) + "\n")


def main(root):
    model = SentenceTransformer(os.environ["MODEL"], revision=os.environ["REVISION"], device="cpu")
    docs = list(read_jsonl(os.path.join(root, "corpus.jsonl")))
    t0 = time.time()
    vecs = encode(model, ["passage: " + (d.get("title", "") + " " + d["text"]).strip() for d in docs])
    write(root, "corpus", [d["_id"] for d in docs], vecs)
    want = test_query_ids(root)
    qs = [q for q in read_jsonl(os.path.join(root, "queries.jsonl")) if q["_id"] in want]
    write(root, "queries", [q["_id"] for q in qs], encode(model, ["query: " + q["text"] for q in qs]))
    print(json.dumps({"corpus": root, "docs": len(docs), "queries": len(qs), "dim": vecs.shape[1],
                      "seconds": round(time.time() - t0)}))


if __name__ == "__main__":
    main(sys.argv[1])
