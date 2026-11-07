"""Контракт строк стенда vector-databases/ann над родовым contract.py."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

# Совпал ли замер с гипотезой, записанной до прогона.
MATCH = Contract(outcomes=("match", "mismatch", "error"), success="match",
                 fields=("kind", "variant", "probe", "outcome", "detail", "cmd"))
END_MARKER = MATCH.END_MARKER
