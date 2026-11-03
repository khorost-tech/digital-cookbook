"""Контракты строк стенда ebpf/network-security — исходы осей над родовым contract.py."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match", fields=_CMD)
FILTER = TETRAGON = CILIUM = MATCH

END_MARKER = MATCH.END_MARKER
