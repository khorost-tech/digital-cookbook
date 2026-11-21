"""Ось qdrant — см. scripts/engine_axis.py.

    python3 scripts/run-qdrant.py          → fixtures/qdrant.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.engine_axis import main  # noqa: E402

if __name__ == "__main__":
    main("qdrant")
