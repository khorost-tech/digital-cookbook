"""Ось 1: deadline и отмена в цепочке → fixtures/deadline.jsonl.

Клиент → звено A → исполнитель B; B работает 10 шагов по 100 мс и перед каждым
шагом проверяет отмену. A либо передаёт свой контекст дальше (propagate), либо
создаёт новый (Go — context.Background(), Java — Context.ROOT). Срок клиента —
300 мс; контроль — 3000 мс, больше всей работы. Счёт снимается у B после того,
как его цикл заведомо закончился: сколько шагов сделано и узнал ли он об отмене.

Все восемь сочетаний языков клиента, A и B. Перед замером — прогревочный вызов
по той же цепочке с длинным сроком и сброс счётчиков: холодный Java-A иначе
может не успеть позвать B за 300 мс. Сколько шагов B успеет до отмены, всё
равно разнится от прогона к прогону, — публикуется исход, а число шагов —
подробность строки.

    python3 scripts/run-deadline.py                          → все варианты, фикстура
    python3 scripts/run-deadline.py go java go propagate 300 → один вариант, строка JSON
"""

import itertools
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import DEADLINE, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "deadline.jsonl")
STEPS, STEP_MS = 10, 100
DEADLINES = (300, 3000)
MODES = ("propagate", "fresh")


def measure(cl, a, b, mode, deadline):
    deadline = int(deadline)
    A, B = "grpc-dl-a", "grpc-dl-b"
    docker.server(A, a)
    docker.server(B, b)
    try:
        # Прогрев: A открывает канал к B и прогревает JVM вызовом с длинным сроком;
        # иначе холодный Java-A мог не успеть позвать B за 300 мс (вызовов до B — 0).
        docker.client(cl, "chain", "-target", A + ":9090", "-next", B + ":9090", "-propagate=true",
                      "-deadline-ms", "10000", "-steps", "1", "-step-ms", "1")
        docker.reset(A)
        docker.reset(B)
        r = docker.client(cl, "chain", "-target", A + ":9090", "-next", B + ":9090",
                          "-propagate=%s" % ("true" if mode == "propagate" else "false"),
                          "-deadline-ms", str(deadline), "-steps", str(STEPS), "-step-ms", str(STEP_MS))
        time.sleep(STEPS * STEP_MS / 1000 + 0.5)  # цикл B заведомо закончился
        st = docker.stats(B)
    finally:
        docker.rm(A, B)
    row = {"kind": "deadline", "variant": "%s->%s->%s/%s/%dms" % (cl, a, b, mode, deadline),
           "probe": "steps%d-x%dms" % (STEPS, STEP_MS),
           "cmd": "python3 scripts/run-deadline.py %s %s %s %s %d" % (cl, a, b, mode, deadline),
           "client": cl, "a": a, "b": b, "mode": mode, "deadline_ms": deadline,
           "client_code": r["code"], "b_steps_done": st["steps_done"], "b_cancel_seen": st["cancel_seen"],
           "b_calls": st["chain_calls"]}
    if st["chain_calls"] != 1:
        row["outcome"], row["detail"] = "error", "до исполнителя дошло вызовов: %d" % st["chain_calls"]
    elif deadline >= STEPS * STEP_MS:  # контроль: срок больше работы
        ok = st["steps_done"] == STEPS and r["code"] == "OK"
        row["outcome"] = "completed" if ok else "error"
        row["detail"] = "B доделал все %d шагов, клиент получил OK" % STEPS if ok else \
            "контроль сломан: шагов %d, код %s" % (st["steps_done"], r["code"])
    elif st["steps_done"] < STEPS:
        row["outcome"] = "stopped"
        row["detail"] = "B узнал об отмене и бросил работу после %d шагов из %d" % (st["steps_done"], STEPS)
    else:
        row["outcome"] = "wasted"
        row["detail"] = "B доделал все %d шагов%s" % (
            STEPS, "" if st["cancel_seen"] else ", об отмене не узнал")
    DEADLINE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "client_code", "detail")


def main():
    docker.network()
    if sys.argv[1:]:
        row = measure(*sys.argv[1:6])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, a, b), mode, dl in itertools.product(itertools.product(docker.KINDS, repeat=3), MODES, DEADLINES):
        row = measure(cl, a, b, mode, dl)
        rows.append(row)
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
