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
    # паритет протокола
    ("Java отвечает на публикацию не 204", "fixtures/protocol.jsonl",
     r're:("variant": "java".*?"publish_ok": )204', r"\g<1>200"),
    ("наблюдения паритета пусты", "fixtures/protocol.jsonl",
     r're:("variant": "go".*?"sse_ids": )\[1, 2, 3\]', r"\g<1>[]"),
    # ось 1
    ("цена события не разностью прогонов", "fixtures/wire.jsonl",
     r're:("down_per_event": )\d+\.\d', r"\g<1>1.5"),
    ("контроль нагрузки снят", "scripts/analyze-wire.py",
     'if not r["compress"] and r["down_per_event"] < r["payload"]:', "if False:"),
    ("пустые опросы не считаются", "fixtures/wire.jsonl",
     r're:("name": "poll-50ms".*?"empty_per_event": )[\d.]+', r"\g<1>0.0"),
    # ось 2
    ("счёт SSE после закрытия потока", "fixtures/proxy.jsonl",
     '"stream_open_at_count": true', '"stream_open_at_count": false'),
    ("контроль без прокси не проверяется", "scripts/analyze-proxy.py",
     'if r["path"] == "direct" and r["outcome"] != CONTROL[r["kind"]]:', "if False:"),
    ("heartbeat у варианта без heartbeat", "fixtures/proxy.jsonl",
     r're:("path": "short-idle", "angie_conf": "short-idle", "heartbeat_ms": )0', r"\g<1>1000"),
    # ось 3
    ("клиенты на одном инстансе", "fixtures/scale.jsonl",
     r're:"clients_per_instance": \{[^}]*\}', '"clients_per_instance": {"rt-sc-a": 20}'),
    ("разброс клиентов не проверяется", "scripts/analyze-scale.py",
     'if set(per) != {A, B} or min(per.values()) == 0:', "if False:"),
    # ось 4
    ("публикация до разрыва", "fixtures/reconnect.jsonl",
     '"bus_down_before_publish": true', '"bus_down_before_publish": false'),
    ("разрыв не подтверждается", "scripts/analyze-reconnect.py",
     'if not (r["bus_down_before_publish"] and r["bus_down_after_publish"] and r["bus_up_again"]):', "if False:"),
    ("шторм не помечен недетерминированным", "fixtures/reconnect.jsonl",
     r're:("kind": "storm".*?"nondeterministic": )true', r"\g<1>false"),
    ("стыка не было", "fixtures/reconnect.jsonl",
     r're:("variant": "go/resume/redis-streams-barrier".*?"received_at_publish_end": )5', r"\g<1>7"),
    ("контроль стыка не провалился", "fixtures/reconnect.jsonl",
     r're:("variant": "go/resume/redis-streams-barrier-late".*?"outcome": )"lost"', r'\g<1>"no-loss"'),
    ("подписчик остался после отмены", "fixtures/reconnect.jsonl",
     r're:("variant": "java/cancel".*?"outcome": )"released"', r'\g<1>"leaked"'),
    ("отказ истории скрыт живым потоком", "fixtures/reconnect.jsonl",
     r're:("variant": "go/resume/redis-streams-history-error".*?"outcome": )"refused"', r'\g<1>"lost"'),
    ("журнал потерял события", "fixtures/reconnect.jsonl",
     r're:("variant": "go/gap/jetstream".*?"outcome": )"no-loss"', r'\g<1>"lost"'),
    # ось 5
    # верхний уровень строки, а не вложенный результат пробы: там то же поле стоит раньше
    ("быстрый клиент потерял", "fixtures/slow.jsonl",
     r're:"fast_all": true(, "fast_missing": 0, "slow_received")', r'"fast_all": false\1'),
    ("событий меньше буферов", "scripts/analyze-slow.py",
     'if r["n"] * r["payload"] < 2 * SOCKET_BUFFERS:', "if False:"),
    # ось 6
    ("шумная база не отвергается", "scripts/analyze-idle.py",
     'if r["base_drift_kb"] * 10 > grew:', "if False:"),
    ("куча JVM не фиксирована", "fixtures/idle.jsonl", '"jvm_heap": "-Xms256m -Xmx256m"', '"jvm_heap": "auto"'),
    # сводки и маркер
    # строка не из контроля: её подмену ловит только сравнение со сводкой сессии
    ("сводка расходится с фикстурами", "fixtures/proxy.jsonl",
     r're:("variant": "go/sse-window/buffered".*?"outcome": )"delivered"', r'\g<1>"held"'),
    ("хеш фикстуры в маркере", "fixtures/RUN-COMPLETE", r're:("wire.jsonl": ")[0-9a-f]', r"\g<1>0"),
]


def make_copy(tmp):
    stand = os.path.join(tmp, "realtime")
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
