"""Отказ на старте: измеряется, а не декларируется.

Совет «валидируйте конфигурацию при запуске» верен, но обычно остаётся
словами. Здесь у двух режимов одна и та же проверка, различается только
момент её выполнения — при импорте или при обработке запроса, — и видно,
чем эта разница оборачивается.

Четыре конфигурации:

* ok      — всё на месте;
* missing — обязательной переменной нет;
* bad_url — есть, но недопустимого вида;
* bad_int — число не парсится.

Для каждой пары режим-конфигурация фиксируется: поднялся ли процесс, с каким
кодом завершился, через сколько, начал ли слушать порт, и — главное —
отвечает ли /health успехом, когда приложение на самом деле нерабочее.

Последнее и есть суть: наивный режим проходит проверку здоровья, потому что
она не заглядывает в конфигурацию. Оркестратор считает такой экземпляр
исправным и отправляет на него трафик.
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

PY = "/opt/py/web/bin/python"
APP_DIR = "/app"
BENCH_DIR = os.path.dirname(os.path.abspath(__file__))
PORT = 8012

GOOD = {"DATABASE_URL": "postgresql://user:pass@db:5432/app", "POOL_SIZE": "10"}

CONFIGS = {
    "ok": GOOD,
    "missing": {"POOL_SIZE": "10"},
    "bad_url": dict(GOOD, DATABASE_URL="mysql://user@db/app"),
    "bad_int": dict(GOOD, POOL_SIZE="много"),
}
MODES = ("naive", "strict")


def probe(path):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{PORT}{path}",
                                    timeout=3) as resp:
            return resp.status, resp.read()[:200]
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()[:200]
    except OSError:
        return None, b""


def run_case(mode, config_name, round_no):
    env = dict(os.environ, APP_MODE=mode, PYTHONPATH=APP_DIR)
    for key in ("DATABASE_URL", "POOL_SIZE"):
        env.pop(key, None)
    env.update(CONFIGS[config_name])

    started = time.time()
    proc = subprocess.Popen(
        [PY, "-m", "uvicorn", "app_config:app", "--port", str(PORT),
         "--log-level", "warning"],
        cwd=APP_DIR, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        env=env, start_new_session=True)

    # Ждём: либо процесс умер, либо порт начал отвечать.
    listening = False
    deadline = time.time() + 20
    while time.time() < deadline:
        if proc.poll() is not None:
            break
        if probe("/health")[0] is not None:
            listening = True
            break
        time.sleep(0.1)

    health_status = health_body = None
    work_status = work_body = None
    if listening:
        health_status, hb = probe("/health")
        health_body = hb.decode("utf-8", "replace")
        work_status, wb = probe("/work")
        work_body = wb.decode("utf-8", "replace")

    exit_code = proc.poll()
    if exit_code is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
    elapsed = time.time() - started

    # Порт должен освободиться до следующего случая.
    for _ in range(50):
        if probe("/health")[0] is None:
            break
        time.sleep(0.2)

    return json.dumps({
        "mode": mode,
        "config": config_name,
        "round": round_no,
        "started_listening": listening,
        "exit_code": exit_code,
        "seconds_to_outcome": round(elapsed, 3),
        "health_status": health_status,
        "work_status": work_status,
        "work_body": work_body,
        # Главный признак: здоровье зелёное, а работа не работает.
        "healthy_but_broken": bool(listening and health_status == 200
                                   and work_status not in (200, None)),
    }, ensure_ascii=False, sort_keys=True)


def main():
    rounds = int(os.environ.get("ROUNDS", "3"))
    out_path = os.environ.get("OUT", "/fixtures/config.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            for mode in MODES:
                for config_name in CONFIGS:
                    out.write(run_case(mode, config_name, r) + "\n")
                    out.flush()
            print(f"круг {r} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
