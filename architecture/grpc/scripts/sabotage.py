"""Реестр диверсий: каждая проверка обязана краснеть от своей поломки.

Прогон: копия стенда во временном каталоге. Сначала контрольная копия без
диверсий: её набор обязан пройти; иначе «покраснело» у диверсий ничего не
значит. Затем каждая диверсия по одной: применить замену текста, прогнать
набор (без Docker), ожидать провал. Неприменившаяся диверсия — проваленная
проверка, а не пропуск.

    python3 scripts/sabotage.py            → по строке на диверсию, код 0 — все сработали

Пока ставится маркер (STAND_MARKER_PENDING=1), диверсии, нацеленные на сам
маркер, пропускаются и называются отдельно.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (имя, файл относительно стенда, было, стало). «Было» с приставкой re: —
# регулярное выражение, заменяется первое совпадение.
SABOTAGES = [
    # паритет
    ("пара java->java расходится", "fixtures/protocol.jsonl",
     r're:("variant": "java->java".*?"unary_plain": \[)"Unknown"', r'\g<1>"Internal"'),
    # ось 1
    ("новый контекст «бросил» работу", "fixtures/deadline.jsonl",
     r're:("mode": "fresh", "deadline_ms": 300,.*?"outcome": )"wasted"', r'\g<1>"stopped"'),
    ("«бросил» без остановки не отвергается", "scripts/analyze-deadline.py",
     'if r["outcome"] == "stopped" and r["b_steps_done"] >= 10:', "if False:"),
    # ось 2
    ("без проверки отмены цикл остановился", "fixtures/cancel.jsonl",
     r're:("mode": "no-check".*?"outcome": )"kept-sending"', r'\g<1>"stopped"'),
    ("отрицательный контроль отмены снят", "scripts/analyze-cancel.py",
     'if r["mode"] not in ("check", "control") and r["outcome"] != "kept-sending":', "if False:"),
    # оси 3 и 8
    ("паника Go не уронила сервер", "fixtures/errors.jsonl",
     r're:("variant": "go->go/panic".*?"health_after": )false, "container_running": false',
     r"\g<1>true, \"container_running\": true"),
    ("паника без перехватчика не проверяется", "scripts/analyze-errors.py",
     'if r["mode"] == "panic" and r["server"] == "go" and r["container_running"]:', "if False:"),
    ("шлюз с errdetails отдаёт 500", "fixtures/errors.jsonl",
     r're:("variant": "go/gateway/details-registered".*?"http_status": )404', r"\g<1>500"),
    # ось 4
    ("метрики первыми видят только разрешённые", "fixtures/chain.jsonl",
     r're:("order": "metrics-first".*?"metrics_calls": )20', r"\g<1>10"),
    ("утечка мимо авторизации не отвергается", "scripts/analyze-chain.py",
     'if r["handler_calls"] != N:', "if False:"),
    ("Java пишет код Ok", "fixtures/chain.jsonl",
     r're:("variant": "go->java/auth-first".*?"metrics_by_code": \{)"OK"', r'\g<1>"Ok"'),
    # ось 5
    ("повторяла проба, а не gRPC", "fixtures/retry.jsonl",
     r're:("name": "unavailable".*?"success_attempt": )3', r"\g<1>1"),
    ("номер попытки не сверяется", "scripts/analyze-retry.py",
     'if r["client_code"] == "OK" and r["success_attempt"] != r["server_attempts"]:', "if False:"),
    ("maxAttempts не урезан", "fixtures/retry.jsonl",
     r're:("name": "max10".*?"server_attempts": )5', r"\g<1>10"),
    # ось 6
    ("политика по умолчанию стерпела пинги", "fixtures/keepalive.jsonl",
     r're:("policy": "default".*?"outcome": )"goaway"', r'\g<1>"survived"'),
    ("«пережило» без READY не отвергается", "scripts/analyze-keepalive.py",
     'if r["outcome"] == "survived" and r["kind"] == "idle" and r["state_before_second"] != "READY":', "if False:"),
    # ось 7
    ("L4 распределил вызовы", "fixtures/balance.jsonl",
     r're:("path": "angie-l4".*?"outcome": )"pinned"', r'\g<1>"spread"'),
    ("число соединений не сверяется", "scripts/analyze-balance.py",
     'if r["outcome"] == "pinned" and sum(r["backend_conns"].values()) != 1:', "if False:"),
    # рецензия: паника + метрики, граница повторов, молчащий стрим
    ("внешний перехватчик не прячет панику от метрик", "fixtures/chain.jsonl",
     r're:("place": "recover-outer".*?"metrics_by_code": \{)"OK": 1\}', r'\g<1>"Internal": 1, "OK": 1}'),
    ("зафиксированный стрим повторён", "fixtures/retry.jsonl",
     r're:("name": "stream-after".*?"server_attempts": )1', r"\g<1>4"),
    ("молчащий стрим за L4 без пингов дожил", "fixtures/keepalive.jsonl",
     r're:("path": "angie-l4", "ka": "off", "code": )"Unavailable"(.*?"outcome": )"dropped"',
     r'\g<1>"OK"\g<2>"survived"'),
    # рецензия 2: доставка отмены в Java, заголовки фиксируют вызов, адрес соединения
    ("onNext после возврата обработчика не бросает", "fixtures/cancel.jsonl",
     r're:("mode": "async",.*?"stream_send_errors": )\d+', r"\g<1>0"),
    ("обработчик отмены при цикле внутри вызова пришёл до конца цикла", "fixtures/cancel.jsonl",
     r're:("mode": "sync-handler",.*?"cancel_handler_before_end": )0', r"\g<1>1"),
    ("стрим после заголовков повторён", "fixtures/retry.jsonl",
     r're:("name": "stream-headers".*?"server_attempts": )1', r"\g<1>4"),
    ("«пережило» по новому соединению", "fixtures/keepalive.jsonl",
     r're:("kind": "idle".*?"same_conn": )true(.*?"outcome": "survived")', r"\g<1>false\g<2>"),
    # ось 9
    ("5 МБ прошли без поднятого предела", "fixtures/size.jsonl",
     r're:("name": "5mb", .*?"outcome": )"rejected"', r'\g<1>"delivered"'),
    # сводки и маркер: поле, которое проверяет только сравнение со сводкой сессии
    ("сводка расходится с фикстурами", "fixtures/errors.jsonl",
     r're:("variant": "go/gateway/stream".*?"lines": )3', r"\g<1>4"),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("deadline.jsonl": ")[0-9a-f]', r"\g<1>0"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "grpc")
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__", "target"))
    return stand


def run_suite(stand):
    env = dict(os.environ)
    env.pop("STAND_WITH_DOCKER", None)
    proc = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "test_*.py"],
                          cwd=stand, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return proc.returncode, proc.stdout.decode("utf-8", "replace")


def main():
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        rc, out = run_suite(make_copy(os.path.join(tmp, "control")))
        if rc != 0:
            print("КОНТРОЛЬНАЯ КОПИЯ НЕ ПРОШЛА — диверсии не значат ничего\n" + out[-3000:])
            return 1
        print("контрольная копия: зелёная")
        pending = os.environ.get("STAND_MARKER_PENDING") == "1"
        skipped = 0
        for i, (name, rel, old, new) in enumerate(SABOTAGES):
            if pending and rel == "fixtures/RUN-COMPLETE":
                print("пропущена (маркер ставится): %s" % name)
                skipped += 1
                continue
            stand = make_copy(os.path.join(tmp, "s%d" % i))
            path = os.path.join(stand, rel)
            with open(path, encoding="utf-8") as f:
                text = f.read()
            if old.startswith("re:"):
                changed, n = re.subn(old[3:], new, text, count=1)
            else:
                changed, n = text.replace(old, new, 1), text.count(old)
            if n == 0:
                print("НЕ ПРИМЕНИЛАСЬ: %s (%s)" % (name, rel))
                failures += 1
                continue
            with open(path, "w", encoding="utf-8") as f:
                f.write(changed)
            rc, _ = run_suite(stand)
            ok = rc != 0
            failures += 0 if ok else 1
            print("%s: %s" % ("покраснело" if ok else "ОСТАЛОСЬ ЗЕЛЁНЫМ", name))
    print("диверсий: %d, не сработало: %d" % (len(SABOTAGES) - skipped, failures))
    if skipped:
        print("пропущено на время постановки маркера: %d" % skipped)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
