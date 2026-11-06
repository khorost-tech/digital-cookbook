"""Базовая модель серии: градиентный бустинг на эталонных признаках.

Модель намеренно простая — предмет серии инфраструктура вокруг неё.
Обучение на днях 0–74, валидация на 75–89, порог — доля флагов 1 % на
валидации. Обучение однопоточное: так прогон воспроизводится побитово.
"""

import os

os.environ.setdefault("OMP_NUM_THREADS", "1")

import numpy as np  # noqa: E402
from sklearn.ensemble import HistGradientBoostingClassifier  # noqa: E402
from sklearn.metrics import roc_auc_score  # noqa: E402

from dataset import FEATURES  # noqa: E402

TRAIN_DAYS = (0, 75)
VAL_DAYS = (75, 90)
FLAG_SHARE = 0.01


def matrix(feats, mask, features=FEATURES):
    return np.column_stack([feats[k][mask] for k in features])


def train(data, feats, days=TRAIN_DAYS, features=FEATURES, seed=0):
    m = (data["day"] >= days[0]) & (data["day"] < days[1])
    model = HistGradientBoostingClassifier(max_iter=150, learning_rate=0.1, early_stopping=False, random_state=seed)
    model.fit(matrix(feats, m, features), data["fraud"][m])
    return model


def threshold(model, data, feats, days=VAL_DAYS, features=FEATURES):
    m = (data["day"] >= days[0]) & (data["day"] < days[1])
    return float(np.quantile(model.predict_proba(matrix(feats, m, features))[:, 1], 1 - FLAG_SHARE))


def auc(model, data, feats, days, features=FEATURES):
    m = (data["day"] >= days[0]) & (data["day"] < days[1])
    return float(roc_auc_score(data["fraud"][m], model.predict_proba(matrix(feats, m, features))[:, 1]))
