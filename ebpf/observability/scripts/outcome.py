"""Контракты строк стенда ebpf/observability — исходы осей над родовым contract.py."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Сверка с эталоном генератора: совпал или нет.
MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match", fields=_CMD)
TRACES = PROFILES = COST = MATCH

END_MARKER = MATCH.END_MARKER
