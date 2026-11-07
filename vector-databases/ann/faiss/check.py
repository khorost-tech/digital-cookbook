"""Сверка своих реализаций стенда с Faiss: те же данные, те же параметры, recall@10.

    check.py <корпус> hnsw M efC ef1,ef2,…
    check.py <корпус> ivf nlist nprobe1,…
    check.py <корпус> pq m r1,…          (r — кандидатов до пересчёта; 0 — без пересчёта)
    check.py <корпус> sq8

Печатает одну строку JSON. Метрика — скалярное произведение (векторы нормированы).
"""

import json
import sys
import time

import faiss
import numpy as np

D, K = 384, 10


def load(root):
    c = np.fromfile(root + "/corpus.f32", dtype="<f4").reshape(-1, D)
    q = np.fromfile(root + "/queries.f32", dtype="<f4").reshape(-1, D)
    rows = [int(x) for x in open(root + "/gt.rows")]
    gt = np.fromfile(root + "/gt.i32", dtype="<i4").reshape(len(rows), -1)[:, :K]
    return c, q[rows], gt


def recall(ids, gt):
    return round(float(np.mean([len(set(a[:K]) & set(b)) / K for a, b in zip(ids, gt)])), 4)


def main():
    root, algo, args = sys.argv[1], sys.argv[2], sys.argv[3:]
    faiss.omp_set_num_threads(faiss.omp_get_max_threads())
    c, q, gt = load(root)
    train = c[::10]
    out = {"algo": algo, "faiss": faiss.__version__, "n": len(c), "points": []}
    t = time.time()
    if algo == "hnsw":
        m, efc = int(args[0]), int(args[1])
        ix = faiss.IndexHNSWFlat(D, m, faiss.METRIC_INNER_PRODUCT)
        ix.hnsw.efConstruction = efc
        ix.add(c)
        out.update(m=m, efc=efc, build_s=round(time.time() - t, 1))
        for ef in map(int, args[2].split(",")):
            ix.hnsw.efSearch = ef
            faiss.cvar.hnsw_stats.reset()
            ids = ix.search(q, K)[1]
            out["points"].append({"param": ef, "recall_at_10": recall(ids, gt),
                                  "dists_per_query": round(faiss.cvar.hnsw_stats.ndis / len(q), 1)})
    elif algo == "ivf":
        nlist = int(args[0])
        quant = faiss.IndexFlatL2(D)
        ix = faiss.IndexIVFFlat(quant, D, nlist, faiss.METRIC_INNER_PRODUCT)
        ix.cp.niter = 20
        ix.train(train)
        ix.add(c)
        out.update(nlist=nlist, build_s=round(time.time() - t, 1))
        for p in map(int, args[1].split(",")):
            ix.nprobe = p
            out["points"].append({"param": p, "recall_at_10": recall(ix.search(q, K)[1], gt)})
    elif algo in ("pq", "sq8"):
        if algo == "pq":
            m = int(args[0])
            ix = faiss.IndexPQ(D, m, 8, faiss.METRIC_INNER_PRODUCT)
            ix.pq.cp.niter = 20
            out.update(pq_m=m)
            rs = list(map(int, args[1].split(",")))
        else:
            ix = faiss.IndexScalarQuantizer(D, faiss.ScalarQuantizer.QT_8bit, faiss.METRIC_INNER_PRODUCT)
            rs = [0, 100]
        ix.train(train if algo == "pq" else c)
        ix.add(c)
        out.update(build_s=round(time.time() - t, 1))
        for r in rs:
            if r == 0:
                ids = ix.search(q, K)[1]
            else:
                cand = ix.search(q, r)[1]
                ids = [cd[np.lexsort((cd, -(c[cd] @ qv)))][:K] for qv, cd in zip(q, cand)]  # при равенстве — меньший номер
            out["points"].append({"r": r, "rescore": r > 0, "recall_at_10": recall(ids, gt)})
    print(json.dumps(out))


if __name__ == "__main__":
    main()
