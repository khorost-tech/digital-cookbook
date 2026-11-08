"""Загрузка артефакта модели под другой версией scikit-learn (запускается интерпретатором старого окружения).

    <python> old_sklearn.py <model.skops> <X.npy> <out.npy>

Печатает одну строку JSON: версия, исход загрузки, первые строки предупреждений и ошибки.
"""

import json
import sys
import warnings

import numpy as np
import sklearn
from skops.io import get_untrusted_types, load

BASE_TRUST = ["sklearn.ensemble._hist_gradient_boosting.predictor.TreePredictor"]

path, x_path, out_path = sys.argv[1:4]
res = {"sklearn": sklearn.__version__, "warnings": []}
# шаг 1 — тот же список доверия, что при сохранении; шаг 2 — плюс типы самого scikit-learn, о которых сообщил skops
try:
    load(path, trusted=BASE_TRUST)
    res["same_trust_list"] = "loaded"
except Exception as e:  # noqa: BLE001
    res["same_trust_list"] = "error"
    res["same_trust_error"] = (type(e).__name__ + ": " + str(e)).splitlines()[0][:300]
extra = [t for t in get_untrusted_types(file=path) if t.startswith("sklearn.") and t not in BASE_TRUST]
res["extra_trusted"] = extra
with warnings.catch_warnings(record=True) as caught:
    warnings.simplefilter("always")
    try:
        model = load(path, trusted=BASE_TRUST + extra)
        np.save(out_path, model.predict_proba(np.load(x_path))[:, 1])
        res["outcome"] = "loaded"
    except Exception as e:  # noqa: BLE001 — исход и текст ошибки и есть измерение
        res["outcome"] = "error"
        res["error"] = (type(e).__name__ + ": " + str(e)).splitlines()[0][:300]
res["warnings"] = sorted({(w.category.__name__ + ": " + str(w.message)).splitlines()[0][:300] for w in caught})
print(json.dumps(res, ensure_ascii=False))
