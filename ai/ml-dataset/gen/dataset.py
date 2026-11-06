"""Поток платежей серии ml-production и эталон признаков.

Сервис авторизации платежа спрашивает модель: похоже ли на мошенничество.
Всё детерминировано зерном SEED; вывод — канонический CSV, его sha256 —
идентичность датасета.

Сюжет данных по дням (день 0 — 01.05.2026, UTC):
- дни 0–119: обычные платежи, недельная сезонность (выходные — больше
  платежей и крупнее суммы), захваты аккаунта — всплески крупных сумм с
  нового устройства, часто из чужой страны;
- с дня 100: новый сегмент трафика — категория digital_goods, мелкие суммы,
  часть из Казахстана; доля нарастает 15 дней (сдвиг входных данных);
- с дня 105: новая схема мошенничества — проверка карты сериями мелких
  платежей со своего устройства (сдвиг связи признаков и метки).

Разметка приходит с задержкой: мошенничество — chargeback через 10–60 дней,
легитимный платёж считается подтверждённым по закрытию окна в 60 дней.

Эталон признаков — состояние ДО транзакции, по предыдущим платежам того же
пользователя; считается здесь, независимо от SQL и Feast в других стендах.
"""

import datetime as dt
import hashlib
import math

import numpy as np

SEED = 20261109
START = 1777593600  # 2026-05-01T00:00:00Z, пятница
DAYS = 120
N_USERS = 2000
DAY = 86400
SEGMENT_DAY = 100     # новый сегмент трафика
SCHEME_DAY = 105      # новая схема мошенничества
LEGIT_WINDOW = 60     # дней до подтверждения легитимного платежа

COUNTRIES = ["RU", "KZ", "AM", "TR", "AE"]
CATEGORIES = ["grocery", "electronics", "travel", "restaurants", "services", "digital_goods"]
BASE_CAT_P = [0.40, 0.10, 0.08, 0.25, 0.17]
# часы UTC: днём по Москве (UTC+3) платят чаще
HOUR_W = np.array([1, 1, 1, 2, 3, 5, 7, 8, 8, 8, 8, 8, 8, 8, 8, 9, 9, 9, 8, 6, 4, 2, 1, 1], float)
HOUR_P = HOUR_W / HOUR_W.sum()

FEATURES = ["amount", "tx_count_24h", "amount_avg_30d", "minutes_since_prev", "amount_ratio", "device_new", "foreign"]


def iso(ts):
    return dt.datetime.fromtimestamp(int(ts), dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def generate():
    rng = np.random.default_rng(SEED)
    home = rng.choice(["RU", "KZ", "AM"], size=N_USERS, p=[0.85, 0.10, 0.05])
    mu = rng.normal(7.0, 0.6, N_USERS)          # лог типичной суммы, ≈1100 ₽
    rate = rng.gamma(2.0, 0.6, N_USERS)         # платежей в день, в среднем 1.2
    cols = {k: [] for k in ("user", "ts", "amount", "cat", "country", "device_new", "fraud", "scheme", "segment")}

    def add(user, ts, amount, cat, country, device_new, fraud, scheme, segment):
        n = len(ts)
        cols["user"].append(np.full(n, user) if np.isscalar(user) else user)
        cols["ts"].append(ts)
        cols["amount"].append(np.round(amount, 2))
        cols["cat"].append(cat)
        cols["country"].append(country)
        cols["device_new"].append(device_new.astype(np.int8))
        cols["fraud"].append(np.full(n, fraud, np.int8) if np.isscalar(fraud) else fraud.astype(np.int8))
        cols["scheme"].append(np.full(n, scheme) if np.isscalar(scheme) else scheme)
        cols["segment"].append(np.full(n, segment))

    def times(day, n):
        return START + day * DAY + rng.choice(24, n, p=HOUR_P) * 3600 + rng.integers(0, 3600, n)

    for day in range(DAYS):
        weekend = (4 + day) % 7 in (5, 6)  # 01.05 — пятница (weekday 4)
        # обычные платежи
        k = rng.poisson(rate * (1.25 if weekend else 1.0))
        users = np.repeat(np.arange(N_USERS), k)
        n = len(users)
        amount = np.exp(rng.normal(mu[users] + (math.log(1.15) if weekend else 0.0), 0.5))
        cat = rng.choice(CATEGORIES[:5], n, p=BASE_CAT_P)
        abroad = rng.random(n) < 0.03
        country = np.where(abroad, rng.choice(COUNTRIES, n), home[users])
        noise = (rng.random(n) < 0.0005).astype(np.int8)
        add(users, times(day, n), amount, cat, country, rng.random(n) < 0.04, noise,
            np.where(noise == 1, "noise", "normal"), "base")
        # новый сегмент: мелкие цифровые покупки, доля растёт 15 дней
        if day >= SEGMENT_DAY:
            ramp = min(1.0, (day - SEGMENT_DAY + 1) / 15)
            k = rng.poisson(rate * 0.4 * ramp)
            users = np.repeat(np.arange(N_USERS), k)
            n = len(users)
            country = np.where(rng.random(n) < 0.5, "KZ", home[users])
            add(users, times(day, n), np.exp(rng.normal(5.5, 0.6, n)), np.full(n, "digital_goods"), country,
                rng.random(n) < 0.04, 0, "normal", "digital")
        # захват аккаунта: серия 3–8 крупных платежей за 2 часа
        for u in np.flatnonzero(rng.random(N_USERS) < 0.0015):
            n = int(rng.integers(3, 9))
            t0 = START + day * DAY + int(rng.integers(0, DAY - 7200))
            ts = np.sort(t0 + rng.integers(0, 7200, n))
            amount = np.exp(mu[u] + np.log(rng.uniform(3, 10, n)))
            cat = rng.choice(["electronics", "travel", "services"], n, p=[0.6, 0.3, 0.1])
            country = np.full(n, rng.choice(["TR", "AE", "KZ"]) if rng.random() < 0.35 else home[u])
            add(u, ts, amount, cat, country, np.full(n, rng.random() < 0.8), 1, "takeover", "base")
        # проверка карты: 10–20 мелких платежей за 30 минут со своего устройства
        if day >= SCHEME_DAY:
            ramp = min(1.0, (day - SCHEME_DAY + 1) / 10)
            for u in np.flatnonzero(rng.random(N_USERS) < 0.003 * ramp):
                n = int(rng.integers(10, 21))
                t0 = START + day * DAY + int(rng.integers(0, DAY - 1800))
                ts = np.sort(t0 + rng.integers(0, 1800, n))
                add(u, ts, rng.uniform(10, 150, n), np.full(n, "digital_goods"), np.full(n, home[u]),
                    np.zeros(n, bool), 1, "card_testing", "digital")

    data = {k: np.concatenate(v) for k, v in cols.items()}
    # порядок (пользователь, время); у одного пользователя время строго растёт
    order = np.lexsort((data["ts"], data["user"]))
    data = {k: v[order] for k, v in data.items()}
    ts, user = data["ts"].astype(np.int64), data["user"]
    for i in range(1, len(ts)):
        if user[i] == user[i - 1] and ts[i] <= ts[i - 1]:
            ts[i] = ts[i - 1] + 1
    data["ts"] = ts
    delay = np.where(data["fraud"] == 1, rng.uniform(10, 60, len(ts)) * DAY, LEGIT_WINDOW * DAY)
    data["label_at"] = ts + delay.astype(np.int64)
    data["home"] = home[user]
    # итоговый порядок — по времени, идентификаторы по нему
    order = np.lexsort((data["user"], data["ts"]))
    data = {k: v[order] for k, v in data.items()}
    data["tx_id"] = np.array(["t%06d" % (i + 1) for i in range(len(order))])
    data["user_id"] = np.array(["u%04d" % u for u in data["user"]])
    data["day"] = (data["ts"] - START) // DAY
    return data, home


def reference_features(data):
    """Признаки «до транзакции» по предыдущим платежам того же пользователя."""
    n = len(data["ts"])
    out = {k: np.full(n, np.nan) for k in ("tx_count_24h", "amount_avg_30d", "minutes_since_prev", "user_fraud_known")}
    for u in np.unique(data["user"]):
        idx = np.flatnonzero(data["user"] == u)          # уже по времени
        ts, amt = data["ts"][idx], data["amount"][idx]
        csum = np.concatenate([[0.0], np.cumsum(amt)])
        pos = np.arange(len(idx))
        lo24 = np.searchsorted(ts, ts - DAY, "left")
        lo30 = np.searchsorted(ts, ts - 30 * DAY, "left")
        out["tx_count_24h"][idx] = pos - lo24
        cnt30 = pos - lo30
        with np.errstate(invalid="ignore", divide="ignore"):
            out["amount_avg_30d"][idx] = np.where(cnt30 > 0, (csum[pos] - csum[lo30]) / np.maximum(cnt30, 1), np.nan)
        prev = np.concatenate([[np.nan], ts[:-1]])
        out["minutes_since_prev"][idx] = (ts - prev) / 60.0
        # мошенничество пользователя, о котором известно к моменту платежа
        known = np.sort(data["label_at"][idx][data["fraud"][idx] == 1])
        out["user_fraud_known"][idx] = np.searchsorted(known, ts, "left")
    out["amount_ratio"] = data["amount"] / out["amount_avg_30d"]
    out["device_new"] = data["device_new"].astype(float)
    out["foreign"] = (data["country"] != data["home"]).astype(float)
    out["amount"] = data["amount"].astype(float)
    out["tx_count_24h"] = out["tx_count_24h"].astype(float)
    return out


def brute_force(data, i):
    """Тот же признак перебором — для проверки векторного эталона."""
    u, t = data["user"][i], data["ts"][i]
    prior = [j for j in np.flatnonzero(data["user"] == u) if data["ts"][j] < t]
    c24 = sum(1 for j in prior if data["ts"][j] >= t - DAY)
    a30 = [data["amount"][j] for j in prior if data["ts"][j] >= t - 30 * DAY]
    return {"tx_count_24h": float(c24), "amount_avg_30d": (sum(a30) / len(a30)) if a30 else float("nan"),
            "minutes_since_prev": ((t - max(data["ts"][j] for j in prior)) / 60.0) if prior else float("nan"),
            "user_fraud_known": float(sum(1 for j in prior if data["fraud"][j] == 1 and data["label_at"][j] < t))}


def fmt(x):
    return "" if x != x else "%.6f" % x


def write_csv(data, feats, path_tx, path_feat):
    """Канонический вывод; возвращает sha256 обоих файлов."""
    h_tx, h_f = hashlib.sha256(), hashlib.sha256()
    with open(path_tx, "w", encoding="utf-8", newline="\n") as f:
        head = "tx_id,user_id,ts,amount,category,country,home_country,device_new,is_fraud,label_at,scheme,segment\n"
        f.write(head); h_tx.update(head.encode())
        for i in range(len(data["ts"])):
            line = "%s,%s,%s,%.2f,%s,%s,%s,%d,%d,%s,%s,%s\n" % (
                data["tx_id"][i], data["user_id"][i], iso(data["ts"][i]), data["amount"][i], data["cat"][i],
                data["country"][i], data["home"][i], data["device_new"][i], data["fraud"][i],
                iso(data["label_at"][i]), data["scheme"][i], data["segment"][i])
            f.write(line); h_tx.update(line.encode())
    with open(path_feat, "w", encoding="utf-8", newline="\n") as f:
        head = "tx_id," + ",".join(FEATURES) + ",user_fraud_known\n"
        f.write(head); h_f.update(head.encode())
        for i in range(len(data["ts"])):
            line = data["tx_id"][i] + "," + ",".join(fmt(feats[k][i]) for k in FEATURES + ["user_fraud_known"]) + "\n"
            f.write(line); h_f.update(line.encode())
    return h_tx.hexdigest(), h_f.hexdigest()
