"""Контракты строк стенда ebpf/fundamentals — исходы осей над родовым contract.py.

`error` отделён: прогон, который не смог поставить опыт (программа не
загрузилась там, где должна была), не попадает в таблицу как свойство eBPF.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Сверка счёта программы с эталоном нагрузки: совпал или нет.
MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match", fields=_CMD)
HOOKS = MAPS = VERIFIER = CORE = BPFTRACE = MATCH

END_MARKER = MATCH.END_MARKER
