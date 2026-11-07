"""Оси статьи 4 на локальном стеке: сквозной трейс через NATS, SLO-правило, нагрузка и пул соединений."""

import json
import shutil
import time

import stack as s
from axes_build import image

OTEL = {"OTEL_ENDPOINT": "http://otelcol:4318", "OTEL_JAVA": "true"}


def spans():
    path = s.ROOT / "out/otel/spans.json"
    out = []
    if not path.exists():
        return out
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        for rs in json.loads(line).get("resourceSpans", []):
            svc = next((a["value"].get("stringValue") for a in rs["resource"].get("attributes", []) if a["key"] == "service.name"), "")
            for ss in rs.get("scopeSpans", []):
                for sp in ss.get("spans", []):
                    out.append({"svc": svc, "trace": sp["traceId"], "span": sp["spanId"], "parent": sp.get("parentSpanId", ""),
                                "name": sp["name"], "scope": ss.get("scope", {}).get("name", "")})
    return out


REQUESTS = 100


def trace_run(propagate, carrier="exact", manual=True):
    # каталог не пересоздаётся: Docker Desktop держит привязку к старому каталогу, и сборщик падал
    # на открытии файла; удаляется только файл спанов прошлого прогона
    d = s.ROOT / "out/otel"
    d.mkdir(parents=True, exist_ok=True)
    (d / "spans.json").unlink(missing_ok=True)
    env = dict(OTEL, PROPAGATE=str(propagate).lower(), NATS_HEADER_CARRIER=carrier, TRACE_MANUAL=str(manual).lower())
    s.compose("--profile", "obs", "up", "-d", "--wait", env=env)
    s.wait_ready()
    code = s.create()
    for _ in range(REQUESTS):
        s.request("/" + code)
    s.wait_clicks(code, REQUESTS)
    time.sleep(4)                                  # пакетная отправка спанов в сборщик
    s.compose("--profile", "obs", "stop", "service", "consumer", env=env)   # выгрузить остаток спанов
    time.sleep(2)
    sp = spans()
    s.compose("--profile", "obs", "down", "-v", check=False)
    return linkage(sp)


def linkage(sp):
    """Связность считается от сделанных запросов: сколько из REQUESTS трейсов редиректа содержат спан
    consumer'а, родитель которого — спан публикации того же трейса. Потерянный спан consumer'а снижает долю."""
    by_trace = {}
    for x in sp:
        by_trace.setdefault(x["trace"], []).append(x)
    # родитель ищется по паре (трейс, спан): ссылка на публикацию из другого трейса — не связь
    publish = {(x["trace"], x["span"]) for x in sp if x["svc"] == "shortlinks" and x["name"] == "nats publish click"}
    manual = [x for x in sp if x["svc"] == "shortlinks-consumer" and x["name"].startswith("consume clicks")]
    agent = [x for x in sp if x["svc"] == "shortlinks-consumer" and x["name"].startswith("clicks.") and x["name"].endswith(" process")]
    jdbc = [x for x in sp if x["svc"] == "shortlinks-consumer" and "jdbc" in x["scope"] and "clicks_by_minute" in x["name"]]

    def linked(kind):
        return sum(1 for xs in by_trace.values()
                   if any(x["svc"] == "shortlinks" and x["name"] == "GET /{code}" for x in xs)
                   and any(x in kind and (x["trace"], x["parent"]) in publish for x in xs)) / REQUESTS

    def in_requests(kind):
        return sum(1 for xs in by_trace.values()
                   if any(x["name"] == "GET /{code}" for x in xs) and any(x in kind for x in xs)) / REQUESTS
    chain = any({"GET /{code}", "pg select link", "nats publish click"} <= {x["name"] for x in xs}
                and any(x["name"].startswith("consume clicks") for x in xs) for xs in by_trace.values())
    manual_ids = {(x["trace"], x["span"]) for x in manual}
    return {"manual": linked(manual), "agent": linked(agent), "jdbc": in_requests(jdbc), "chain": chain,
            "jdbc_under_manual": bool(jdbc) and all((x["trace"], x["parent"]) in manual_ids for x in jdbc),
            "counts": {"manual": len(manual), "agent": len(agent), "jdbc": len(jdbc), "spans": len(sp)}}


def foreign_parent_control(n=REQUESTS):
    """Отрицательный контроль проверки связности: в каждом трейсе спан consumer'а ссылается на спан публикации
    СОСЕДНЕГО трейса. Связность обязана быть нулевой — иначе проверка принимает чужого родителя."""
    sp = []
    for i in range(n):
        tr, nxt = "t%03d" % i, "t%03d" % ((i + 1) % n)
        sp += [{"svc": "shortlinks", "trace": tr, "span": "h" + tr, "parent": "", "name": "GET /{code}", "scope": "http"},
               {"svc": "shortlinks", "trace": tr, "span": "p" + tr, "parent": "h" + tr, "name": "nats publish click", "scope": "s"},
               {"svc": "shortlinks-consumer", "trace": tr, "span": "c" + tr, "parent": "p" + nxt, "name": "consume clicks.x", "scope": "s"},
               {"svc": "shortlinks-consumer", "trace": tr, "span": "a" + tr, "parent": "p" + nxt, "name": "clicks.x process",
                "scope": "io.opentelemetry.nats"},
               {"svc": "shortlinks-consumer", "trace": tr, "span": "j" + tr, "parent": "c" + nxt, "name": "INSERT clicks_by_minute",
                "scope": "io.opentelemetry.jdbc"}]
    r = linkage(sp)
    return r["manual"] == 0 and r["agent"] == 0 and not r["jdbc_under_manual"]


def trace():
    control = foreign_parent_control()
    p = trace_run(True)
    a = trace_run(True, manual=False)
    c = trace_run(True, "canonical")
    n = trace_run(False)
    return {"actual": {"propagated": {"request_traces_with_consumer_share_over_0_99": p["manual"] > 0.99,
                                      "consumer_spans_equal_requests": p["counts"]["manual"] == REQUESTS,
                                      "agent_span_child_of_publish_share_over_0_99": p["agent"] > 0.99,
                                      "chain_http_pg_publish_consume": p["chain"], "jdbc_under_manual_span": p["jdbc_under_manual"]},
                       "agent_only": {"agent_span_child_of_publish_share_over_0_99": a["agent"] > 0.99,
                                      "jdbc_in_request_traces_share_over_0_99": a["jdbc"] > 0.99, "no_manual_spans": a["counts"]["manual"] == 0},
                       "canonical_header_names": {"request_traces_with_consumer_share": round(c["manual"], 4),
                                                  "agent_share": round(c["agent"], 4)},
                       "not_propagated": {"request_traces_with_consumer_share": round(n["manual"], 4), "agent_share": round(n["agent"], 4)},
                       "foreign_parent_rejected": control},
            "counts~": {"propagated": p["counts"], "agent_only": a["counts"], "canonical": c["counts"], "not_propagated": n["counts"]},
            "jdbc_share_agent_only~": round(a["jdbc"], 4)}


def slo():
    d = str(s.ROOT / "deploy/prometheus")
    check = s.sh(["docker", "run", "--rm", "-v", d + ":/p", "--entrypoint", "promtool", image("prometheus"), "check", "rules", "/p/slo.yml"],
                 check=False)
    # каждый сценарий slo_test.yml — отдельным файлом: у каждого признака свой код возврата promtool
    import yaml_lite
    doc = yaml_lite.split_tests((s.ROOT / "deploy/prometheus/slo_test.yml").read_text())
    work = s.ROOT / "out/promtool"
    work.mkdir(parents=True, exist_ok=True)
    codes = []
    for n, text in enumerate(doc):
        (work / ("t%d.yml" % n)).write_text(text)
        p = s.sh(["docker", "run", "--rm", "-v", d + ":/p", "-v", str(work) + ":/t", "-w", "/p", "--entrypoint", "promtool",
                  image("prometheus"), "test", "rules", "/t/t%d.yml" % n], check=False)
        codes.append(p.returncode)
    fast, slow, normal, down, absent, probe = (c == 0 for c in codes)
    return {"actual": {"fast_burn_fires": fast, "slow_burn_fires": slow, "normal_error_rate_silent": normal,
                       "rules_valid": check.returncode == 0,
                       "blind_spots": {"all_targets_down_fires": down, "metrics_absent_fires": absent,
                                       "probe_failure_fires_while_ratio_silent": probe}},
            "promtool_exit_codes": codes}


def load_run(conns, rate=1500):
    s.up({"REDIS_ADDR": "", "DB_MAX_CONNS": str(conns)})
    s.psql("INSERT INTO links (code, url, created_at) SELECT 'k' || g, 'https://example.com/' || g, now() "
           "FROM generate_series(1, 2000) g ON CONFLICT DO NOTHING")
    (s.ROOT / "out/k6").mkdir(parents=True, exist_ok=True)
    (s.ROOT / "out/k6/codes.json").write_text(json.dumps(["k%d" % i for i in range(1, 2001)]))
    shutil.copy(s.ROOT / "deploy/k6/redirects.js", s.ROOT / "out/k6/redirects.js")
    for _ in range(50):
        s.request("/k1")                                       # прогрев соединений
    m0 = s.metrics()
    out = s.sh(["docker", "run", "--rm", "--network", "capstone_default", "-v", str(s.ROOT / "out/k6") + ":/scripts",
                image("k6"), "run", "-q", "-e", "RATE=%d" % rate, "-e", "DURATION=30s", "/scripts/redirects.js"], timeout=600).stdout
    m1 = s.metrics()
    r = json.loads([l for l in out.splitlines() if l.startswith("{")][-1])
    waits = m1["db_pool_empty_acquire_total"] - m0["db_pool_empty_acquire_total"]
    acq = m1["db_pool_acquire_total"] - m0["db_pool_acquire_total"]
    wait_s = m1["db_pool_acquire_wait_seconds_total"] - m0["db_pool_acquire_wait_seconds_total"]
    return r, waits / max(1, acq), wait_s


def load():
    small, sw, sws = load_run(2)
    large, lw, lws = load_run(20)
    ramp = {}
    for rate in (3000,):                 # 6000 убрана: генератор на ней не держит частоту (revision_stage9_session8)
        rs, rsw, _ = load_run(2, rate)
        rl, rlw, _ = load_run(20, rate)
        ramp[rate] = {"small": rs, "large": rl, "small_waited": rsw, "large_waited": rlw}
    def reached(*runs):
        # генератор держал частоту: пропущенных итераций меньше 1 % — иначе ступень не та, что заявлена
        return all(r["dropped"] < 0.01 * (r["reqs"] + r["dropped"]) for r in runs)

    def step(v):
        return {"small_waits_over_5pct": v["small_waited"] > 0.05, "small_waits_over_5x_large": v["small_waited"] > 5 * v["large_waited"],
                "errors_zero": v["small"]["failed"] == 0 and v["large"]["failed"] == 0,
                "generator_reached_rate": reached(v["small"], v["large"])}
    # p99 одной машины между прогонами — от 0,03× до 7,5× (revision_stage5_after_session1): в actual только насыщение пула
    return {"actual": {"small_pool": {"errors_zero": small["failed"] == 0}, "large_pool": {"errors_zero": large["failed"] == 0},
                       "small_pool_waits_for_connections": sw > 0.1, "large_pool_waits_less": lw < sw,
                       "generator_reached_rate": reached(small, large),
                       "ramp_3000": step(ramp[3000])},
            "requests~": {"small": small["reqs"], "large": large["reqs"]},
            "p99_ratio~": round(small["p99"] / large["p99"], 2), "p50_ratio~": round(small["p50"] / large["p50"], 2),
            "waited_share~": {"small": round(sw, 4), "large": round(lw, 4)},
            "ramp~": {str(k): {"p99_ratio": round(v["small"]["p99"] / v["large"]["p99"], 2),
                               "waited": {"small": round(v["small_waited"], 4), "large": round(v["large_waited"], 4)},
                               "reqs": {"small": v["small"]["reqs"], "large": v["large"]["reqs"]},
                               "failed": {"small": v["small"]["failed"], "large": v["large"]["failed"]},
                               "dropped": {"small": v["small"]["dropped"], "large": v["large"]["dropped"]}} for k, v in ramp.items()},
            "dropped~": {"small": small["dropped"], "large": large["dropped"]}}


AXES = {"trace": trace, "slo": slo, "load": load}
