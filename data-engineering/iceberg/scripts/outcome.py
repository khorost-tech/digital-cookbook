"""Контракты строк стенда iceberg — исходы осей над родовым contract.py.

`error` отделён везде: прогон, который не смог поставить опыт (DAG не
запустился, стек не поднялся), не попадает в таблицу как свойство пайплайна.
У каждой строки — `cmd`: команда, которой читатель повторит замер.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Совпал ли исход опыта с тем, что обещает таблица (эталон — исходные данные).
MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match", fields=_CMD)
WRITERS = READER = DELETE = SCHEMA = PARTITIONS = MAINTENANCE = MATCH

END_MARKER = MATCH.END_MARKER
