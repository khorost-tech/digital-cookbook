# Определения Feast: одна сущность, состояние пользователя после каждого платежа
# и признаки, которые считаются в момент запроса из этого состояния и самого платежа.
from datetime import timedelta

import pandas as pd
from feast import Entity, FeatureView, Field, RequestSource
from feast.infra.offline_stores.contrib.postgres_offline_store.postgres_source import PostgreSQLSource
from feast.on_demand_feature_view import on_demand_feature_view
from feast.types import Float64, Int64

user = Entity(name="user", join_keys=["user_id"])

# строка на каждый платёж: состояние ПОСЛЕ него, отметка времени — время платежа
user_state = FeatureView(
    name="user_state",
    entities=[user],
    ttl=timedelta(days=7),
    schema=[Field(name="tx_count_24h", dtype=Int64), Field(name="amount_avg_30d", dtype=Float64),
            Field(name="last_tx_ts", dtype=Int64)],
    source=PostgreSQLSource(name="user_state_src", query="SELECT * FROM user_state", timestamp_field="event_timestamp"),
    online=True,
)

payment = RequestSource(name="payment", schema=[Field(name="amount", dtype=Float64), Field(name="request_ts", dtype=Int64)])


@on_demand_feature_view(sources=[user_state, payment],
                        schema=[Field(name="amount_ratio", dtype=Float64), Field(name="minutes_since_prev", dtype=Float64)])
def payment_features(df: pd.DataFrame) -> pd.DataFrame:
    # одна функция на оба пути: Feast вызывает её и при сборке выборки, и при инференсе
    out = pd.DataFrame()
    out["amount_ratio"] = df["amount"] / df["amount_avg_30d"]
    out["minutes_since_prev"] = (df["request_ts"] - df["last_tx_ts"]) / 60.0
    return out


# мошенничество пользователя накопленным счётом: два источника с разной отметкой времени
def fraud_view(name, table):
    return FeatureView(
        name=name, entities=[user], ttl=timedelta(0),
        schema=[Field(name="fraud_count", dtype=Int64)],
        source=PostgreSQLSource(name=table + "_src", query="SELECT * FROM " + table, timestamp_field="event_timestamp"),
        online=False,
    )


fraud_by_event = fraud_view("fraud_by_event", "fraud_by_event")   # время самого платежа
fraud_by_event_prior = fraud_view("fraud_by_event_prior", "fraud_by_event_prior")  # то же, без самого платежа
fraud_by_label = fraud_view("fraud_by_label", "fraud_by_label")   # время, когда пришёл chargeback

OBJECTS = [user, user_state, payment_features, fraud_by_event, fraud_by_event_prior, fraud_by_label]
