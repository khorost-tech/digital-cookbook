"""Оси стенда ml-features: печатает одну строку JSON {ось: {"actual": ..., ...}}.

Поля с «~» — шумные (время), в сравнение сессий не входят.
"""

import json
import os
import sys
import time

import numpy as np

sys.path.insert(0, "/work/dataset")
import dataset as ds  # noqa: E402
import train as tr  # noqa: E402

import db  # noqa: E402
import feast_store as fs  # noqa: E402
from online_team import OnlineFeatures  # noqa: E402

PROD = (90, 100)       # запросы оси skew
REPLAY = (90, 92)      # запросы осей с Feast: два дня
COMPARED = ["tx_count_24h", "amount_avg_30d", "minutes_since_prev", "amount_ratio"]
DAY = ds.DAY


def differs(x, y):
    if (x != x) or (y != y):
        return (x != x) != (y != y)
    return abs(x - y) > 1e-6 * max(1.0, abs(y))


def mismatch(a, b, keys=COMPARED):
    """Доля расхождений по каждому признаку и маска «хоть один признак разошёлся»."""
    n = len(a[keys[0]])
    any_bad = np.zeros(n, bool)
    share = {}
    for k in keys:
        bad = np.array([differs(a[k][i], b[k][i]) for i in range(n)])
        share[k] = round(float(bad.mean()), 6)
        any_bad |= bad
    return share, any_bad


def subset(feats, idx):
    return {k: np.asarray(v)[idx] for k, v in feats.items()}


def scores(model, feats):
    return model.predict_proba(np.column_stack([feats[k] for k in ds.FEATURES]))[:, 1]


def with_base(data, idx, part):
    """Признаки онлайн-пути плюс поля запроса — в форме матрицы модели."""
    out = {k: np.asarray(v, float) for k, v in part.items()}
    out["amount"] = data["amount"][idx].astype(float)
    out["device_new"] = data["device_new"][idx].astype(float)
    out["foreign"] = (data["country"][idx] != data["home"][idx]).astype(float)
    return out


def controls(conn, data, ref, batch, store):
    share, bad = mismatch(batch, ref)
    injected = dict(batch)
    injected["tx_count_24h"] = batch["tx_count_24h"] + 1
    _, bad_inj = mismatch(injected, ref, ["tx_count_24h"])
    return {"actual": {"batch_vs_reference_mismatches": int(bad.sum()), "comparator_detects_injection": bool(bad_inj.all()),
                       "postgres_major": int(conn.execute("SHOW server_version_num").fetchone()[0]) // 10000},
            "versions": {"feast": __import__("feast").__version__, "sklearn": __import__("sklearn").__version__,
                         "numpy": np.__version__, "pandas": __import__("pandas").__version__}}


def skew(data, batch, model, thr):
    req = np.flatnonzero((data["day"] >= PROD[0]) & (data["day"] < PROD[1]))
    warm = np.flatnonzero(data["day"] < PROD[0])
    b = subset(batch, req)
    pb = scores(model, b)
    fraud = data["fraud"][req] == 1
    variants = {"as_written": (), "fix_current": ("current",), "fix_current_calendar": ("current", "calendar"),
                "all_fixed": ("current", "calendar", "default")}
    actual, shares, flips, errors = {}, {}, {}, 0
    for name, fixes in variants.items():
        team = OnlineFeatures(fixes)
        for i in warm:
            team.observe(data["user_id"][i], int(data["ts"][i]), float(data["amount"][i]))
        rows = []
        for i in req:
            try:
                rows.append(team.request(data["user_id"][i], int(data["ts"][i]), float(data["amount"][i])))
            except Exception:  # noqa: BLE001 — сервис отвечает всегда; ошибка была бы видна здесь
                errors += 1
        on = with_base(data, req, {k: [r[k] for r in rows] for k in COMPARED})
        share, _ = mismatch(on, b)
        po = scores(model, on)
        flip = (po > thr) != (pb > thr)
        actual[name] = {"differing": sorted(k for k, v in share.items() if v > 0), "flipped": bool(flip.any())}
        shares[name] = share
        flips[name] = {"flipped": int(flip.sum()), "flagged_online": int((po > thr).sum()), "flagged_batch": int((pb > thr).sum()),
                       "fraud_caught_online": int(((po > thr) & fraud).sum()), "fraud_caught_batch": int(((pb > thr) & fraud).sum())}
        if name == "as_written":
            actual[name]["count_always_off_by_one"] = bool(np.all(on["tx_count_24h"] == b["tx_count_24h"] + 1))
    return {"actual": {"errors": errors, "variants": actual},
            "requests": int(len(req)), "fraud_requests": int(fraud.sum()), "mismatch_share": shares, "decisions": flips}


def feature_store(conn, data, store, model_fs, thr_fs):
    """Онлайн с записью каждого состояния (push) против офлайн-PIT Feast."""
    req = np.flatnonzero((data["day"] >= REPLAY[0]) & (data["day"] < REPLAY[1]))
    start = ds.START + REPLAY[0] * DAY
    # граница PIT: отметка сущности равна отметке строки — строка попадает в выборку?
    probe = req[0]
    own = fs.historical(store, data, np.array([probe]), 0, ["user_state:last_tx_ts"])
    inclusive = int(own["last_tx_ts"].iloc[0]) == int(data["ts"][probe])

    rows = {(u, int(t.timestamp())): (u, t, c, a, last) for u, t, c, a, last in conn.execute(
        "SELECT user_id, event_timestamp, tx_count_24h, amount_avg_30d, last_tx_ts FROM user_state "
        "WHERE event_timestamp >= %s AND event_timestamp < %s", (fs.utc(start), fs.utc(ds.START + REPLAY[1] * DAY)))}
    push, t_online = replay(conn, store, data, req, rows, interval=None)
    # контроль онлайн-хранилища после записи: известный пользователь найден, неизвестный — пусто
    known = fs.online(store, data["user_id"][req[-1]], data["ts"][req[-1]] + 1, 100.0)
    unknown = fs.online(store, "u9999", data["ts"][req[-1]] + 1, 100.0)
    online_controls = {"known_user_found": known["tx_count_24h"] == known["tx_count_24h"],
                       "unknown_user_null": unknown["tx_count_24h"] != unknown["tx_count_24h"]}
    shifted = fs.historical(store, data, req, 1, fs.ONLINE)
    own_row = fs.historical(store, data, req, 0, fs.ONLINE)
    off_s = {k: shifted[k].astype(float).to_numpy() for k in fs.NAMES}
    off_o = {k: own_row[k].astype(float).to_numpy() for k in fs.NAMES}
    prev = np.array([prev_ts(data, i) for i in req], float)
    gap = (data["ts"][req] - 1 - prev) > 7 * DAY      # ttl представления — 7 дней
    share_s, bad_s = mismatch(off_s, push, fs.NAMES)
    share_o, bad_o = mismatch(off_o, push, fs.NAMES)
    on_m = with_base(data, req, push)
    p_on = scores(model_fs, on_m)
    p_own = scores(model_fs, with_base(data, req, off_o))
    p_sh = scores(model_fs, with_base(data, req, off_s))
    ref = ds.reference_features(data)
    _, bad_ref = mismatch({"tx_count_24h": push["tx_count_24h"]}, {"tx_count_24h": ref["tx_count_24h"][req]}, ["tx_count_24h"])
    return {"actual": {
        "online_controls": online_controls,
        "inclusive_boundary": bool(inclusive),
        "own_row": {"tx_count_24h_differ_over_50pct": share_o["tx_count_24h"] > 0.5,
                    "flipped": bool(((p_own > thr_fs) != (p_on > thr_fs)).any())},
        "shifted": {"mismatch_outside_ttl_gap": int((bad_s & ~gap).sum()), "ttl_gap_cases_present": bool(gap.any()),
                    "ttl_gap_cases_all_differ": bool(bad_s[gap].all()),
                    "flipped_outside_ttl_gap": int((((p_sh > thr_fs) != (p_on > thr_fs)) & ~gap).sum())},
        "consistent_but_not_reference": bool(bad_ref[~gap].any())},
        "requests": int(len(req)), "ttl_gap_cases": int(gap.sum()), "mismatch_share_own_row": share_o,
        "mismatch_share_shifted": share_s, "own_row_flipped": int(((p_own > thr_fs) != (p_on > thr_fs)).sum()),
        "not_reference_share": round(float(bad_ref[~gap].mean()), 6), "online_ms~": round(1000 * t_online, 2)}, push


def prev_ts(data, i):
    u = data["user"][i]
    j = PREV[i]
    return data["ts"][j] if j >= 0 and data["user"][j] == u else -1e18


def replay(conn, store, data, req, rows, interval):
    """Поток запросов по времени. interval=None — состояние пишется в онлайн сразу (push);
    иначе — материализация раз в interval секунд, между запусками онлайн стоит на месте."""
    reset_online(conn)
    start = ds.START + REPLAY[0] * DAY
    store.materialize(fs.utc(ds.START), fs.utc(start - 1), feature_views=["user_state"])
    out = {k: [] for k in fs.NAMES}
    boundary, last = start + (interval or 0), start
    spent = 0.0
    for i in req:
        t = int(data["ts"][i])
        while interval and boundary <= t:
            store.materialize(fs.utc(last), fs.utc(boundary - 1), feature_views=["user_state"])
            last, boundary = boundary, boundary + interval
        t0 = time.perf_counter()
        r = fs.online(store, data["user_id"][i], t, data["amount"][i])
        spent += time.perf_counter() - t0
        for k in fs.NAMES:
            out[k].append(r[k])
        if interval is None:
            store.write_to_online_store("user_state", fs.state_row(rows[(data["user_id"][i], t)]))
    return {k: np.array(v, float) for k, v in out.items()}, spent / len(req)


def reset_online(conn):
    for (name,) in conn.execute("SELECT tablename FROM pg_tables WHERE tablename = 'payments_user_state'"):
        conn.execute("TRUNCATE " + name)


def materialization(conn, data, store, push, model_fs, thr_fs):
    req = np.flatnonzero((data["day"] >= REPLAY[0]) & (data["day"] < REPLAY[1]))
    p_push = scores(model_fs, with_base(data, req, push))
    actual, extra = {}, {}
    for name, interval in (("15m", 900), ("1h", 3600), ("24h", DAY)):
        got, _ = replay(conn, store, data, req, None, interval)
        share, bad = mismatch(got, push, fs.NAMES)
        flip = (scores(model_fs, with_base(data, req, got)) > thr_fs) != (p_push > thr_fs)
        extra[name] = {"stale_share": round(float(bad.mean()), 6), "by_feature": share, "flipped": int(flip.sum())}
    s = [extra[k]["stale_share"] for k in ("15m", "1h", "24h")]
    actual = {"stale_15m": s[0] > 0, "monotonic": s[0] < s[1] < s[2], "flipped_24h": extra["24h"]["flipped"] > 0}
    return {"actual": actual, "requests": int(len(req)), "intervals": extra}


def point_in_time(conn, data, store, batch):
    """Счёт мошенничества пользователя как признак: снимок, PIT по времени платежа, PIT по времени знания."""
    idx = np.flatnonzero(data["day"] < PROD[1])
    by_event = fs.historical(store, data, idx, 0, ["fraud_by_event:fraud_count"])["fraud_count"].fillna(0).to_numpy(float)
    by_label = fs.historical(store, data, idx, 0, ["fraud_by_label:fraud_count"])["fraud_count"].fillna(0).to_numpy(float)
    # строго предыдущие события: строка платежа хранит счёт ДО него; отметка = время платежа, граница включительна
    by_prior = fs.historical(store, data, idx, 0, ["fraud_by_event_prior:fraud_count"])["fraud_count"].fillna(0).to_numpy(float)
    tr_fraud = (data["day"][idx] < 75) & (data["fraud"][idx] == 1)
    own_label = bool(np.all(by_event[tr_fraud] - by_prior[tr_fraud] == 1))
    # независимая сверка на всех строках: мошеннические платежи того же пользователя строго раньше текущего
    prior_ref = np.zeros(len(data["ts"]))
    seen = {}
    for i in range(len(data["ts"])):          # данные упорядочены по времени, у пользователя время строго растёт
        u = data["user"][i]
        prior_ref[i] = seen.get(u, 0)
        seen[u] = seen.get(u, 0) + int(data["fraud"][i])
    prior_mismatches = int(np.sum(by_prior != prior_ref[idx]))
    cut = fs.utc(ds.START + 90 * DAY)
    snap = dict(conn.execute("SELECT user_id, count(*) FROM tx WHERE is_fraud = 1 AND label_at < %s GROUP BY user_id", (cut,)).fetchall())
    naive = np.array([float(snap.get(u, 0)) for u in data["user_id"][idx]])
    ref = ds.reference_features(data)["user_fraud_known"][idx]
    # PIT Feast включает строку с той же отметкой, эталон — строго раньше; совпадения отметок здесь не ожидаются
    feast_vs_ref = int(np.sum(by_label != ref))
    feats = FEATURES_PIT
    day = data["day"][idx]
    y = data["fraud"][idx]
    base = {k: batch[k][idx] for k in ds.FEATURES}
    res = {}
    from sklearn.ensemble import HistGradientBoostingClassifier
    from sklearn.metrics import roc_auc_score
    for name, col in (("naive", naive), ("by_event", by_event), ("by_event_prior", by_prior), ("by_label", by_label)):
        X = np.column_stack([base[k] for k in ds.FEATURES] + [col])
        Xp = np.column_stack([base[k] for k in ds.FEATURES] + [by_label])   # в проде известно только пришедшее
        tr_m, va_m, pr_m = day < 75, (day >= 75) & (day < 90), day >= 90
        m = HistGradientBoostingClassifier(max_iter=150, learning_rate=0.1, early_stopping=False, random_state=0)
        m.fit(X[tr_m], y[tr_m])
        res[name] = {"val_auc": round(float(roc_auc_score(y[va_m], m.predict_proba(X[va_m])[:, 1])), 4),
                     "prod_auc": round(float(roc_auc_score(y[pr_m], m.predict_proba(Xp[pr_m])[:, 1])), 4)}
    a = {"naive_val_above_label_val": res["naive"]["val_auc"] > res["by_label"]["val_auc"],
         "event_val_above_label_val": res["by_event"]["val_auc"] > res["by_label"]["val_auc"],
         "naive_prod_below_its_val": res["naive"]["prod_auc"] < res["naive"]["val_auc"] - 0.02,
         "event_prod_below_its_val": res["by_event"]["prod_auc"] < res["by_event"]["val_auc"] - 0.02,
         "label_val_prod_within_0_03": abs(res["by_label"]["val_auc"] - res["by_label"]["prod_auc"]) < 0.03,
         "feast_label_pit_vs_reference_mismatches": feast_vs_ref, "features": feats,
         "by_event_includes_own_label": own_label, "prior_vs_reference_mismatches_all_rows": prior_mismatches,
         "prior_val_above_label_val": res["by_event_prior"]["val_auc"] > res["by_label"]["val_auc"],
         "prior_prod_below_its_val": res["by_event_prior"]["prod_auc"] < res["by_event_prior"]["val_auc"] - 0.02}
    return {"actual": a, "auc": res, "rows": int(len(idx)),
            "fraud_count_nonzero_share": {"naive": round(float((naive > 0).mean()), 6),
                                          "by_event": round(float((by_event > 0).mean()), 6),
                                          "by_event_prior": round(float((by_prior > 0).mean()), 6),
                                          "by_label": round(float((by_label > 0).mean()), 6)}}


FEATURES_PIT = ds.FEATURES + ["user_fraud_count"]


def one_path(conn, data, batch, t_feast):
    req = np.flatnonzero((data["day"] >= REPLAY[0]) & (data["day"] < REPLAY[1]))
    conn.execute(db.ONE_PATH_SQL)
    conn.execute("INSERT INTO tx_live SELECT * FROM tx WHERE ts < %s", (fs.utc(ds.START + REPLAY[0] * DAY),))
    conn.execute("ANALYZE tx_live")
    got = {k: [] for k in ("tx_count_24h", "amount_avg_30d", "minutes_since_prev")}
    spent = 0.0
    for i in req:
        t0 = time.perf_counter()
        c, a, m = conn.execute("SELECT * FROM features_before(%s, %s)", (data["user_id"][i], fs.utc(data["ts"][i]))).fetchone()
        spent += time.perf_counter() - t0
        got["tx_count_24h"].append(float(c)); got["amount_avg_30d"].append(np.nan if a is None else a)
        got["minutes_since_prev"].append(np.nan if m is None else float(m))
        conn.execute("INSERT INTO tx_live SELECT * FROM tx WHERE tx_id = %s", (data["tx_id"][i],))
    on = {k: np.array(v, float) for k, v in got.items()}
    on["amount_ratio"] = data["amount"][req] / on["amount_avg_30d"]
    # обучение — та же функция над той же таблицей, LATERAL по историческим платежам
    train = {k: [] for k in got}
    for c, a, m in conn.execute("SELECT f.* FROM tx t, LATERAL features_before(t.user_id, t.ts) f "
                                "WHERE t.ts >= %s AND t.ts < %s ORDER BY t.ts, t.user_id",
                                (fs.utc(ds.START + REPLAY[0] * DAY), fs.utc(ds.START + REPLAY[1] * DAY))):
        train["tx_count_24h"].append(float(c)); train["amount_avg_30d"].append(np.nan if a is None else a)
        train["minutes_since_prev"].append(np.nan if m is None else float(m))
    tr_ = {k: np.array(v, float) for k, v in train.items()}
    tr_["amount_ratio"] = data["amount"][req] / tr_["amount_avg_30d"]
    _, bad_b = mismatch(on, subset(batch, req))
    _, bad_t = mismatch(on, tr_)
    return {"actual": {"mismatch_vs_batch": int(bad_b.sum()), "mismatch_train_vs_serve": int(bad_t.sum())},
            "requests": int(len(req)), "sql_ms~": round(1000 * spent / len(req), 2),
            "sql_to_feast_online~": round((spent / len(req)) / t_feast, 2)}


def main():
    global PREV
    t0 = time.time()
    data, _ = ds.generate()
    # предыдущий платёж того же пользователя (данные упорядочены по времени)
    PREV = np.full(len(data["ts"]), -1)
    last = {}
    for i, u in enumerate(data["user"]):
        PREV[i] = last.get(u, -1)
        last[u] = i
    ref = ds.reference_features(data)
    conn = db.connect()
    db.load(conn, data)
    batch = db.batch_features(conn, data)
    conn.execute(db.STATE_SQL)
    conn.execute(db.FRAUD_SQL)
    store = fs.make_store("/work/feast", "/tmp/feast-repo")
    model = tr.train(data, batch)
    thr = tr.threshold(model, data, batch)
    res = {"controls": controls(conn, data, ref, batch, store)}
    res["skew"] = skew(data, batch, model, thr)
    # модель для осей Feast — на офлайн-признаках Feast с отметкой «за секунду до платежа»
    tr_idx = np.flatnonzero(data["day"] < 90)
    hist = fs.historical(store, data, tr_idx, 1, fs.ONLINE)
    n = len(data["ts"])
    fsf = {k: np.full(n, np.nan) for k in fs.NAMES}
    for k in fs.NAMES:
        fsf[k][tr_idx] = hist[k].astype(float).to_numpy()
    fsf = db.complete(data, {k: fsf[k] for k in ("tx_count_24h", "amount_avg_30d", "minutes_since_prev")}) | \
        {"amount_ratio": fsf["amount_ratio"]}
    model_fs = tr.train(data, fsf)
    thr_fs = tr.threshold(model_fs, data, fsf)
    res["feature_store"], push = feature_store(conn, data, store, model_fs, thr_fs)
    res["materialization"] = materialization(conn, data, store, push, model_fs, thr_fs)
    res["point_in_time"] = point_in_time(conn, data, store, batch)
    res["one_path"] = one_path(conn, data, batch, res["feature_store"]["online_ms~"] / 1000)
    res["controls"]["total_s~"] = round(time.time() - t0, 1)
    print(json.dumps(res, ensure_ascii=False, default=float))


PREV = None

if __name__ == "__main__":
    main()
