"""Оси стенда ml-serving: печатает одну строку JSON {ось: {"actual": ..., ...}}.

Абсолютное время — только в полях «~»; в actual — соотношения и пороговые признаки.
Сервис BentoML работает на ядрах 0–3 (taskset), генератор нагрузки — шесть процессов на ядрах 4–9.
"""

import asyncio
import json
import multiprocessing
import os
import re
import signal
import subprocess
import sys
import time

import httpx
import joblib
import numpy as np

sys.path.insert(0, "/work/dataset")
import dataset as ds  # noqa: E402
import train as tr  # noqa: E402

MODEL = "/tmp/model.joblib"
PORT = 3000
URL = "http://127.0.0.1:%d" % PORT
SERVICE_CPUS = "0-3"
LOAD_CPUS = [4, 5, 6, 7, 8, 9]
SLO_S = 0.1                     # «ответ в срок»: 200 не позже 100 мс
os.sched_setaffinity(0, set(LOAD_CPUS))   # генератор нагрузки и замеры в процессе — не на ядрах сервиса


def start(workers=1, batch=False, delay_ms=0):
    env = dict(os.environ, FRAUD_MODEL=MODEL, FRAUD_WORKERS=str(workers), FRAUD_BATCH="1" if batch else "0",
               FRAUD_DELAY_MS=str(delay_ms), BENTOML_DO_NOT_TRACK="true", OMP_NUM_THREADS="1")
    t0 = time.perf_counter()
    p = subprocess.Popen(["taskset", "-c", SERVICE_CPUS, "bentoml", "serve", "service:Fraud", "--host", "127.0.0.1",
                          "--port", str(PORT)], cwd="/work/stand", env=env, stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL, start_new_session=True)
    return p, t0


def ready(t0, timeout=120):
    """Опрос /readyz каждые 50 мс; сколько запросов до готовности получили отказ."""
    refused = 0
    while time.perf_counter() - t0 < timeout:
        try:
            if httpx.get(URL + "/readyz", timeout=1).status_code == 200:
                return time.perf_counter() - t0, refused
            refused += 1
        except httpx.HTTPError:
            refused += 1
        time.sleep(0.05)
    raise RuntimeError("сервис не стал готов")


def stop(p):
    os.killpg(p.pid, signal.SIGTERM)
    p.wait(timeout=60)
    t0 = time.time()
    while time.time() - t0 < 30:
        try:
            httpx.get(URL + "/readyz", timeout=0.5)
        except httpx.HTTPError:
            return
        time.sleep(0.2)
    raise RuntimeError("порт не освободился")


def score_one(client, row):
    r = client.post(URL + "/score", json={"rows": [row]}, timeout=30)
    r.raise_for_status()
    return r.json()[0]


async def _load_part(rows, concurrency, idx, path):
    lat, codes, good = [], {}, [0]
    it = iter(idx)
    async with httpx.AsyncClient(timeout=30, limits=httpx.Limits(max_connections=concurrency)) as c:
        async def worker():
            for i in it:
                t = time.perf_counter()
                r = await (c.post(URL + path, json={"rows": [rows[i % len(rows)]]}) if path == "/score" else c.get(URL + path))
                spent = time.perf_counter() - t
                codes[r.status_code] = codes.get(r.status_code, 0) + 1
                if r.status_code == 200:
                    lat.append(spent)
                    good[0] += spent <= SLO_S
        t0 = time.perf_counter()
        await asyncio.gather(*[worker() for _ in range(concurrency)])
        return lat, codes, t0, time.perf_counter(), good[0]


def _part(args):
    cpu, rows, conc, idx, path = args
    os.sched_setaffinity(0, {cpu})
    return asyncio.run(_load_part(rows, conc, idx, path))


def load(rows, concurrency, n, path="/score"):
    """n запросов по одной строке с общей конкуренцией concurrency, разложенной по процессам генератора."""
    procs = min(len(LOAD_CPUS), concurrency)
    share = [concurrency // procs + (1 if k < concurrency % procs else 0) for k in range(procs)]
    parts = [(LOAD_CPUS[k], rows, share[k], list(range(k, n, procs)), path) for k in range(procs)]
    with multiprocessing.get_context("fork").Pool(procs) as pool:
        got = pool.map(_part, parts)
    lat = np.array([x for g in got for x in g[0]])
    codes = {}
    for g in got:
        for k, v in g[1].items():
            codes[k] = codes.get(k, 0) + v
    wall = max(g[3] for g in got) - min(g[2] for g in got)
    # p50, p99 и rps — по успешным ответам; доля в срок — по всем запросам, отказ считается промахом
    return {"good_share": sum(g[4] for g in got) / n, "rps": len(lat) / wall, "p50_ms": 1000 * float(np.median(lat)), "p99_ms": 1000 * float(np.quantile(lat, 0.99)),
            "rejected_share": 1 - codes.get(200, 0) / n, "codes": {str(k): v for k, v in sorted(codes.items())}}


def batch_sizes():
    """Средний размер пачки по метрике адаптивного батчинга BentoML."""
    text = httpx.get(URL + "/metrics", timeout=5).text
    s = sum(float(v) for v in re.findall(r'^bentoml_service_adaptive_batch_size_sum\{[^}]*\} (\S+)', text, re.M))
    c = sum(float(v) for v in re.findall(r'^bentoml_service_adaptive_batch_size_count\{[^}]*\} (\S+)', text, re.M))
    return s / c if c else None


def rss_mb(p):
    """Суммарная резидентная память процессов сессии сервиса (сервис стартует в своей сессии)."""
    total = 0
    for pid in filter(str.isdigit, os.listdir("/proc")):
        try:
            with open("/proc/%s/stat" % pid) as f:
                sid = int(f.read().rsplit(")", 1)[1].split()[3])
            if sid != p.pid:
                continue
            with open("/proc/%s/status" % pid) as f:
                total += int(re.search(r"VmRSS:\s+(\d+)", f.read()).group(1))
        except (OSError, AttributeError, ValueError, IndexError):
            pass
    return total / 1024


def controls(model, rows):
    p, t0 = start()
    ready(t0)
    with httpx.Client() as c:
        got = [score_one(c, r) for r in rows[:500]]
        bad = c.post(URL + "/score", json={"rows": [[1.0, 2.0, 3.0]]}, timeout=30).status_code
        missing = c.post(URL + "/score", json={}, timeout=30).status_code
        nan_row = c.post(URL + "/score", content='{"rows": [[NaN, 1, NaN, 5, NaN, 0, 0]]}',
                         headers={"Content-Type": "application/json"}, timeout=30).status_code
    stop(p)
    want = model.predict_proba(np.array(rows[:500]))[:, 1]
    return {"actual": {"service_matches_in_process": bool(np.array_equal(np.array(got), want)),
                       "wrong_width_status": bad, "missing_field_status": missing, "nan_row_status": nan_row},
            "versions": {"bentoml": __import__("bentoml").__version__, "sklearn": __import__("sklearn").__version__}}


def service_vs_library(model, rows):
    X = np.array(rows[:2000])
    t = time.perf_counter()
    for i in range(len(X)):
        model.predict_proba(X[i:i + 1])
    inproc = (time.perf_counter() - t) / len(X)
    p, t0 = start()
    ready(t0)
    with httpx.Client() as c:
        for r in rows[:200]:
            score_one(c, r)                     # прогрев
        t = time.perf_counter()
        for r in rows[:2000]:
            score_one(c, r)
        http = (time.perf_counter() - t) / 2000
    stop(p)
    return {"actual": {"http_at_least_2x_in_process": http / inproc >= 2},
            "in_process_ms~": round(1000 * inproc, 3), "http_ms~": round(1000 * http, 3), "ratio~": round(http / inproc, 1)}


def cold_start(rows):
    p, t0 = start()
    to_ready, refused = ready(t0)
    with httpx.Client() as c:
        t = time.perf_counter()
        score_one(c, rows[0])
        first = time.perf_counter() - t
        steady = []
        for r in rows[1:301]:
            t = time.perf_counter()
            score_one(c, r)
            steady.append(time.perf_counter() - t)
        load_s = c.post(URL + "/load_time", json={}, timeout=30).json()
    rss = rss_mb(p)
    stop(p)
    artifact_mb = os.path.getsize(MODEL) / 2 ** 20
    return {"actual": {"requests_before_ready_refused": refused > 0, "model_load_under_20pct_of_start": load_s < 0.2 * to_ready,
                       "rss_over_20x_artifact": rss > 20 * artifact_mb},
            "artifact_mb": round(artifact_mb, 2), "to_ready_s~": round(to_ready, 2), "model_load_s~": round(load_s, 4),
            "refused_before_ready~": refused, "first_request_ms~": round(1000 * first, 2),
            "steady_p50_ms~": round(1000 * float(np.median(steady)), 2), "rss_mb~": round(rss, 1)}


def batching(rows):
    out = {}
    for batch in (False, True):
        p, t0 = start(batch=batch)
        ready(t0)
        (load(rows, 8, 300))         # прогрев
        for conc in (1, 8, 64):
            r = (load(rows, conc, 3000 if conc > 1 else 1000))
            if batch:
                r["mean_batch"] = batch_sizes()
            out[("on" if batch else "off") + "_c%d" % conc] = r
            if batch:
                stop(p)
                p, t0 = start(batch=True)        # счётчик пачек — с нуля для каждой конкуренции
                ready(t0)
                (load(rows, 8, 300))
        stop(p)
    gain = {c: out["on_c%d" % c]["rps"] / out["off_c%d" % c]["rps"] for c in (1, 8, 64)}
    p, t0 = start()
    ready(t0)
    ceiling = load(rows, 64, 6000, path="/livez")["rps"]
    stop(p)
    best = max(v["rps"] for v in out.values())
    return {"actual": {"generator_ceiling_over_2x_best": ceiling > 2 * best,
                       "c64_throughput_gain_over_1_5": gain[64] > 1.5, "c1_no_gain": gain[1] < 1.2,
                       "c1_mean_batch_under_1_5": out["on_c1"]["mean_batch"] < 1.5, "c64_mean_batch_over_4": out["on_c64"]["mean_batch"] > 4,
                       "c64_batching_rejects_some": out["on_c64"]["rejected_share"] > 0,
                       "without_batching_rejects_none": all(out["off_c%d" % c]["rejected_share"] == 0 for c in (1, 8, 64)),
                       "slo_100ms": {"c1_batching_not_better": out["on_c1"]["good_share"] <= out["off_c1"]["good_share"],
                                     "c64_batching_better": out["on_c64"]["good_share"] > out["off_c64"]["good_share"]}},
            "good_share~": {k: round(v["good_share"], 4) for k, v in out.items()},
            "gain~": {str(c): round(v, 2) for c, v in gain.items()}, "generator_ceiling_rps~": round(ceiling, 1),
            "rejected_share~": {k: round(v["rejected_share"], 4) for k, v in out.items()},
            "runs~": {k: {kk: (round(vv, 2) if isinstance(vv, float) else vv) for kk, vv in v.items() if kk != "rejected_share"}
                      for k, v in out.items()}}


def workers(rows):
    out = {}
    for w in (1, 4):
        p, t0 = start(workers=w)
        ready(t0)
        (load(rows, 16, 300))
        out[w] = (load(rows, 64, 4000))
        stop(p)
    ratio = out[4]["rps"] / out[1]["rps"]
    p, t0 = start(workers=4)
    ready(t0)
    ceiling = load(rows, 64, 8000, path="/livez")["rps"]
    stop(p)
    return {"actual": {"generator_ceiling_over_2x_best": ceiling > 2 * max(out[1]["rps"], out[4]["rps"]),
                       "four_workers_over_2x": ratio > 2, "rejected": {str(k): v["rejected_share"] > 0 for k, v in out.items()}},
            "ratio~": round(ratio, 2), "generator_ceiling_rps~": round(ceiling, 1),
            "runs~": {str(k): {kk: (round(vv, 4) if isinstance(vv, float) else vv) for kk, vv in v.items()} for k, v in out.items()}}


def heuristic(row):
    """Запасное правило: крупная сумма относительно обычной или новое устройство из чужой страны."""
    f = dict(zip(ds.FEATURES, row))
    ratio = f["amount_ratio"]
    return 1.0 if (ratio == ratio and ratio > 4) or (f["device_new"] == 1 and f["foreign"] == 1) else 0.0


def degradation(model, thr, rows, fraud):
    """Модель отвечает за 200 мс, клиент ждёт 50 мс. Три политики клиента против здоровой модели."""
    X = np.array(rows)
    healthy = model.predict_proba(X)[:, 1]
    p, t0 = start(delay_ms=200)
    ready(t0)
    res = {}
    with httpx.Client() as c:
        for policy in ("silent_zero", "heuristic", "explicit_error"):
            scores, visible_errors = [], 0
            for r in rows:
                try:
                    resp = c.post(URL + "/score", json={"rows": [r]}, timeout=0.05)
                    resp.raise_for_status()
                    scores.append(resp.json()[0])
                except httpx.HTTPError:
                    if policy == "silent_zero":
                        scores.append(0.0)               # «чтобы не ронять платёж»
                    elif policy == "heuristic":
                        scores.append(heuristic(r))
                    else:
                        visible_errors += 1
                        scores.append(np.nan)
            s = np.array(scores)
            flagged = (s > thr)
            res[policy] = {"visible_errors": visible_errors, "flagged": int(np.nansum(flagged)),
                           "fraud_caught": int(np.sum(flagged & fraud)), "mean_score": float(np.nanmean(s)) if visible_errors < len(s) else None}
    stop(p)
    res["healthy"] = {"visible_errors": 0, "flagged": int((healthy > thr).sum()), "fraud_caught": int(((healthy > thr) & fraud).sum()),
                      "mean_score": float(healthy.mean())}
    a = {"silent_zero": {"visible_errors": res["silent_zero"]["visible_errors"], "flagged": res["silent_zero"]["flagged"]},
         "heuristic": {"visible_errors": res["heuristic"]["visible_errors"],
                       "catches_some": 0 < res["heuristic"]["fraud_caught"] < res["healthy"]["fraud_caught"]},
         "explicit_error": {"all_visible": res["explicit_error"]["visible_errors"] == len(rows)},
         "silent_mean_score_drop_over_90pct": res["silent_zero"]["mean_score"] < 0.1 * res["healthy"]["mean_score"]}
    return {"actual": a, "requests": len(rows), "fraud": int(fraud.sum()), "policies": res}


def main():
    t0 = time.time()
    data, _ = ds.generate()
    feats = ds.reference_features(data)
    model = tr.train(data, feats)
    thr = tr.threshold(model, data, feats)
    joblib.dump(model, MODEL)
    prod = np.flatnonzero((data["day"] >= 90) & (data["day"] < 100))
    X = np.column_stack([feats[k][prod] for k in ds.FEATURES])
    X = X[~np.isnan(X).any(axis=1)]            # нагрузка — строки без пропусков; пропуск в JSON — отдельный контроль
    rows = [[float(v) for v in r] for r in X[:5000]]
    res = {"controls": controls(model, rows)}
    res["service_vs_library"] = service_vs_library(model, rows)
    res["cold_start"] = cold_start(rows)
    res["batching"] = batching(rows)
    res["workers"] = workers(rows)
    # деградация: всё мошенничество дней 90–99 и столько же легитимных платежей
    fr = prod[data["fraud"][prod] == 1]
    legit = prod[data["fraud"][prod] == 0][: 3 * len(fr)]
    sel = np.concatenate([fr, legit])
    sel = sel[~np.isnan(np.column_stack([feats[k][sel] for k in ds.FEATURES])).any(axis=1)]
    Xd = [[float(v) for v in r] for r in np.column_stack([feats[k][sel] for k in ds.FEATURES])]
    res["degradation"] = degradation(model, thr, Xd, data["fraud"][sel] == 1)
    res["controls"]["total_s~"] = round(time.time() - t0, 1)
    print(json.dumps(res, ensure_ascii=False, default=float))


if __name__ == "__main__":
    main()
