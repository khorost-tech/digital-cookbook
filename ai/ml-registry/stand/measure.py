"""Оси стенда ml-registry: печатает одну строку JSON {ось: {"actual": ..., ...}}.

Поля с «~» — шумные (время), в сравнение сессий не входят.
"""

import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
import warnings

import numpy as np

sys.path.insert(0, "/work/dataset")
import dataset as ds  # noqa: E402
import train as tr  # noqa: E402

import mlflow  # noqa: E402
from mlflow import MlflowClient  # noqa: E402
from sklearn.ensemble import HistGradientBoostingClassifier  # noqa: E402
from sklearn.pipeline import make_pipeline  # noqa: E402
from sklearn.preprocessing import FunctionTransformer  # noqa: E402

NAME = "fraud"
# skops по умолчанию не доверяет узлам деревьев HistGradientBoosting: доверие — явное и к одному типу
TRUSTED = ["sklearn.ensemble._hist_gradient_boosting.predictor.TreePredictor"]
PROD = (90, 100)
DAY = ds.DAY
mlflow.set_tracking_uri(os.environ.get("MLFLOW_TRACKING_URI", "http://mlflow:5000"))
client = MlflowClient()


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def matrix(feats, mask):
    return np.column_stack([feats[k][mask] for k in ds.FEATURES])


def fit(X, y):
    m = HistGradientBoostingClassifier(max_iter=150, learning_rate=0.1, early_stopping=False, random_state=0)
    return m.fit(X, y)


def log(model, X, y, run_name, params, tags=None):
    """Прогон обучения: параметры, срез данных с дайджестом, модель в формате по умолчанию."""
    with mlflow.start_run(run_name=run_name) as run:
        mlflow.log_params(params)
        if tags:
            mlflow.set_tags(tags)
        mlflow.log_input(mlflow.data.from_numpy(X, targets=y, name=run_name), context="training")
        info = mlflow.sklearn.log_model(model, name="model", input_example=X[:2], pyfunc_predict_fn="predict_proba",
                                        skops_trusted_types=TRUSTED)
    return run.info.run_id, info


def files(info, dest):
    if os.path.exists(dest):
        shutil.rmtree(dest)
    path = mlflow.artifacts.download_artifacts(info.model_uri, dst_path=dest)
    return {f: sha(os.path.join(path, f)) for f in sorted(os.listdir(path))}, path


def dataset_digest(run_id):
    return client.get_run(run_id).inputs.dataset_inputs[0].dataset.digest


def flips(p, q, thr):
    return int(((p > thr) != (q > thr)).sum())


def avg_window(data, days):
    """amount_avg за другое окно — «версия 2» признака."""
    out = np.full(len(data["ts"]), np.nan)
    for u in np.unique(data["user"]):
        idx = np.flatnonzero(data["user"] == u)
        ts, amt = data["ts"][idx], data["amount"][idx]
        csum = np.concatenate([[0.0], np.cumsum(amt)])
        pos = np.arange(len(idx))
        lo = np.searchsorted(ts, ts - days * DAY, "left")
        cnt = pos - lo
        with np.errstate(invalid="ignore", divide="ignore"):
            out[idx] = np.where(cnt > 0, (csum[pos] - csum[lo]) / np.maximum(cnt, 1), np.nan)
    return out


def prep_v2(X):
    """Препроцессинг версии 2: логарифм суммы и средней, отношение обрезано сверху на 20."""
    X = np.array(X, float, copy=True)
    i = ds.FEATURES.index
    X[:, i("amount")] = np.log1p(X[:, i("amount")])
    X[:, i("amount_avg_30d")] = np.log1p(X[:, i("amount_avg_30d")])
    X[:, i("amount_ratio")] = np.minimum(X[:, i("amount_ratio")], 20)
    return X


def controls(base, info, Xp, X):
    try:
        with mlflow.start_run(run_name="no-trust"):
            mlflow.sklearn.log_model(base, name="model", input_example=X[:2])
        untrusted = "logged"
        untrusted_msg = ""
    except Exception as e:  # noqa: BLE001 — отказ и его текст и есть измерение
        untrusted = type(e).__name__
        untrusted_msg = str(e).splitlines()[0][:400]
    loaded = mlflow.sklearn.load_model(info.model_uri)
    same = bool(np.array_equal(loaded.predict_proba(Xp)[:, 1], base.predict_proba(Xp)[:, 1]))
    try:
        mlflow.sklearn.load_model("models:/%s@no_such_alias" % NAME)
        missing = "loaded"
    except Exception as e:  # noqa: BLE001
        missing = type(e).__name__
    fmt = sorted(f for f in os.listdir(mlflow.artifacts.download_artifacts(info.model_uri, dst_path="/tmp/ctl")) if f.startswith("model."))
    return {"actual": {"registry_roundtrip_identical": same, "missing_alias": missing, "default_artifact": fmt,
                       "log_without_trusted_types": untrusted},
            "untrusted_message": untrusted_msg,
            "versions": {"mlflow": mlflow.__version__, "sklearn": __import__("sklearn").__version__,
                         "dvc": subprocess.run(["dvc", "--version"], capture_output=True, text=True).stdout.strip()}}


def five_parts(data, feats, base, base_info, thr, Xp, prod):
    pb = base.predict_proba(Xp)[:, 1]
    tr_m = data["day"] < 75
    X, y = matrix(feats, tr_m), data["fraud"][tr_m]
    # тот же код, те же данные, та же версия библиотек
    again = fit(X, y)
    _, again_info = log(again, X, y, "same-inputs", {"train_days": "0-74", "features": "v1", "prep": "v1"})
    fa, _ = files(base_info, "/tmp/a")
    fb, _ = files(again_info, "/tmp/b")
    same = {"flipped": flips(again.predict_proba(Xp)[:, 1], pb, thr),
            "predictions_identical": bool(np.array_equal(again.predict_proba(Xp)[:, 1], pb)),
            "differing_files": sorted(f for f in set(fa) | set(fb) if fa.get(f) != fb.get(f))}
    # срез данных на день больше
    m2 = data["day"] < 76
    sl = fit(matrix(feats, m2), data["fraud"][m2])
    sl_run, _ = log(sl, matrix(feats, m2), data["fraud"][m2], "slice-0-75", {"train_days": "0-75"})
    slice_ = {"flipped": flips(sl.predict_proba(Xp)[:, 1], pb, thr) > 0,
              "digest_differs": dataset_digest(sl_run) != dataset_digest(BASE_RUN)}
    # версия признака: средняя за 14 дней вместо 30; сервинг отдаёт старую
    f2 = dict(feats)
    f2["amount_avg_30d"] = avg_window(data, 14)
    f2["amount_ratio"] = data["amount"] / f2["amount_avg_30d"]
    mv2 = fit(matrix(f2, tr_m), y)
    feature = {"flipped_when_served_v1": flips(mv2.predict_proba(Xp)[:, 1], mv2.predict_proba(matrix(f2, prod))[:, 1], thr)}
    # препроцессинг отдельно от модели и внутри пайплайна
    mp = fit(prep_v2(X), y)
    right = mp.predict_proba(prep_v2(Xp))[:, 1]
    pipe = make_pipeline(FunctionTransformer(prep_v2), fit(prep_v2(X), y))
    prep = {"separate_flipped": flips(mp.predict_proba(Xp)[:, 1], right, thr),
            "pipeline_flipped": flips(pipe.predict_proba(Xp)[:, 1], right, thr)}
    a = {"same_inputs": {"predictions_identical": same["predictions_identical"], "differing_files": same["differing_files"]},
         "data_slice": slice_,
         "feature_version": {"flipped": feature["flipped_when_served_v1"] > 0},
         "preprocessing": {"separate_flipped": prep["separate_flipped"] > 0, "pipeline_flipped": prep["pipeline_flipped"]}}
    return {"actual": a, "prod_requests": int(len(pb)),
            "flipped": {"data_slice": flips(sl.predict_proba(Xp)[:, 1], pb, thr), "feature_version": feature["flipped_when_served_v1"],
                        "preprocessing_separate": prep["separate_flipped"]}}


def decision_policy(data, feats, base, thr, Xp):
    """Та же модель, тот же вход — другой порог: 1 % флагов по окну дней 60–74 вместо валидации 75–89."""
    older = tr.threshold(base, data, feats, days=(60, 75))
    p = base.predict_proba(Xp)[:, 1]
    return {"actual": {"same_model_other_threshold_flipped": flips(p, p, thr) == 0 and int(((p > thr) != (p > older)).sum()) > 0,
                       "threshold_from_older_window_differs": older != thr},
            "threshold_val": round(thr, 6), "threshold_older": round(older, 6), "flipped": int(((p > thr) != (p > older)).sum()),
            "prod_requests": int(len(p))}


def library_version(base_info, base, Xp):
    _, path = files(base_info, "/tmp/lib")
    skops_file = os.path.join(path, "model.skops")
    np.save("/tmp/Xp.npy", Xp)
    ref = base.predict_proba(Xp)[:, 1]
    out, extra = {}, {}
    for v in ("1.8.0", "1.7.2", "1.6.1"):
        p = subprocess.run(["/opt/sklearn-%s/bin/python" % v, "/work/stand/old_sklearn.py", skops_file, "/tmp/Xp.npy", "/tmp/p.npy"],
                           capture_output=True, text=True)
        r = json.loads(p.stdout.strip().splitlines()[-1])
        same = None
        if r["outcome"] == "loaded":
            same = bool(np.array_equal(np.load("/tmp/p.npy"), ref))
        version_warning = any(w.startswith("InconsistentVersionWarning") for w in r["warnings"])
        out[v] = {"same_trust_list": r["same_trust_list"], "outcome": r["outcome"], "predictions_identical": same,
                  "version_warning": version_warning}
        extra[v] = {"warnings": r["warnings"], "error": r.get("error"), "same_trust_error": r.get("same_trust_error"),
                    "extra_trusted": r["extra_trusted"]}
    req = open(os.path.join(path, "requirements.txt")).read().split()
    return {"actual": {"by_version": out, "artifact_pins_sklearn": "scikit-learn==" + __import__("sklearn").__version__ in req},
            "messages": extra}


def registry(v1, v2):
    client.set_registered_model_alias(NAME, "champion", v1)
    with warnings.catch_warnings(record=True) as caught:
        warnings.simplefilter("always")
        client.transition_model_version_stage(NAME, v1, "Production")
        latest = client.get_latest_versions(NAME, stages=["Production"])
    stage_warnings = sorted({w.category.__name__ for w in caught if "stage" in str(w.message).lower()})
    by_stage = mlflow.sklearn.load_model("models:/%s/Production" % NAME)
    return {"actual": {"alias_resolves": client.get_model_version_by_alias(NAME, "champion").version == v1,
                       "stage_api_works": [m.version for m in latest] == [v1] and by_stage is not None,
                       "stage_api_warning": stage_warnings},
            "stage_warning_text": sorted({str(w.message).splitlines()[0][:200] for w in caught if "stage" in str(w.message).lower()})}


def wait(url, timeout=120):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            with urllib.request.urlopen(url, timeout=2) as r:
                if r.status == 200:
                    return time.time() - t0
        except Exception:  # noqa: BLE001
            time.sleep(0.5)
    raise RuntimeError("не поднялся " + url)


def post(url, body):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.loads(r.read())


def get(url):
    with urllib.request.urlopen(url, timeout=10) as r:
        return r.read().decode()


def stages(v1, v2, m1, m2, Xs):
    """Алиас перенесли на v2 — что обслуживают уже запущенные процессы."""
    client.set_registered_model_alias(NAME, "champion", v1)
    p1, p2 = m1.predict_proba(Xs)[:, 1], m2.predict_proba(Xs)[:, 1]
    env = dict(os.environ, MLFLOW_TRACKING_URI=mlflow.get_tracking_uri())
    serve = ["mlflow", "models", "serve", "-m", "models:/%s@champion" % NAME, "--env-manager", "local", "-h", "127.0.0.1", "-p", "5001"]
    own = [sys.executable, "/work/stand/serve.py", NAME, "champion", "5002"]

    def start():
        procs = [subprocess.Popen(cmd, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
                 for cmd in (serve, own)]
        wait("http://127.0.0.1:5001/ping")
        wait("http://127.0.0.1:5002/loaded")
        return procs

    def query():
        r = post("http://127.0.0.1:5001/invocations", {"inputs": Xs.tolist()})
        got = np.array([row[1] if isinstance(row, list) else row for row in r["predictions"]], float)
        return got

    def reconcile():
        loaded = json.loads(get("http://127.0.0.1:5002/loaded"))
        return loaded["version"] == client.get_model_version_by_alias(NAME, "champion").version

    def stop(procs):
        # mlflow models serve запускает воркеры uvicorn дочерними процессами — гасим всю группу и ждём, пока порты закроются
        for p in procs:
            os.killpg(p.pid, signal.SIGTERM)
            p.wait(timeout=30)
        for port in (5001, 5002):
            t0 = time.time()
            while time.time() - t0 < 30:
                try:
                    urllib.request.urlopen("http://127.0.0.1:%d/" % port, timeout=1)
                except urllib.error.HTTPError:
                    pass
                except Exception:  # noqa: BLE001 — соединение не принято: порт свободен
                    break
                time.sleep(0.3)
            else:
                raise RuntimeError("порт %d не освободился" % port)

    procs = start()
    before = query()
    ok_before = reconcile()
    version_body = get("http://127.0.0.1:5001/version").strip().strip('"')
    client.set_registered_model_alias(NAME, "champion", v2)
    # реестр хранит текущее назначение: у прежней версии следа алиаса не остаётся
    old_trace = "champion" in (client.get_model_version(NAME, v1).aliases or [])
    after = query()
    ok_after = reconcile()
    stop(procs)
    procs = start()
    restarted = query()
    ok_restart = reconcile()
    stop(procs)
    tol = 1e-12
    a = {"before_move_serves_v1": bool(np.allclose(before, p1, atol=tol, rtol=0)),
         "after_move_serves_v1": bool(np.allclose(after, p1, atol=tol, rtol=0)),
         "after_move_serves_v2": bool(np.allclose(after, p2, atol=tol, rtol=0)),
         "after_restart_serves_v2": bool(np.allclose(restarted, p2, atol=tol, rtol=0)),
         "version_endpoint_is_mlflow_version": version_body == mlflow.__version__,
         "reconcile": {"before_move": ok_before, "after_move": ok_after, "after_restart": ok_restart},
         "registry_says_after_move": "v2", "old_version_alias_trace": old_trace}
    return {"actual": a, "requests": int(len(Xs)), "v1_v2_differ": int((np.abs(p1 - p2) > 1e-9).sum())}


def git_size(path):
    total = 0
    for root, _, fs in os.walk(os.path.join(path, ".git")):
        for f in fs:
            total += os.path.getsize(os.path.join(root, f))
    return total


def run(cmd, cwd):
    p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError("%s: %s" % (cmd, p.stderr[-500:]))
    return p.stdout


def csv_model(tx, fe, day, Xp):
    """Модель, обученная из CSV версии датасета: предсказания на проде."""
    y = np.loadtxt(tx, delimiter=",", skiprows=1, usecols=[8], dtype=int)
    X = np.genfromtxt(fe, delimiter=",", skip_header=1, usecols=range(1, 8))
    return fit(X[day < 75], y[day < 75]).predict_proba(Xp)[:, 1]


def data_versioning(data, feats, base, Xp):
    """DVC: две версии датасета в кеше, git хранит указатель; откат восстанавливает байты и модель."""
    repo = "/tmp/dv"
    shutil.rmtree(repo, ignore_errors=True)
    os.makedirs(repo + "/data")
    run(["git", "init", "-q"], repo)
    run(["dvc", "init", "-q"], repo)
    tx, fe = repo + "/data/transactions.csv", repo + "/data/features.csv"
    ds.write_csv(data, feats, tx, fe)
    v1_sha = (sha(tx), sha(fe))
    v1_model = csv_model(tx, fe, data["day"], Xp)
    run(["dvc", "add", "-q", "data/transactions.csv", "data/features.csv"], repo)
    run(["git", "add", "-A"], repo)
    run(["git", "commit", "-qm", "v1"], repo)
    # версия 2: пришли поздние chargeback — у десяти платежей меняется метка, признаки те же
    d2 = dict(data)
    d2["fraud"] = data["fraud"].copy()
    flip = np.flatnonzero((data["fraud"] == 0) & (data["day"] < 60))[:10]
    d2["fraud"][flip] = 1
    tx1_size = os.path.getsize(tx)
    ds.write_csv(d2, feats, tx, fe)
    v2_sha = (sha(tx), sha(fe))
    run(["dvc", "add", "-q", "data/transactions.csv", "data/features.csv"], repo)
    run(["git", "add", "-A"], repo)
    run(["git", "commit", "-qm", "v2"], repo)
    cache = sum(os.path.getsize(os.path.join(r, f)) for r, _, fs in os.walk(repo + "/.dvc/cache") for f in fs)
    file_size = os.path.getsize(tx) + os.path.getsize(fe)
    git_dvc = git_size(repo)
    run(["git", "checkout", "-q", "HEAD~1", "--", "data/transactions.csv.dvc", "data/features.csv.dvc"], repo)
    run(["dvc", "checkout", "-q"], repo)
    restored = (sha(tx), sha(fe)) == v1_sha
    same_model = bool(np.array_equal(csv_model(tx, fe, data["day"], Xp), v1_model))
    # тот же датасет прямо в git: две версии, до и после gc
    plain = "/tmp/plain"
    shutil.rmtree(plain, ignore_errors=True)
    os.makedirs(plain + "/data")
    run(["git", "init", "-q"], plain)
    for d_ in (data, d2):
        ds.write_csv(d_, feats, plain + "/data/transactions.csv", plain + "/data/features.csv")
        run(["git", "add", "-A"], plain)
        run(["git", "commit", "-qm", "v"], plain)
    plain_before = git_size(plain)
    run(["git", "gc", "-q", "--aggressive"], plain)
    plain_after = git_size(plain)
    return {"actual": {"versions_differ": v1_sha != v2_sha, "only_labels_file_changed": v1_sha[1] == v2_sha[1],
                       "restored_bytes_identical": restored, "restored_model_identical": same_model,
                       "dvc_stores_changed_file_whole_and_unchanged_once": cache == tx1_size + file_size,
                       "git_with_dvc_under_1mb": git_dvc < 1024 * 1024,
                       "git_gc_smaller_than_dvc_cache": plain_after < cache},
            "bytes": {"dataset": file_size, "dvc_cache": cache},
            # размер .git зависит от времени коммитов — несколько байт между сессиями
            "git_bytes~": {"with_dvc": git_dvc, "plain_loose": plain_before, "plain_after_gc": plain_after}}


def main():
    global BASE_RUN
    t0 = time.time()
    data, _ = ds.generate()
    feats = ds.reference_features(data)
    prod = (data["day"] >= PROD[0]) & (data["day"] < PROD[1])
    Xp = matrix(feats, prod)
    base = tr.train(data, feats)
    thr = tr.threshold(base, data, feats)
    tr_m = data["day"] < 75
    X, y = matrix(feats, tr_m), data["fraud"][tr_m]
    mlflow.set_experiment("fraud")
    BASE_RUN, base_info = log(base, X, y, "base", {"train_days": "0-74", "features": "v1", "prep": "v1"})
    v1 = mlflow.register_model(base_info.model_uri, NAME).version
    res = {"controls": controls(base, base_info, Xp, X)}
    m2 = data["day"] < 76
    newer = fit(matrix(feats, m2), data["fraud"][m2])
    _, newer_info = log(newer, matrix(feats, m2), data["fraud"][m2], "newer", {"train_days": "0-75"})
    v2 = mlflow.register_model(newer_info.model_uri, NAME).version
    res["five_parts"] = five_parts(data, feats, base, base_info, thr, Xp, prod)
    res["decision_policy"] = decision_policy(data, feats, base, thr, Xp)
    res["library_version"] = library_version(base_info, base, Xp)
    res["registry"] = registry(v1, v2)
    sample = np.flatnonzero(prod)[:2000]
    res["stages"] = stages(v1, v2, base, newer, feats_matrix(feats, sample))
    res["data_versioning"] = data_versioning(data, feats, base, Xp)
    res["controls"]["total_s~"] = round(time.time() - t0, 1)
    print(json.dumps(res, ensure_ascii=False, default=float))


def feats_matrix(feats, idx):
    return np.column_stack([feats[k][idx] for k in ds.FEATURES])


BASE_RUN = None

if __name__ == "__main__":
    main()
