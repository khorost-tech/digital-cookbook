"""Проверки датасета: печатает одну строку JSON {проверка: {"actual": ..., ...}}.

Поля с «~» — шумные (время), в сравнение сессий не входят.
"""

import json
import os
import sys
import time

import numpy as np

import dataset as ds
import train as tr

OUT = sys.argv[1] if len(sys.argv) > 1 else "/work/out"


def controls(data, sha_a, sha_b):
    day, scheme, fraud = data["day"], data["scheme"], data["fraud"]
    early = day < ds.SCHEME_DAY
    delay = (data["label_at"] - data["ts"]) / ds.DAY
    user_ok = all(np.all(np.diff(data["ts"][data["user"] == u]) > 0) for u in range(ds.N_USERS))
    share = float(fraud[early].mean())
    return {
        "actual": {
            "users": int(len(np.unique(data["user"]))),
            "days": int(day.max() + 1),
            "deterministic": sha_a == sha_b,
            "schemes": sorted(set(scheme.tolist())),
            "fraud_share_before_105_in_0_3_to_1_5pct": 0.003 <= share <= 0.015,
            "card_testing_from_day_105_only": bool(day[scheme == "card_testing"].min() >= ds.SCHEME_DAY),
            "digital_segment_from_day_100_only": bool(day[data["segment"] == "digital"].min() >= ds.SEGMENT_DAY),
            "legit_label_delay_days": sorted(set(np.round(delay[fraud == 0], 6).tolist())),
            "fraud_label_delay_within_10_60": bool(delay[fraud == 1].min() >= 10 and delay[fraud == 1].max() <= 60),
            "user_time_strictly_increasing": bool(user_ok),
        },
        "transactions": int(len(day)),
        "fraud_by_scheme": {s: int(((scheme == s) & (fraud == 1)).sum()) for s in ("noise", "takeover", "card_testing")},
        "fraud_share_before_105": round(share, 6),
        "sha256": sha_a,
    }


def reference(data, feats):
    rng = np.random.default_rng(1)
    sample = rng.choice(len(data["ts"]), 2000, replace=False)
    keys = ("tx_count_24h", "amount_avg_30d", "minutes_since_prev", "user_fraud_known")

    def mismatches(values):
        bad = 0
        for i in sample:
            b = ds.brute_force(data, i)
            for k in keys:
                x, y = values[k][i], b[k]
                if (x != x) != (y != y) or (x == x and abs(x - y) > 1e-6 * max(1.0, abs(y))):
                    bad += 1
                    break
        return bad

    # отрицательный контроль: окно, включающее сам платёж, обязано разойтись
    broken = dict(feats)
    broken["tx_count_24h"] = feats["tx_count_24h"] + 1
    return {"actual": {"sampled": len(sample), "mismatches": mismatches(feats),
                       "broken_window_detected": mismatches(broken) > 0}}


def labels(data):
    end = ds.START + ds.DAYS * ds.DAY
    prod = data["day"] >= 90
    known = data["label_at"] < end
    return {"actual": {"prod_labeled_share_below_1pct": float(known[prod].mean()) < 0.01,
                       "prod_legit_labels_known": int((known & prod & (data["fraud"] == 0)).sum()),
                       "labeled_share_falls_after_day_60": bool(known[data["day"] < 59].mean() == 1.0
                                                                and known[(data["day"] >= 60) & ~prod].mean() < 0.05)},
            "prod_labeled_share": round(float(known[prod].mean()), 6),
            "prod_fraud_labels_known": int((known & prod & (data["fraud"] == 1)).sum())}


def model(data, feats):
    t0 = time.time()
    a = tr.train(data, feats)
    b = tr.train(data, feats)
    m = data["day"] >= 75
    pa = a.predict_proba(tr.matrix(feats, m))[:, 1]
    pb = b.predict_proba(tr.matrix(feats, m))[:, 1]
    thr = tr.threshold(a, data, feats)
    val = (data["day"] >= 75) & (data["day"] < 90)
    flags = float((a.predict_proba(tr.matrix(feats, val))[:, 1] > thr).mean())
    auc = tr.auc(a, data, feats, tr.VAL_DAYS)
    return {"actual": {"deterministic": bool(np.array_equal(pa, pb)), "features": ds.FEATURES,
                       "val_flag_share_about_1pct": abs(flags - 0.01) < 0.002, "val_auc_above_0_8": auc > 0.8},
            "val_auc": round(auc, 4), "threshold": round(thr, 6), "train_s~": round(time.time() - t0, 1)}


def main():
    os.makedirs(OUT, exist_ok=True)
    t0 = time.time()
    data, _ = ds.generate()
    feats = ds.reference_features(data)
    sha_a = ds.write_csv(data, feats, OUT + "/transactions.csv", OUT + "/features.csv")
    again, _ = ds.generate()
    sha_b = ds.write_csv(again, ds.reference_features(again), OUT + "/transactions.again.csv", OUT + "/features.again.csv")
    os.remove(OUT + "/transactions.again.csv"); os.remove(OUT + "/features.again.csv")
    res = {"controls": controls(data, sha_a, sha_b), "reference": reference(data, feats),
           "labels": labels(data), "model": model(data, feats)}
    res["controls"]["generate_s~"] = round(time.time() - t0, 1)
    print(json.dumps(res, ensure_ascii=False, default=float))


if __name__ == "__main__":
    main()
