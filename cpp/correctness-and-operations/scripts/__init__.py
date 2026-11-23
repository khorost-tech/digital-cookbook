"""Пакет скриптов стенда `cpp/correctness-and-operations` (волна C серии cpp-deep-dive).

Как и у стенда волны B: `scripts/outcome.py` и `scripts/console.py` — тонкие
переходники к родовому модулю `cpp/_common`, а чтобы `from cpp._common import ...`
разрешался при запуске `python3 -m unittest discover -s scripts`, каталог,
содержащий `cpp/`, добавляется в sys.path здесь, до остальных импортов.
"""

import os
import sys

_STAND_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_REPO_ROOT = os.path.dirname(os.path.dirname(_STAND_DIR))
if _REPO_ROOT not in sys.path:
    sys.path.insert(0, _REPO_ROOT)

from scripts.console import force_utf8

force_utf8()
