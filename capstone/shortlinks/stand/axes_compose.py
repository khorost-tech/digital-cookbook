"""Оси статей 1 и 2 на локальном стеке: конфигурация, остановка, ошибки, кэш, брокер, outbox, consumer."""

import json
import statistics
import threading
import time

import stack as s

SERVICE = "capstone-service-1"


def config():
    """Три ошибки конфигурации сразу — код выхода, все в одном сообщении, порт не открыт."""
    p = s.sh(["docker", "run", "--rm", "-e", "PUBLISH_MODE=fast", "-e", "DB_MAX_CONNS=0", "shortlinks-service:dev"], check=False)
    lines = [json.loads(l) for l in p.stdout.splitlines() if l.startswith("{")]
    err = " ".join(l.get("err", "") for l in lines)
    s.up()
    return {"actual": {"invalid_exit_code": p.returncode,
                       "all_errors_reported": all(k in err for k in ("DATABASE_URL", "PUBLISH_MODE", "DB_MAX_CONNS")),
                       "port_never_opened": not any("сервис готов" in l.get("msg", "") for l in lines),
                       "valid_ready": s.status("/readyz") == 200},
            "message": err}


def _inflight(graceful):
    s.up({"SLOW_REDIRECT": "500ms", "GRACEFUL": str(graceful).lower()})
    code = s.create()
    s.request("/" + code)
    out = {}

    def fire():
        out["res"] = s.burst(["/" + code] * 20, 20)
    t = threading.Thread(target=fire)
    t.start()
    time.sleep(0.15)
    s.sh(["docker", "kill", "-s", "SIGTERM", SERVICE])
    t.join()
    ok = sum(1 for r in out["res"] if r[0] == 302)
    return {"inflight_ok": ok, "inflight_failed": 20 - ok}


def _drain(broker_up):
    """SIGTERM при полном буфере кликов: укладывается ли остановка в SHUTDOWN_TIMEOUT, если брокер недоступен."""
    s.up({"PUBLISH_BUFFER": "100", "SHUTDOWN_TIMEOUT": "5s"})
    code = s.create()
    s.request("/" + code)
    if not broker_up:
        s.compose("stop", "nats")
    try:
        for r in s.burst(["/" + code] * 300, 20):
            assert r[0] == 302, r
        n0 = len(s.sh(["docker", "logs", SERVICE]).stdout.splitlines())   # строки прошлых запусков того же контейнера
        t0 = time.time()
        s.sh(["docker", "kill", "-s", "SIGTERM", SERVICE])
        s.sh(["docker", "wait", SERVICE], timeout=300)
        took = time.time() - t0
        logs = [json.loads(l) for l in s.sh(["docker", "logs", SERVICE]).stdout.splitlines()[n0:] if l.startswith("{")]
    finally:
        if not broker_up:
            s.compose("start", "nats")
    unsent = sum(int(l.get("unsent", 0)) for l in logs if "срока остановки" in l.get("msg", ""))
    clean = any(l.get("msg") == "остановлен штатно" for l in logs)
    return took, unsent, clean


def _db_blocked():
    """Запрос ждёт PostgreSQL дольше срока остановки: укладывается ли остановка в SHUTDOWN_TIMEOUT."""
    s.up({"SHUTDOWN_TIMEOUT": "3s"})
    locker = threading.Thread(target=lambda: s.psql("BEGIN; LOCK TABLE links IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(20); COMMIT;"))
    locker.start()
    time.sleep(1)
    res = {}
    client = threading.Thread(target=lambda: res.update(r=s.request("/links", "POST", {"url": "https://example.com/locked"}, timeout=30)))
    client.start()
    time.sleep(1)
    blocked = client.is_alive()                               # контроль: запрос действительно ждёт базу
    n0 = len(s.sh(["docker", "logs", SERVICE]).stdout.splitlines())
    t0 = time.time()
    s.sh(["docker", "kill", "-s", "SIGTERM", SERVICE])
    s.sh(["docker", "wait", SERVICE], timeout=120)
    took = time.time() - t0
    logs = [json.loads(l) for l in s.sh(["docker", "logs", SERVICE]).stdout.splitlines()[n0:] if l.startswith("{")]
    client.join()
    locker.join()
    aborted = any("незавершённые запросы прерваны" in l.get("msg", "") for l in logs)
    return {"request_was_blocked": blocked, "exit_within_timeout": took < 4.5, "aborted_logged": aborted,
            "client_got_no_success": res["r"][0] != 201}, round(took, 1)


def _collector(up):
    """Невыгруженные спаны и SIGTERM: укладывается ли выгрузка трейсов в срок остановки, если Collector не отвечает."""
    (s.ROOT / "out/otel").mkdir(parents=True, exist_ok=True)
    env = {"SHUTDOWN_TIMEOUT": "4s", "OTEL_ENDPOINT": "http://otelcol:4318" if up else "http://10.255.255.1:4318"}
    s.compose(*(["--profile", "obs"] if up else []), "up", "-d", "--wait", env=env)
    s.wait_ready()
    code = s.create()
    for _ in range(20):                                         # спаны ждут пакетной отправки в процессоре
        s.request("/" + code)
    n0 = len(s.sh(["docker", "logs", SERVICE]).stdout.splitlines())
    t0 = time.time()
    s.sh(["docker", "kill", "-s", "SIGTERM", SERVICE])
    s.sh(["docker", "wait", SERVICE], timeout=300)
    took = time.time() - t0
    logs = [json.loads(l) for l in s.sh(["docker", "logs", SERVICE]).stdout.splitlines()[n0:] if l.startswith("{")]
    if up:
        s.compose("--profile", "obs", "stop", "otelcol", check=False)
    warned = any("трейсы не выгружены" in l.get("msg", "") for l in logs)
    return {"exit_within_timeout": took < 4.5, "unexported_logged": warned}, round(took, 1)


def shutdown():
    graceful = _inflight(True)
    abrupt = _inflight(False)
    # пауза после снятия готовности: /readyz 503, а запросы ещё обслуживаются
    s.up({"SHUTDOWN_DELAY": "2s"})
    code = s.create()
    before = s.clicks(code)
    made = 200
    for r in s.burst(["/" + code] * made, 20):
        assert r[0] == 302, r
    s.sh(["docker", "kill", "-s", "SIGTERM", SERVICE])
    time.sleep(0.3)
    ready = s.status("/readyz")
    during = [s.request("/" + code)[0] for _ in range(10)]
    time.sleep(3)
    s.up()                                        # сервис снова поднят — статистика доступна
    counted = s.wait_clicks(code, before + made + 10, timeout=15)
    # общий срок остановки: полный буфер и недоступный брокер; контроль — брокер доступен. Последними в оси:
    # после перезапуска NATS consumer молчит до пропуска heartbeat (статья 2) — его пересоздаём для следующих осей
    blocked, blocked_took = _db_blocked()
    d_took, d_unsent, d_clean = _drain(False)
    c_took, c_unsent, c_clean = _drain(True)
    s.compose("up", "-d", "--force-recreate", "consumer")
    # выгрузка трейсов в том же сроке: Collector не отвечает; контроль — Collector работает
    coll_down, coll_down_took = _collector(False)
    coll_up, coll_up_took = _collector(True)
    s.up()
    return {"actual": {"graceful": graceful, "abrupt": {"inflight_ok": abrupt["inflight_ok"]},
                       "readyz_503_during_delay": ready == 503,
                       "requests_during_delay_ok": all(c == 302 for c in during),
                       "buffered_clicks_flushed": counted == before + made + len(during),
                       "broker_down_full_buffer": {"exit_within_timeout": d_took < 6.5, "unsent_counted": d_unsent > 0,
                                                   "clean_exit": d_clean},
                       "broker_up_full_buffer": {"unsent_zero": c_unsent == 0, "clean_exit": c_clean},
                       "db_blocked_request": blocked,
                       "collector_down": coll_down,
                       "collector_up": {"exit_within_timeout": coll_up["exit_within_timeout"], "no_warning": not coll_up["unexported_logged"]}},
            "drain_s~": {"broker_down": round(d_took, 1), "broker_up": round(c_took, 1), "db_blocked": blocked_took,
                         "collector_down": coll_down_took, "collector_up": coll_up_took}, "unsent_broker_down~": d_unsent,
            "abrupt_failed": abrupt["inflight_failed"], "clicks_counted": counted, "clicks_made": before + made + len(during)}


def errors():
    s.up()
    res = {"invalid_url": s.request("/links", "POST", {"url": "ftp://x"})[0],
           "bad_json": s.request("/links", "POST", "{not json")[0],
           "unknown_code": s.request("/zzzzzzz")[0]}
    cached = s.create()
    s.request("/" + cached)
    uncached = s.create("https://example.com/other")
    s.compose("stop", "postgres")
    try:
        res["db_down_cached_redirect"] = s.request("/" + cached)[0]
        code, body, _ = s.request("/" + uncached)
        res["db_down_uncached"] = code
        res["internal_details_hidden"] = not any(w in body.lower() for w in ("pg", "dial", "connect", "postgres", "tcp"))
    finally:
        s.compose("start", "postgres")
    return {"actual": res, "uncached_body": body}


def cache():
    def stampede(single):
        s.up({"SINGLEFLIGHT": str(single).lower(), "SLOW_LOAD": "50ms"})
        code = s.create()
        before = s.metrics()["db_link_reads_total"]
        codes, _ = s.probe("/" + code, 200, 200)
        cold = s.metrics()["db_link_reads_total"] - before
        before = s.metrics()["db_link_reads_total"]
        codes2, _ = s.probe("/" + code, 200, 200)
        warm = s.metrics()["db_link_reads_total"] - before
        return cold, warm, codes.get(302, 0) == 200 and codes2.get(302, 0) == 200
    c1, w1, ok1 = stampede(True)
    c2, w2, ok2 = stampede(False)
    # срок общей загрузки и выход ожидающего клиента по своему контексту — модульные тесты internal/cache
    from axes_build import retry
    img = json.loads((s.ROOT / "images.json").read_text())["golang"]
    go = ["docker", "run", "--rm", "-v", str(s.ROOT) + ":/src", "-v", "capstone-gomod:/go/pkg/mod", "-w", "/src", img["ref"] + "@" + img["digest"], "go"]
    retry(go + ["mod", "download"], timeout=900)              # сбой сети при скачивании модулей — не результат опыта
    unit = s.sh(go + ["test", "-count=1", "-run", "TestWaiterLeavesSharedLoadContinues|TestSharedLoadHasOwnDeadline", "./internal/cache"],
                check=False, timeout=900)
    # SLOW_LOAD=50ms: без задержки результат решал планировщик (revision_stage5_after_session1)
    return {"actual": {"singleflight_db_reads": int(c1), "no_singleflight_every_request_reads": c2 == 200,
                       "warm_db_reads": int(w1 + w2), "all_redirects_ok": ok1 and ok2,
                       "waiter_cancel_and_load_deadline_tests_pass": unit.returncode == 0},
            "singleflight_db_reads~": int(c1), "no_singleflight_db_reads~": int(c2)}


def broker_down():
    def run(mode, n, workers):
        s.up({"PUBLISH_MODE": mode, "PUBLISH_BUFFER": "100"})
        code = s.create()
        s.request("/" + code)
        time.sleep(1)
        m0 = s.metrics()
        s.compose("stop", "nats")
        try:
            res = s.burst(["/" + code] * n, workers)
            m1 = s.metrics()
        finally:
            s.compose("start", "nats")
        lat = [r[2] for r in res]
        d = {k: m1.get(k, 0) - m0.get(k, 0) for k in ("clicks_dropped_total", "clicks_published_total", "clicks_publish_failed_total")}
        return res, statistics.median(lat), d, m1.get("clicks_buffered", 0)
    res, med, d, buffered = run("async", 300, 20)
    async_ = {"redirects_ok_share_1": all(r[0] == 302 for r in res), "fast": med < 0.1, "dropped_over_0": d["clicks_dropped_total"] > 0,
              "loss_visible_in_metric": d["clicks_dropped_total"] + d["clicks_published_total"] + d["clicks_publish_failed_total"] + buffered
              >= 300 - 1}
    res_s, med_s, d_s, _ = run("sync", 10, 5)
    sync = {"redirects_ok_share_1": all(r[0] == 302 for r in res_s), "fast": med_s < 0.1}
    # consumer после перезапуска брокера: сколько он молчит, пока не переподпишется
    resume = {e: resume_after_restart(e) for e in ("30s", "5s")}
    return {"actual": {"async": async_, "sync": sync,
                       "consumer_resume": {"default_30s_over_20s": resume["30s"] is not None and resume["30s"] > 20,
                                           "expires_5s_under_15s": resume["5s"] is not None and resume["5s"] < 15}},
            "consumer_resume_s~": resume,
            "async_counters~": {k: int(v) for k, v in d.items()}, "async_buffered~": int(buffered),
            "median_s~": {"async": round(med, 4), "sync": round(med_s, 3)}}


def resume_after_restart(expires, timeout=120):
    """NATS остановлен и запущен: через сколько секунд клик снова доходит до агрегата."""
    s.up({"CONSUME_EXPIRES": expires})
    # свежий consumer: прошлые остановки брокера в этой же оси оставили его в непредсказуемой фазе
    s.compose("up", "-d", "--force-recreate", "consumer", env={"CONSUME_EXPIRES": expires})
    warm = s.create()
    t0 = time.time()
    while s.clicks(warm) == 0 and time.time() - t0 < 60:
        s.request("/" + warm)
        time.sleep(0.5)
    s.compose("stop", "nats")
    time.sleep(3)
    s.compose("start", "nats")
    t0 = time.time()
    code = s.create()
    while time.time() - t0 < timeout:
        s.request("/" + code)
        if s.clicks(code) > 0:
            return round(time.time() - t0)
        time.sleep(0.5)
    return None


def outbox():
    out = {}
    for mode in ("direct", "outbox"):
        s.up({"EVENT_MODE": mode})
        s.compose("up", "-d", "--wait", "service", env={"EVENT_MODE": mode, "CRASH_AFTER_INSERT": "true"})
        s.wait_ready()
        before = int(s.psql("select count(*) from links"))
        s.request("/links", "POST", {"url": "https://example.com/" + mode})   # ответа не будет: процесс падает
        time.sleep(1)
        code = s.psql("select code from links order by created_at desc limit 1")
        in_db = int(s.psql("select count(*) from links")) == before + 1
        s.up({"EVENT_MODE": mode})                                             # перезапуск без падения
        seen = False
        for _ in range(30):
            if s.psql("select count(*) from seen_links where code = '%s'" % code) == "1":
                seen = True
                break
            time.sleep(0.5)
        out[mode] = {"link_in_db": in_db, "event_seen": seen}
    return {"actual": out}


def _db_restart():
    """PostgreSQL остановлен и запущен, consumer не перезапускается: восстановится ли он и посчитает ли точно."""
    s.up()
    code = s.create()
    s.request("/" + code)                                       # ссылка в кэше: редиректы работают без базы
    s.wait_clicks(code, 1)
    started = s.sh(["docker", "inspect", "-f", "{{.State.StartedAt}}", "capstone-consumer-1"]).stdout.strip()
    s.compose("stop", "postgres")
    try:
        res = s.burst(["/" + code] * 100, 20)
        time.sleep(5)                                           # consumer получает клики при недоступной базе
    finally:
        s.compose("up", "-d", "--wait", "postgres")             # с проверкой готовности базы
    n = s.wait_clicks(code, 101, timeout=90)
    time.sleep(3)                                               # дать дойти лишним, если бы они были
    n = s.clicks(code)
    same = s.sh(["docker", "inspect", "-f", "{{.State.StartedAt}}", "capstone-consumer-1"]).stdout.strip() == started
    return {"redirects_ok_without_db": all(r[0] == 302 for r in res), "recovers_without_restart": same and n >= 101,
            "count_exact": n == 101, "reconnected": s.metrics(s.CONSUMER).get("db_reconnects_total", 0) > 0}, n


def consumer():
    s.up()
    code = s.create()
    for r in s.burst(["/" + code] * 100, 20):
        assert r[0] == 302
    control = s.wait_clicks(code, 100) == 100
    crash = {}
    dup = {}
    for idem in (False, True):
        env = {"IDEMPOTENT": str(idem).lower(), "ACK_WAIT": "2s"}
        s.compose("stop", "consumer")
        code = s.create("https://example.com/c%s" % idem)
        for r in s.burst(["/" + code] * 100, 20):
            assert r[0] == 302
        time.sleep(1)
        s.compose("up", "-d", "consumer", env=dict(env, CRASH_AFTER_COMMIT="50"))
        for _ in range(60):                          # дождаться падения после 50-го коммита
            st = s.sh(["docker", "inspect", "-f", "{{.State.Running}}", "capstone-consumer-1"]).stdout.strip()
            if st == "false":
                break
            time.sleep(0.5)
        s.compose("up", "-d", "consumer", env=env)
        n = s.wait_clicks(code, 100, timeout=40)
        time.sleep(3)                                # ACK_WAIT: повторная доставка неподтверждённого
        n = s.clicks(code)
        crash[idem] = n
        dup[idem] = s.metrics(s.CONSUMER).get("clicks_duplicates_total", 0)
    restart, restart_n = _db_restart()
    return {"actual": {"control_exact": control, "db_restart": restart,
                       "crash": {"non_idempotent_overcounts": crash[False] > 100, "idempotent_exact": crash[True] == 100,
                                 "idempotent_duplicates_over_0": dup[True] > 0}},
            "counted": {"non_idempotent": crash[False], "idempotent": crash[True], "after_db_restart": restart_n},
            "duplicates_skipped": int(dup[True])}


AXES = {"config": config, "shutdown": shutdown, "errors": errors, "cache": cache, "broker_down": broker_down,
        "outbox": outbox, "consumer": consumer}
