"""Ось 4: разрыв и реконнект → fixtures/reconnect.jsonl.

  gap <шина>        — подписка инстанса A на шину рвётся: между A и шиной стоит
                      рвущий прокси пробы; инстанс B публикует в шину напрямую.
                      5 событий до разрыва, 5 во время (публикуются, только когда
                      A сам сообщил bus_state=down), 5 после восстановления.
                      Шины: redis (pub/sub), redis-streams, nats (core), jetstream.
  resume <история>  — SSE-клиент рвётся и возвращается с Last-Event-ID; 5 событий
                      до, 5 в разрыв, 5 после. История: none (её нет), redis-streams,
                      jetstream; *-barrier — сервер прочитал историю и ждёт, «после»
                      публикуется в это время; *-barrier-late — контроль с подпиской
                      после истории; redis-streams-history-error — чтение истории
                      падает (инъекция RT_HISTORY_FAIL).
  cancel            — клиент приходит с Last-Event-ID и уходит, пока сервер читает
                      историю (пауза 1,5 с): подписчик обязан исчезнуть из хаба.
  storm <джиттер>   — 200 клиентов WS на одном инстансе, инстанс перезапускается;
                      пауза переподключения 200 мс плюс случайная добавка до
                      джиттера (0 или 2000 мс). Счёт — по корзинам 100 мс.

    python3 scripts/run-reconnect.py                       → все варианты, фикстура
    python3 scripts/run-reconnect.py go gap redis          → один вариант, строка JSON
"""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import CANCEL, END_MARKER, RECONNECT, STORM  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "reconnect.jsonl")
A, B, BPROXY, REDIS, NATS = "rt-rc-a", "rt-rc-b", "rt-rc-bproxy", "rt-rc-redis", "rt-rc-nats"
SERVERS = ("go", "java")
GAP_BUSES = ("redis", "redis-streams", "nats", "jetstream")
HISTORIES = ("none", "redis-streams", "jetstream", "redis-streams-barrier", "jetstream-barrier",
             "redis-streams-barrier-late", "redis-streams-history-error")
# вариант resume → (шина, env сервера, публиковать на стыке)
RESUME = {
    "none": ("none", {}, False),
    "redis-streams": ("redis-streams", {}, False),
    "jetstream": ("jetstream", {}, False),
    # стык: сервер прочитал историю и стоит на барьере; проба публикует «после»
    # и отпускает — прийти эти события могут только по живой подписке
    "redis-streams-barrier": ("redis-streams", {"RT_HISTORY_BARRIER": "1"}, True),
    "jetstream-barrier": ("jetstream", {"RT_HISTORY_BARRIER": "1"}, True),
    # контроль: та же постановка, но подписка после истории — опыт обязан потерять
    "redis-streams-barrier-late": ("redis-streams", {"RT_HISTORY_BARRIER": "1", "RT_SUBSCRIBE_LATE": "1"}, True),
    # чтение истории падает (инъекция): клиент обязан получить отказ, а не поток с пропуском
    "redis-streams-history-error": ("redis-streams", {"RT_HISTORY_FAIL": "1"}, False),
}
JITTERS = (0, 2000)
NB = ND = NA = 5
K_STORM = 200


def stop():
    docker.rm(A, B, BPROXY, REDIS, NATS)


def infra(bus):
    if bus.startswith("redis"):
        docker.redis(REDIS)
        return REDIS + ":6379"
    if bus in ("nats", "jetstream"):
        docker.nats(NATS)
        return NATS + ":4222"
    return None


def bus_env(bus, addr, topic):
    env = {"RT_BUS": bus, "RT_TOPICS": topic}
    if bus.startswith("redis"):
        env["RT_REDIS"] = addr
    elif addr:
        env["RT_NATS"] = "nats://" + addr
    return env


def gap(server, bus):
    stop()
    direct = infra(bus)
    docker.proxy(BPROXY, direct)
    topic = docker.topic("rg")
    docker.server(A, server, bus_env(bus, BPROXY + ":9000", topic))   # подписчик — через рвущий прокси
    docker.server(B, server, bus_env(bus, direct, topic))             # издатель — напрямую
    r = docker.probe("gap", "-transport", "ws", "-url", "http://%s:8080" % A, "-publish", "http://%s:8080" % B,
                     "-health", "http://%s:8080/healthz" % A, "-control", "http://%s:9100" % BPROXY,
                     "-topic", topic, "-n-before", str(NB), "-n-during", str(ND), "-n-after", str(NA),
                     "-wait", "15s")
    row = {"kind": "gap", "variant": "%s/gap/%s" % (server, bus), "probe": "nb%d-nd%d-na%d" % (NB, ND, NA),
           "cmd": "python3 scripts/run-reconnect.py %s gap %s" % (server, bus), "server": server, "bus": bus, **r}
    staged = (r["received_before_cut"] and r["bus_down_before_publish"] and r["bus_down_after_publish"]
              and r["bus_up_again"] and r["publish_errors"] == 0)
    if not staged:
        row["outcome"] = "error"
        row["detail"] = "разрыв не поставлен: до %s, down до/после %s/%s, восстановлен %s, ошибок публикации %d" % (
            r["received_before_cut"], r["bus_down_before_publish"], r["bus_down_after_publish"],
            r["bus_up_again"], r["publish_errors"])
    elif r["missing"] == 0:
        row["outcome"], row["detail"] = "no-loss", "все %d дошли, в том числе %d из разрыва" % (NB + ND + NA, ND)
    else:
        row["outcome"] = "lost"
        row["detail"] = "потеряно %d, из них в разрыв %d из %d" % (r["missing"], r["missing_during_cut"], ND)
    RECONNECT.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def resume(server, history):
    stop()
    bus, extra, overlap = RESUME[history]
    addr = infra(bus)
    topic = docker.topic("rr")
    docker.server(A, server, dict(bus_env(bus, addr, topic), **extra))
    args = ["sse-resume", "-url", "http://%s:8080" % A, "-topic", topic,
            "-n-before", str(NB), "-n-during", str(ND), "-n-after", str(NA), "-wait", "10s"]
    if overlap:
        args.append("-overlap")
    r = docker.probe(*args)
    row = {"kind": "resume", "variant": "%s/resume/%s" % (server, history), "probe": "nb%d-nd%d-na%d" % (NB, ND, NA),
           "cmd": "python3 scripts/run-reconnect.py %s resume %s" % (server, history),
           "server": server, "history": history, **r}
    if r["publish_errors"] or r["last_event_id_sent"] != NB:
        row["outcome"] = "error"
        row["detail"] = "опыт не поставлен: Last-Event-ID %s, ошибок публикации %d" % (
            r["last_event_id_sent"], r["publish_errors"])
    elif "503" in r["resume_result"]:
        row["outcome"] = "refused"
        row["detail"] = "догон отказан (503); событий после разрыва дошло: %d" % (ND + NA - r["missing"])
    elif r["missing"] == 0 and r["dups"] == 0:
        row["outcome"], row["detail"] = "no-loss", "догнал все %d из разрыва, дублей нет" % ND
    elif r["missing"] == 0:
        row["outcome"], row["detail"] = "duplicated", "пропусков нет, но дублей %d" % r["dups"]
    else:
        row["outcome"], row["detail"] = "lost", "потеряно %d из %d событий после разрыва" % (r["missing"], ND + NA)
    RECONNECT.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def cancel(server):
    stop()
    docker.redis(REDIS)
    topic = docker.topic("rc")
    docker.server(A, server, dict(bus_env("redis-streams", REDIS + ":6379", topic), RT_HISTORY_DELAY_MS="1500"))
    r = docker.probe("cancel-resume", "-url", "http://%s:8080" % A, "-topic", topic, "-wait", "5s")
    row = {"kind": "cancel", "variant": "%s/cancel" % server, "probe": "delay1500ms",
           "cmd": "python3 scripts/run-reconnect.py %s cancel" % server, "server": server, **r}
    if r["conns_during"] <= r["conns_before"]:
        row["outcome"], row["detail"] = "error", "соединение во время чтения истории не учтено — опыт не поставлен"
    elif r["conns_after"] == r["conns_before"]:
        row["outcome"], row["detail"] = "released", "подписчик снят: соединений снова %d" % r["conns_before"]
    else:
        row["outcome"] = "leaked"
        row["detail"] = "подписчик остался: соединений %d вместо %d" % (r["conns_after"], r["conns_before"])
    CANCEL.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def storm(server, jitter):
    jitter = int(jitter)
    stop()
    docker.server(A, server)
    p = docker.probe_interactive("storm", "-url", "http://%s:8080" % A, "-topic", docker.topic("rs"),
                                 "-clients", str(K_STORM), "-backoff", "200ms", "-jitter", "%dms" % jitter,
                                 "-wait", "30s")
    ready = p.stdout.readline().strip()
    if ready != "READY":
        p.kill()
        raise SystemExit("storm: проба не готова: %r" % ready)
    docker.sh("docker", "restart", "-t", "0", A)
    out, _ = p.communicate(timeout=120)
    r = json.loads([l for l in out.splitlines() if l.startswith("{")][-1])
    row = {"kind": "storm", "variant": "%s/storm/jitter-%dms" % (server, jitter), "probe": "k%d" % K_STORM,
           "cmd": "python3 scripts/run-reconnect.py %s storm %d" % (server, jitter),
           "server": server, "nondeterministic": True, **r}
    if r["reconnected"] != K_STORM:
        row["outcome"], row["detail"] = "error", "вернулись %d из %d" % (r["reconnected"], K_STORM)
    elif r["max_reconnects_per_100ms"] > K_STORM // 2:
        row["outcome"] = "burst"
        row["detail"] = "в худшей корзине 100 мс — %d переподключений из %d" % (r["max_reconnects_per_100ms"], K_STORM)
    else:
        row["outcome"] = "spread"
        row["detail"] = "в худшей корзине 100 мс — %d из %d, корзин с переподключениями %d" % (
            r["max_reconnects_per_100ms"], K_STORM, r["buckets_with_reconnects"])
    STORM.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def plan():
    for server in SERVERS:
        for bus in GAP_BUSES:
            yield gap, server, bus
        for h in HISTORIES:
            yield resume, server, h
        yield cancel, server
        for j in JITTERS:
            yield storm, server, j


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    args = sys.argv[1:]
    try:
        if args:
            fn = {"gap": gap, "resume": resume, "cancel": cancel, "storm": storm}.get(args[1] if len(args) > 1 else "")
            if fn is None or args[0] not in SERVERS:
                raise SystemExit(__doc__)
            row = fn(args[0], *args[2:])
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
            return
        docker.build()
        rows = []
        for fn, *a in plan():
            row = fn(*a)
            rows.append(row)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    finally:
        stop()
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
