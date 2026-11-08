"""Ось ops: индекс vector_ip_ops и три оператора расстояния.

    python3 scripts/run-ops.py          → fixtures/ops.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import outcome, run  # noqa: E402


def measure(_):
    stack.vbench("index", "-kind", "hnsw", "-m", "16", "-efc", "200", "-reuse")
    o = stack.vbench("ops")
    uses = {k: "docs_hnsw" in o[k]["plan"] for k in ("l2", "cosine", "ip")}
    only_ip = uses == {"l2": False, "cosine": False, "ip": True}
    slower = min(o["l2"]["p50_us"], o["cosine"]["p50_us"]) > 10 * o["ip"]["p50_us"]
    ok = only_ip and slower
    return {"variant": "all", "probe": "HNSW по vector_ip_ops, hnsw.ef_search = 40, 100 запросов с каждым оператором",
            "tool_cmd": stack.command("ops"),
            "ops": {k: {"operator": o[k]["operator"], "plan": o[k]["plan"], "uses_index": uses[k],
                        "recall_at_10~": o[k]["recall_at_10"], "p50_us~": o[k]["p50_us"]} for k in ("l2", "cosine", "ip")},
            "only_ip_uses_index": only_ip, "others_10x_slower": slower, "outcome": outcome(ok),
            "detail": "; ".join("%s: %s, %d мс" % (o[k]["operator"], "индекс" if uses[k] else "без индекса",
                                                    round(o[k]["p50_us"] / 1000)) for k in ("l2", "cosine", "ip"))}


if __name__ == "__main__":
    run("ops", measure, ["all"])
