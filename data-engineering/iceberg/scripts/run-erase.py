"""Ось 3б: жизненный цикл удаления строки (jobs/sp_erase.py) — что остаётся
в хранилище после DELETE, после истечения снапшотов и после уплотнения.

После каждой фазы: строк удалённого заказа в текущем снапшоте, в самом старом
доступном снапшоте и физических копий в паркет-объектах таблицы (jobs/lake.py
physical — читает все файлы данных под префиксом, включая те, на которые
таблица уже не ссылается).

    python3 scripts/run-erase.py                  → оба режима, фикстура
    python3 scripts/run-erase.py merge-on-read    → один режим
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import DELETE, END_MARKER  # noqa: E402

PHASES = ("delete", "expire", "compact", "expire")


def measure(mode):
    steps = []
    for phase in PHASES:
        r = stack.spark("sp_erase", mode, phase)
        code = "from jobs import lake; print('N', lake.physical(%r, %d))" % (
            "shop/erase_%s/data" % mode.replace("-", "_"), r["victim"])
        _, out = stack.tools_py(code)
        r["physical"] = int([l for l in out.split("\n") if l.startswith("N ")][-1][2:])
        steps.append({k: r[k] for k in ("phase", "current", "oldest_snapshot", "physical", "snapshots",
                                        "delete_files")})
    gone = [i for i, st in enumerate(steps) if st["physical"] == 0]
    r = {"probe": "delete 1 order of 2026-09-03, then expire / compact / expire", "mode": mode, "steps": steps,
         "physical_after_delete": steps[0]["physical"], "physical_after_expire": steps[1]["physical"],
         "physical_after_compact_expire": steps[3]["physical"],
         "first_gone_after": " → ".join(PHASES[:gone[0] + 1]) if gone else None}
    r["outcome"] = "match" if steps[1]["physical"] == 0 else "mismatch"
    r["detail"] = "физических копий: после DELETE %d, после expire %d, после compact и expire %d; " \
                  "в старейшем снапшоте после DELETE %d" % (
                      steps[0]["physical"], steps[1]["physical"], steps[3]["physical"], steps[0]["oldest_snapshot"])
    return r


if __name__ == "__main__":
    common.run("erase", measure, ("copy-on-write", "merge-on-read"), DELETE, END_MARKER)
