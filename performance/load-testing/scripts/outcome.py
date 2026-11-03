"""Контракты строк стенда performance/load-testing — исходы осей над родовым contract.py."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Ось model: что генератор сделал с паузой сервиса — показал как есть (matches),
# спрятал (hides) или показал искажённо (differs). Контроль — match/mismatch.
MODEL = Contract(outcomes=("matches", "hides", "differs", "match", "mismatch", "error"), success="matches",
                 fields=_CMD)
# Остальные оси: совпал ли замер с расчётом или гипотезой.
MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match", fields=_CMD)

END_MARKER = MATCH.END_MARKER
