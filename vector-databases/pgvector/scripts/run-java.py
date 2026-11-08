"""Ось java: те же 200 запросов через JDBC + pgvector-java и через pgx (Go).

    python3 scripts/run-java.py          → fixtures/java.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import outcome, run  # noqa: E402


def measure(_):
    stack.vbench("index", "-kind", "hnsw", "-m", "16", "-efc", "200", "-reuse")
    go = stack.vbench("dump", "-ef", "40")["ids"]
    java = stack.java_dump(40)["ids"]
    same = sum(1 for a, b in zip(go, java) if a == b)
    ok = same == len(go) == len(java)
    return {"variant": "jdbc", "probe": "SELECT id FROM docs ORDER BY embedding <#> ? LIMIT 10, hnsw.ef_search = 40: "
                                        "pgJDBC 42.7.14 + pgvector-java 0.1.6 против pgx 5.11.0 + pgvector-go 0.4.1",
            "tool_cmd": "java vdb.Dump /data/quora jdbc:postgresql://pg:5432/vdb 40",
            "queries": len(go), "same_order": same, "identical": ok, "outcome": outcome(ok),
            "detail": "выдача совпала id в id на %d запросах из %d" % (same, len(go))}


if __name__ == "__main__":
    run("java", measure, ["jdbc"])
