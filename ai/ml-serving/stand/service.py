"""Модель серии как сервис BentoML. Вариант задаётся окружением:

FRAUD_MODEL     — путь к артефакту (joblib)
FRAUD_WORKERS   — число процессов-воркеров
FRAUD_BATCH     — 1: адаптивный батчинг (до 64 строк, бюджет задержки пачки max_latency_ms=20)
FRAUD_DELAY_MS  — искусственная задержка модели: «модель перегружена»
"""

import os
import time

import bentoml
import joblib
import numpy as np

WORKERS = int(os.environ.get("FRAUD_WORKERS", "1"))
BATCH = os.environ.get("FRAUD_BATCH", "0") == "1"
DELAY = float(os.environ.get("FRAUD_DELAY_MS", "0")) / 1000


@bentoml.service(workers=WORKERS, traffic={"timeout": 30})
class Fraud:
    def __init__(self):
        t0 = time.perf_counter()
        self.model = joblib.load(os.environ["FRAUD_MODEL"])
        self.load_s = time.perf_counter() - t0

    def _predict(self, rows):
        if DELAY:
            time.sleep(DELAY)
        return self.model.predict_proba(rows)[:, 1]

    if BATCH:
        @bentoml.api(batchable=True, max_batch_size=64, max_latency_ms=20)
        def score(self, rows: np.ndarray) -> np.ndarray:
            return self._predict(rows)
    else:
        @bentoml.api
        def score(self, rows: np.ndarray) -> np.ndarray:
            return self._predict(rows)

    @bentoml.api
    def load_time(self) -> float:
        return self.load_s
