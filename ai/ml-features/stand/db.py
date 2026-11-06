"""PostgreSQL — источник истины: платежи, батч-признаки для обучения, таблицы для Feast."""

import io
import os

import numpy as np
import psycopg

import dataset as ds

DSN = os.environ.get("DATABASE_URL", "postgresql://feast:local-fixture-only@postgres:5432/payments")

SCHEMA = """
CREATE TABLE tx (tx_id text PRIMARY KEY, user_id text NOT NULL, ts timestamptz NOT NULL, amount numeric(12,2) NOT NULL,
                 category text, country text, home_country text, device_new int, is_fraud int,
                 label_at timestamptz NOT NULL, scheme text, segment text);
CREATE INDEX tx_user_ts ON tx (user_id, ts);
"""

# Батч-путь обучения: признаки «до платежа» оконными функциями по истории.
BATCH_SQL = """
SELECT tx_id,
       count(*) OVER w24 AS tx_count_24h,
       avg(amount::float8) OVER w30 AS amount_avg_30d,
       extract(epoch FROM ts - lag(ts) OVER (PARTITION BY user_id ORDER BY ts)) / 60 AS minutes_since_prev
FROM tx
WINDOW w24 AS (PARTITION BY user_id ORDER BY ts RANGE BETWEEN interval '24 hours' PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW),
       w30 AS (PARTITION BY user_id ORDER BY ts RANGE BETWEEN interval '30 days' PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW)
"""

# Состояние пользователя ПОСЛЕ каждого платежа — источник Feast.
STATE_SQL = """
CREATE TABLE user_state AS
SELECT user_id, ts AS event_timestamp,
       count(*) OVER w24 AS tx_count_24h,
       avg(amount::float8) OVER w30 AS amount_avg_30d,
       extract(epoch FROM ts)::bigint AS last_tx_ts
FROM tx
WINDOW w24 AS (PARTITION BY user_id ORDER BY ts RANGE BETWEEN interval '24 hours' PRECEDING AND CURRENT ROW),
       w30 AS (PARTITION BY user_id ORDER BY ts RANGE BETWEEN interval '30 days' PRECEDING AND CURRENT ROW);
CREATE INDEX ON user_state (user_id, event_timestamp);
"""

# Накопленный счёт мошенничества: по времени платежа — со строкой самого платежа (строка на каждое
# мошенничество) и строго по предыдущим (строка на КАЖДЫЙ платёж: состояние до него), — и по времени,
# когда о нём узнали.
FRAUD_SQL = """
CREATE TABLE fraud_by_event AS
SELECT user_id, ts AS event_timestamp, count(*) OVER (PARTITION BY user_id ORDER BY ts) AS fraud_count
FROM tx WHERE is_fraud = 1;
CREATE TABLE fraud_by_event_prior AS
SELECT user_id, ts AS event_timestamp,
       coalesce(sum(is_fraud) OVER (PARTITION BY user_id ORDER BY ts ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0)
         AS fraud_count
FROM tx;
CREATE TABLE fraud_by_label AS
SELECT user_id, label_at AS event_timestamp, count(*) OVER (PARTITION BY user_id ORDER BY label_at, tx_id) AS fraud_count
FROM tx WHERE is_fraud = 1;
"""

# Один путь на обучение и инференс: функция над сырыми платежами, без хранилища признаков.
ONE_PATH_SQL = """
CREATE TABLE tx_live (LIKE tx INCLUDING ALL);
CREATE FUNCTION features_before(p_user text, p_ts timestamptz, OUT tx_count_24h bigint, OUT amount_avg_30d float8,
                                OUT minutes_since_prev float8)
LANGUAGE sql STABLE AS $$
  SELECT (SELECT count(*) FROM tx_live WHERE user_id = p_user AND ts >= p_ts - interval '24 hours' AND ts < p_ts),
         (SELECT avg(amount::float8) FROM tx_live WHERE user_id = p_user AND ts >= p_ts - interval '30 days' AND ts < p_ts),
         (SELECT extract(epoch FROM p_ts - max(ts)) / 60 FROM tx_live WHERE user_id = p_user AND ts < p_ts)
$$;
"""


def connect():
    return psycopg.connect(DSN, autocommit=True)


def load(conn, data):
    conn.execute(SCHEMA)
    buf = io.StringIO()
    for i in range(len(data["ts"])):
        buf.write("%s\t%s\t%s\t%.2f\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n" % (
            data["tx_id"][i], data["user_id"][i], ds.iso(data["ts"][i]), data["amount"][i], data["cat"][i],
            data["country"][i], data["home"][i], data["device_new"][i], data["fraud"][i], ds.iso(data["label_at"][i]),
            data["scheme"][i], data["segment"][i]))
    with conn.cursor().copy("COPY tx FROM STDIN") as cp:
        cp.write(buf.getvalue())
    conn.execute("ANALYZE tx")


def batch_features(conn, data):
    """Батч-признаки в порядке data; amount_ratio, device_new, foreign — как в эталоне."""
    idx = {t: i for i, t in enumerate(data["tx_id"])}
    n = len(idx)
    out = {k: np.full(n, np.nan) for k in ("tx_count_24h", "amount_avg_30d", "minutes_since_prev")}
    for tx_id, c, a, m in conn.execute(BATCH_SQL):
        i = idx[tx_id]
        out["tx_count_24h"][i] = c
        out["amount_avg_30d"][i] = np.nan if a is None else a
        out["minutes_since_prev"][i] = np.nan if m is None else float(m)
    return complete(data, out)


def complete(data, out):
    out["amount"] = data["amount"].astype(float)
    out["amount_ratio"] = data["amount"] / out["amount_avg_30d"]
    out["device_new"] = data["device_new"].astype(float)
    out["foreign"] = (data["country"] != data["home"]).astype(float)
    return out
