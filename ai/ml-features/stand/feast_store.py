"""Обвязка Feast: репозиторий во временном каталоге, офлайн- и онлайн-хранилища на PostgreSQL."""

import datetime as dt
import importlib.util
import os
import shutil
import sys

import numpy as np
import pandas as pd
from feast import FeatureStore

ONLINE = ["user_state:tx_count_24h", "user_state:amount_avg_30d",
          "payment_features:amount_ratio", "payment_features:minutes_since_prev"]
NAMES = ["tx_count_24h", "amount_avg_30d", "amount_ratio", "minutes_since_prev"]

YAML = """project: payments
provider: local
registry: {registry}
entity_key_serialization_version: 3
offline_store:
  type: postgres
  host: postgres
  port: 5432
  database: payments
  db_schema: public
  user: feast
  password: local-fixture-only
  sslmode: disable
online_store:
  type: postgres
  host: postgres
  port: 5432
  database: payments
  db_schema: public
  user: feast
  password: local-fixture-only
  sslmode: disable
"""


def utc(ts):
    return dt.datetime.fromtimestamp(int(ts), dt.timezone.utc)


def make_store(src, work):
    if os.path.exists(work):
        shutil.rmtree(work)
    shutil.copytree(src, work)
    with open(os.path.join(work, "feature_store.yaml"), "w") as f:
        f.write(YAML.format(registry=os.path.join(work, "registry.db")))
    spec = importlib.util.spec_from_file_location("repo", os.path.join(work, "repo.py"))
    repo = importlib.util.module_from_spec(spec)
    sys.modules["repo"] = repo
    spec.loader.exec_module(repo)
    store = FeatureStore(repo_path=work)
    store.apply(repo.OBJECTS)
    return store


def as_float(v):
    return np.nan if v is None or (isinstance(v, float) and v != v) else float(v)


def online(store, user, ts, amount):
    r = store.get_online_features(features=ONLINE, entity_rows=[{"user_id": user, "amount": float(amount),
                                                                  "request_ts": int(ts)}]).to_dict()
    return {k: as_float(r[k][0]) for k in NAMES}


def historical(store, data, idx, shift_s, features):
    """PIT-выборка Feast для платежей idx; отметка сущности — время платежа минус shift_s."""
    edf = pd.DataFrame({"tx_id": data["tx_id"][idx], "user_id": data["user_id"][idx],
                        "event_timestamp": pd.to_datetime(data["ts"][idx] - shift_s, unit="s", utc=True),
                        "amount": data["amount"][idx].astype(float), "request_ts": data["ts"][idx].astype(np.int64)})
    df = store.get_historical_features(entity_df=edf, features=features).to_df()
    df = df.set_index("tx_id").loc[data["tx_id"][idx]]
    return df


def state_row(row):
    user, ts, c, a, last = row
    return pd.DataFrame({"user_id": [user], "event_timestamp": [ts], "tx_count_24h": [int(c)],
                         "amount_avg_30d": [np.nan if a is None else float(a)], "last_tx_ts": [int(last)]})
