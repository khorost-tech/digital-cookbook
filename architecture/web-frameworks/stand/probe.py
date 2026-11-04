"""Одни и те же запросы к каждому из двенадцати приложений; ответ — код, заголовки, тело.

Нет ответа — код 0 и класс транспортной ошибки: closed (сервер закрыл соединение), timeout, refused.
"""

import html
import http.client
import json
import re
import socket
from urllib.parse import urlsplit

import apps

MARKER = "marker-7f3a9c"   # уникальная метка в сообщении исключения всех двенадцати приложений
STACK = re.compile(r"Traceback \(most recent call last\)|\n\s+at \S+ \(|panicked at|\.java:\d+\)|\.go:\d+|File \"[^\"]+\.py\"|node_modules/")
# пороги известных лимитов тела по умолчанию: размеры чуть ниже и чуть выше каждого, байт тела запроса
SIZES = (90_000, 110_000, 900_000, 1_100_000, 2_000_000, 2_200_000, 29_000_000, 31_000_000, 40 * 1024 * 1024)


def kind(e):
    if isinstance(e, (socket.timeout, TimeoutError)):
        return "timeout"
    if isinstance(e, ConnectionRefusedError):
        return "refused"
    return "closed"   # RemoteDisconnected, ConnectionResetError, BrokenPipe, IncompleteRead


def request(app, method, path, body=None, ctype="application/json"):
    """(код, заголовки, тело, транспорт): транспорт — "ok" или класс ошибки при отсутствии ответа."""
    conn = http.client.HTTPConnection("127.0.0.1", apps.PORT[app], timeout=30)
    try:
        headers = {"Content-Type": ctype} if body is not None else {}
        conn.request(method, path, body=body, headers=headers)
        r = conn.getresponse()
        return r.status, {k.lower(): v for k, v in r.getheaders()}, r.read().decode("utf-8", "replace"), "ok"
    except (ConnectionError, http.client.HTTPException, OSError) as e:
        return 0, {}, "", kind(e)
    finally:
        conn.close()


def strings(x):
    if isinstance(x, str):
        yield x
    elif isinstance(x, dict):
        for k, v in x.items():
            yield k
            yield from strings(v)
    elif isinstance(x, list):
        for v in x:
            yield from strings(v)


def leaks(body):
    """Сообщение исключения или стек в теле: проверяется сырой текст, текст после HTML-раскодирования и все строки
    разобранного JSON — содержимое одно, представление разное (\\u-экранирование, &#…;)."""
    views = [body, html.unescape(body)]
    try:
        views += list(strings(json.loads(body)))
    except ValueError:
        pass
    return any(MARKER in v or STACK.search(v) for v in views)


# контроли детектора: одна и та же утечка в разных представлениях — да; общий текст ошибки — нет
DETECTOR_CONTROLS = {"plain": ("boom: " + MARKER, True),
                     "json_escaped": (json.dumps({"error": "boom: " + MARKER}).replace("m", "\\u006d"), True),
                     "html_entities": ("<pre>boom: " + "".join("&#%d;" % ord(c) for c in MARKER) + "</pre>", True),
                     "stack_only": ("Error\n    at Layer.handle (/app/node_modules/router/lib/layer.js:1:1)", True),
                     "generic": (json.dumps({"error": "Internal Server Error"}), False)}


def detector_ok():
    return all(leaks(body) == want for body, want in DETECTOR_CONTROLS.values())


def post_body(app, size):
    """POST /items телом ровно size байт; ответ читается и после обрыва записи — сервер с лимитом часто отвечает 413,
    не дочитав. 0 — ответа нет вовсе."""
    payload = b'{"name":"' + b"x" * (size - len(b'{"name":"","qty":1}')) + b'","qty":1}'
    assert len(payload) == size
    conn = http.client.HTTPConnection("127.0.0.1", apps.PORT[app], timeout=60)
    try:
        conn.putrequest("POST", "/items")
        conn.putheader("Content-Type", "application/json")
        conn.putheader("Content-Length", str(len(payload)))
        conn.endheaders()
        try:
            for i in range(0, len(payload), 1 << 20):
                conn.send(payload[i:i + (1 << 20)])
        except (ConnectionError, OSError):
            pass
        try:
            r = conn.getresponse()
            r.read()
            return r.status
        except (ConnectionError, http.client.HTTPException, OSError):
            return 0
    finally:
        conn.close()


def jget(body, key):
    try:
        return json.loads(body).get(key)
    except (ValueError, AttributeError):
        return None


def post_trailing(app):
    """POST /items/ с телом: код, Location и — для перенаправления — повтор того же POST с тем же телом по Location
    (так поступает клиент, соблюдающий 307/308): дошло ли тело."""
    body = json.dumps({"name": "slash", "qty": 1})
    code, h, _, _ = request(app, "POST", "/items/", body)
    loc = h.get("location") if 300 <= code < 400 else None
    replay = None
    if loc:
        c2, _, b2, _ = request(app, "POST", urlsplit(loc).path or "/", body)
        replay = c2 == 201 and jget(b2, "name") == "slash"
    return code, (urlsplit(loc).path if loc else None), replay


def probe(app):
    out = {}
    out["start_s"] = apps.start(app)
    try:
        s = lambda m, p, b=None: request(app, m, p, b)[0]
        wm, wmh, _, _ = request(app, "DELETE", "/items/1")
        pt_code, pt_loc, pt_replay = post_trailing(app)
        out["routing"] = {"wrong_method": wm, "allow_on_405": sorted(m.strip() for m in wmh.get("allow", "").split(",") if m.strip()) if wm == 405 else None,
                          "trailing_slash": s("GET", "/items/1/"), "post_trailing_slash": pt_code,
                          "post_redirect_location": pt_loc, "post_redirect_body_kept": pt_replay,
                          "bad_param": s("GET", "/items/abc"), "digits_prefix_param": s("GET", "/items/1abc"),
                          "zero_id": s("GET", "/items/0"), "unknown_path": s("GET", "/nope"),
                          "typed_bad_param": s("GET", "/typed/items/abc") if app == "java-javalin" else None}
        out["validation"] = {"malformed_json": s("POST", "/items", "{bad"),
                             "missing_name": s("POST", "/items", json.dumps({"qty": 1})),
                             "empty_name": s("POST", "/items", json.dumps({"name": "", "qty": 1})),
                             "wrong_type": s("POST", "/items", json.dumps({"name": "x", "qty": "many"})),
                             "numeric_string": s("POST", "/items", json.dumps({"name": "x", "qty": "1"})),
                             "fractional": s("POST", "/items", json.dumps({"name": "x", "qty": 1.5})),
                             "trailing_garbage": s("POST", "/items", '{"name":"x","qty":1} garbage')}
        ok, _, body_ok, _ = request(app, "POST", "/items", json.dumps({"name": "x", "qty": 1}))
        g, _, body_g, _ = request(app, "GET", "/items/1")
        out["control"] = {"valid_post": ok, "valid_post_echoes_name": jget(body_ok, "name") == "x",
                          "valid_get": g, "valid_get_returns_id": jget(body_g, "id") == 1}
        if app == "java-javalin":
            tg, _, tb, _ = request(app, "GET", "/typed/items/1")
            out["control"]["typed_valid_get"] = tg == 200 and jget(tb, "id") == 1
        code, headers, body, transport = request(app, "GET", "/boom")
        _, gh, _, _ = request(app, "GET", "/items/1")
        out["errors"] = {"boom_status": code, "boom_transport": transport, "boom_leaks_message_or_stack": leaks(body),
                         "server_discloses_software": bool(gh.get("server") or gh.get("x-powered-by")),
                         "server_header": gh.get("server", ""), "x_powered_by": gh.get("x-powered-by", ""),
                         "boom_body_head": body[:160]}
        out["limits"] = {str(n): post_body(app, n) for n in SIZES}
        for _ in range(200):                                 # прогрев перед замером памяти
            request(app, "GET", "/items/1")
        out["rss_mib"] = apps.rss_mib(app)
    finally:
        apps.stop(app)
    # время старта — медиана трёх запусков: первый запуск выше плюс два отдельных
    starts = [out["start_s"]]
    for _ in range(2):
        starts.append(apps.start(app))
        apps.stop(app)
    out["start_s"] = sorted(starts)[1]
    return out


def probe_all():
    return {a: probe(a) for a in apps.APPS}
