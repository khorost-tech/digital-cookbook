#!/usr/bin/env python3
"""Диверсии над исходниками: применить к КОПИИ, потребовать падения, убрать.

ЗАЧЕМ ЭТОТ ФАЙЛ ЛЕЖИТ В РЕПОЗИТОРИИ. Проверка, которую никто не ронял,
стоит ровно столько же, сколько честное слово. Первая редакция проверок
этого стенда была написана добросовестно и держала ноль из трёх
заявленных свойств: шесть диверсий ревьюера прошли зелёными, потому что
проверки искали подстроку по всему файлу, а файл полон комментариев,
объясняющих ровно те свойства, которые проверки искали.

ПОЧЕМУ НАБОР ПЕРЕПИСАН ЦЕЛИКОМ. Метод стенда сменился: занятость больше
не выводится из длительностей ответов, а ЧИТАЕТСЯ — сервис считает
обработчиков в полёте, а вторым источником служит счёт активных запросов
в самой базе. Вместе с волнами, полосами, порогами и опорной пробой ушли
и все диверсии, целившиеся в них: они больше не про что. Набор придуман
заново, от того, ЧТО стенд утверждает теперь:

* счётчик считает занятость, а не настройку, не число запросов и не
  собственный наблюдающий вопрос;
* он убывает на выходе — в том числе при выходе исключением;
* наибольшее прочитанное число берётся и из ответов проб, и из показаний
  маршрута живости;
* оба контроля счётчика сняты, а не объявлены;
* второй источник спрашивается ПОКА пробы внутри и про ИМЯ ВАРИАНТА;
* расхождение источников записывается, а не усредняется.

ГЛАВНОЕ ПРАВИЛО. Диверсия, которая НЕ ПРИМЕНИЛАСЬ, — это ПРОВАЛЕННАЯ
проверка, а не пройденная: якорь, не нашедшийся в исходнике, означает,
что правка прошла мимо предмета, а набор проверок при этом остался
зелёным и выглядел бы подтверждением.

ЗАПУСК (из каталога стенда, в WSL):
  python3 scripts/sabotage_source.py list
  python3 scripts/sabotage_source.py run
  python3 scripts/sabotage_source.py run имя-диверсии ...

Репозиторий не правится: каждая диверсия применяется к свежей копии
каталога стенда во временном месте, и туда же ходит прогон проверок.
Копии гоняются ПАРАЛЛЕЛЬНО: набор вырос, а прогон проверок занимает
около полутора минут, и последовательный проход занимал бы часы —
столько же, сколько занимал прежний, неверный замер.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts.console import force_utf8  # noqa: E402  (после правки sys.path)
from scripts.outcome import END_MARKER  # noqa: E402

# Каталог `cpp/`: рядом со стендом лежит родовой модуль `_common`, без
# которого копия стенда не собирается (см. `_copy_stand`).
CPP_ROOT = os.path.dirname(STAND)
FIXTURE = os.path.join(STAND, "fixtures", "variants.jsonl")
DB_WAIT_FIXTURE = os.path.join(STAND, "fixtures", "db-wait.jsonl")
POOL_FIXTURE = os.path.join(STAND, "fixtures", "pool.jsonl")
PREPARED_FIXTURE = os.path.join(STAND, "fixtures", "prepared.jsonl")

# Сколько копий гоняется разом. Больше числа ядер смысла не имеет: каждый
# прогон проверок занят собой.
LANES = 6
# Предел на прогон проверок в копии. Диверсия, снявшая срок у барьера,
# ВЕШАЕТ замер, а повисший прогон неотличим от долгого — здесь он
# превращается в громкий отказ, то есть в сработавшую диверсию.
CHECK_TIMEOUT_S = 300
# Предел на прогон с докером: там собираются образы вариантов и
# поднимается база, и полный набор идёт около шести минут.
DOCKER_TIMEOUT_S = 1800


def _rows(path):
    with open(path, encoding="utf-8") as handle:
        return [json.loads(line) for line in handle
                if line.strip().startswith("{")]


def _appended(rows):
    """Строки, дописанные перед маркером: последняя вытесняет настоящую."""
    return "".join(json.dumps(row, ensure_ascii=False, sort_keys=True) + "\n"
                   for row in rows) + END_MARKER + "\n"


def _spoiled_row(variant, patch=None, **fields):
    """Строка замера с подменёнными полями — для диверсий над фикстурой.

    ПОЧЕМУ ДУБЛИКАТ, А НЕ ПРАВКА НА МЕСТЕ. Якорем тогда пришлось бы брать
    кусок самой строки замера, а он меняется с каждым перезамером: числа
    в нём не константы. Диверсия протухала бы молча — и «не применилась»
    читалось бы как «проверки держат». Здесь якорь — маркер завершения,
    а подменённая строка строится ИЗ ТЕКУЩЕЙ фикстуры и вытесняет
    настоящую: разбор кладёт строки в отображение по имени варианта, и
    последняя побеждает.
    """
    for row in _rows(FIXTURE):
        if row["variant"] == variant:
            spoiled = json.loads(json.dumps(row))
            spoiled.update(fields)
            if patch is not None:
                patch(spoiled)
            return _appended([spoiled])
    raise AssertionError("в фикстуре нет варианта %s" % variant)


def _spoiled_db_row(variant, patch=None, **fields):
    """То же, что _spoiled_row, но для замера ожидания в базе."""
    for row in _rows(DB_WAIT_FIXTURE):
        if row["variant"] == variant:
            spoiled = json.loads(json.dumps(row))
            spoiled.update(fields)
            if patch is not None:
                patch(spoiled)
            return _appended([spoiled])
    raise AssertionError("в замере ожидания нет варианта %s" % variant)


def _spoiled_pool_row(variant, pool_size, kind="pool", patch=None,
                      **fields):
    """То же, что _spoiled_row, но для замера оси 2 — пары по размеру пула.

    Ключ здесь двойной: вариант И размер пула. Пара — это и есть контроль,
    и порча одной её половины обязана быть адресной: строка, подменённая
    «у варианта», испортила бы обе настройки разом и покраснела бы не тем.
    """
    for row in _rows(POOL_FIXTURE):
        if (row["kind"] == kind and row["variant"] == variant
                and row["pool_size"] == pool_size):
            spoiled = json.loads(json.dumps(row))
            spoiled.update(fields)
            if patch is not None:
                patch(spoiled)
            return _appended([spoiled])
    raise AssertionError("в замере оси 2 нет %s %s при пуле %d"
                         % (kind, variant, pool_size))


def _spoiled_prepared_row(variant, patch=None, **fields):
    """То же, что _spoiled_row, но для замера оси 3 — текстов запроса.

    Ключ — имя плеча, включая ЧЕТВЁРТОЕ, контрольное: порча контроля
    обязана быть адресной, иначе краснело бы не то.
    """
    for row in _rows(PREPARED_FIXTURE):
        if row["variant"] == variant:
            spoiled = json.loads(json.dumps(row))
            spoiled.update(fields)
            if patch is not None:
                patch(spoiled)
            return _appended([spoiled])
    raise AssertionError("в замере оси 3 нет плеча %s" % variant)


def _spoiled_db_rows(patch):
    """Подменяет ВСЕ строки замера ожидания — для утверждений «хоть один»."""
    spoiled = json.loads(json.dumps(_rows(DB_WAIT_FIXTURE)))
    for row in spoiled:
        patch(row)
    return _appended(spoiled)


def _volleys(row):
    for setting in row["at"]:
        for item in setting["volleys"]:
            yield item


def _blind_second_source(row):
    """Второй источник, не увидевший ни одного активного запроса.

    Согласие источников на нулях — согласие ни о чём, и проверка обязана
    этим покраснеть, а не зазеленеть.
    """
    for setting in row["at"]:
        setting["runs"] = [0] * len(setting["runs"])
        for item in setting["volleys"]:
            item["db_samples"] = [0]
            item["db_peak"] = 0
            item["counter_peak"] = 0
            item["db_observed_activity"] = False
            item["inflight_by_probe"] = [0] * len(item["inflight_by_probe"])
            item["healthz_calls"] = []
            item["sources_agree"] = True
            item["sources_missed"] = False
    row["occupies_unit"] = True
    row["control_overlaps_always"] = False
    row["control_overlaps_ever"] = False
    row["sources"] = {"volleys": row["sources"]["volleys"], "disagreed": 0,
                      "missed": 0, "without_second_source": 0,
                      "agree_everywhere": True}
    row["publishable"] = False


CANCEL_FIXTURE = os.path.join(STAND, "fixtures", "cancel.jsonl")
MANIFESTS_DIR = os.path.join(STAND, "fixtures", "manifests")


def _duplicated_cancel_row():
    """Фикстура правлена так, что РАЗБОР её не замечает.

    Дописанная копия последней строки ничего не меняет по смыслу:
    строки кладутся в отображение по паре «вариант, плечо», и дубль
    вытесняет оригинал самим собой. Меняются только БАЙТЫ файла — то
    есть ровно то, что держит маркер полного прогона. Диверсия проверяет
    его, а не разбор.
    """
    rows = _rows(CANCEL_FIXTURE)
    return _appended([rows[-1]])


def _manifest_text(name):
    with open(os.path.join(MANIFESTS_DIR, name + ".json"),
              encoding="utf-8") as handle:
        return handle.read()


def _manifest_with_the_witness_copied():
    """Свидетель прошлой сессии подкручен под текущий.

    Поле, ИСКЛЮЧЁННОЕ из сравнения по замыслу, перестаёт различаться —
    и сверка двух сессий незаметно превращается в сверку одного прогона
    с самим собой: все величины сходятся, потому что это одни и те же
    числа. Если проверка этого не ловит, исключение бессмысленно.
    """
    previous = json.loads(_manifest_text("commit-92c3b1d"))
    current = json.loads(_manifest_text("current"))
    field = "!reply_ms_total"
    for key, values in previous["axes"]["pool"].items():
        if field in values and key in current["axes"]["pool"]:
            values[field] = current["axes"]["pool"][key].get(field,
                                                             values[field])
    return json.dumps(previous, ensure_ascii=False, indent=2,
                      sort_keys=True) + chr(10)


# (имя, файл, якорь, замена). Якорь обязан встречаться в файле РОВНО раз;
# замена обязана уронить хотя бы одну проверку.
SABOTAGES = [

    # --- круг 5: сырая задержка живости, а не готовый вывод -------------

    ("delay-is-not-measured-at-all", "scripts/docker.py",
     "    started = time.time()\n    try:\n        status, body = _host(host).get(\n"
     '            "http://127.0.0.1:%d/healthz" % port, timeout=timeout)\n'
     "    except (OSError, http.client.HTTPException):\n"
     "        return None, int((time.time() - started) * 1000)\n",
     "    try:\n        status, body = _host(host).get(\n"
     '            "http://127.0.0.1:%d/healthz" % port, timeout=timeout)\n'
     "    except (OSError, http.client.HTTPException):\n"
     "        return None, 0\n"),
    ("delay-frozen-at-the-response-time", "scripts/docker.py",
     "    delay_ms = int((time.time() - started) * 1000)\n"
     "    if status != 200:\n        return None, delay_ms\n"
     '    return parse_reply(body, least=0)["inflight"], delay_ms\n',
     "    if status != 200:\n        return None, 0\n"
     '    return parse_reply(body, least=0)["inflight"], 0\n'),
    ("liveness-still-claims-a-verdict", "scripts/docker.py",
     '        "every_call_got_a_value": asks > 0 and asks == answers,\n',
     '        "every_call_got_a_value": True,\n'),
    ("liveness-max-delay-is-the-mean", "scripts/docker.py",
     '        "max_delay_ms": max(delays) if delays else 0,\n',
     '        "max_delay_ms": (sum(delays) // len(delays)) if delays else 0,\n'),
    ("liveness-does-not-check-the-setting-is-uniform", "scripts/docker.py",
     "    settings = {item[\"workers\"] for item in volleys}\n"
     "    if len(settings) != 1:\n"
     "        raise ValueError(\n"
     '            "залпы сняты при разных настройках (%s): вывод живости не "\n'
     '            "привязан ни к одной из них" % sorted(settings))\n', ""),
    ("liveness-tags-a-hardcoded-setting", "scripts/docker.py",
     '        "workers": next(iter(settings)),\n',
     '        "workers": HIGH_WORKERS,\n'),
    ("liveness-computed-from-the-low-setting", "scripts/docker.py",
     '    row["liveness"] = liveness_of(high["volleys"])\n',
     '    row["liveness"] = liveness_of(by_workers[LOW_WORKERS]["volleys"])\n'),
    ("healthz-timeout-inflated", "scripts/docker.py",
     "HEALTHZ_TIMEOUT_S = 2.0\n", "HEALTHZ_TIMEOUT_S = 60.0\n"),
    ("healthz-timeout-not-actually-used", "scripts/docker.py",
     "def read_occupancy(port, timeout=HEALTHZ_TIMEOUT_S, host=None):\n",
     "def read_occupancy(port, timeout=30.0, host=None):\n"),

    # --- круг 5: граница клиента ------------------------------------------

    ("client-bound-sampled-after-the-call", "scripts/docker.py",
     "                    client_pending = probes - sum(1 for item in pending\n"
     "                                                  if item.done())\n"
     "                    value, delay_ms = read_occupancy(port, host=host)\n",
     "                    value, delay_ms = read_occupancy(port, host=host)\n"
     "                    client_pending = probes - sum(1 for item in pending\n"
     "                                                  if item.done())\n"),
    ("client-bound-always-full", "scripts/docker.py",
     "                    client_pending = probes - sum(1 for item in pending\n"
     "                                                  if item.done())\n",
     "                    client_pending = probes\n"),
    ("client-bound-not-checked-per-call", "scripts/docker.py",
     '    row["client_bound_holds"] = all(\n'
     '        call["value"] is None or call["value"] <= call["client_pending"]\n'
     "        for call in healthz_calls)\n",
     '    row["client_bound_holds"] = True\n'),
    ("client-bound-aggregate-ignores-any-volley", "scripts/docker.py",
     '    row["client_bound_holds"] = all(\n'
     '        item["client_bound_holds"]\n'
     '        for setting in at for item in setting["volleys"])\n',
     '    row["client_bound_holds"] = any(\n'
     '        item["client_bound_holds"]\n'
     '        for setting in at for item in setting["volleys"])\n'),
    ("client-bound-dropped-from-publication", "scripts/docker.py",
     '        and row["idle_reads_zero"]\n'
     '        and row["client_bound_holds"])\n',
     '        and row["idle_reads_zero"])\n'),

    # --- круг 5: второй источник отдельным потоком, missed vs disagreed --

    ("sampler-thread-tied-to-the-healthz-loop-again", "scripts/docker.py",
     "                    sampler_thread = threading.Thread(\n"
     "                        target=_sample_second_source,\n"
     "                        args=(sampler, samples, pending, deadline))\n",
     "                    sampler_thread = threading.Thread(\n"
     "                        target=_sample_second_source,\n"
     "                        args=(sampler, samples, [], deadline))\n"),
    ("sampler-stops-as-soon-as-one-probe-finishes", "scripts/docker.py",
     "    while time.time() < deadline and not all(item.done()"
     " for item in pending):\n",
     "    while time.time() < deadline and not any(item.done()"
     " for item in pending):\n"),
    ("sampler-deadline-already-expired", "scripts/docker.py",
     "                        args=(sampler, samples, pending, deadline))\n",
     "                        args=(sampler, samples, pending, time.time()))\n"),
    ("missed-reclassified-as-agreement", "scripts/docker.py",
     "    elif not row[\"db_observed_activity\"] and row[\"counter_peak\"] >= 2:\n"
     '        row["sources_agree"] = False\n'
     '        row["sources_missed"] = True\n',
     '        row["sources_agree"] = True\n'
     '        row["sources_missed"] = False\n'),
    ("observed-activity-ignores-the-sample-values", "scripts/docker.py",
     '    row["db_observed_activity"] = bool(samples) and max(samples) >= 1\n',
     '    row["db_observed_activity"] = bool(samples)\n'),
    ("missed-and-disagreed-share-one-bucket", "scripts/dbwait.py",
     "    disagreed = [item for item in volleys\n"
     '                if item["sources_agree"] is False\n'
     '                and not item["sources_missed"]]\n'
     '    missed = [item for item in volleys if item["sources_missed"]]\n',
     "    disagreed = [item for item in volleys\n"
     '                if item["sources_agree"] is False]\n'
     "    missed = []\n"),
    ("missed-does-not-block-publication", "scripts/dbwait.py",
     '        "agree_everywhere": not disagreed and not missed and not unseen,\n',
     '        "agree_everywhere": not disagreed and not unseen,\n'),

    # --- круг 5: валидность запаса контроля и связь фикстур ---------------

    ("negative-headroom-reads-as-valid", "scripts/dbwait.py",
     '        "control_is_valid": headroom >= 0,\n',
     '        "control_is_valid": True,\n'),
    ("control-validity-not-in-publication", "scripts/dbwait.py",
     '    row["publishable"] = bool(row["control_overlaps_always"]\n'
     '                              and row["sources"]["agree_everywhere"]\n'
     '                              and row["control_is_valid"]\n'
     "                              and row[\"entry_binary_matches_variants_fixture\"])\n",
     '    row["publishable"] = bool(row["control_overlaps_always"]\n'
     '                              and row["sources"]["agree_everywhere"])\n'),
    ("entry-sha-not-measured-fresh", "scripts/dbwait.py",
     '    fresh_sha256 = dockerlib.entry_binary(name, host=host)["sha256"]\n',
     "    fresh_sha256 = variants_entry_sha256(name, variants_row=variants_row)\n"),
    ("entry-sha-comparison-always-true", "scripts/dbwait.py",
     '        "entry_binary_matches_variants_fixture": (\n'
     "            fresh_sha256 == variants_entry_sha256(\n"
     "                name, variants_row=variants_row)),\n",
     '        "entry_binary_matches_variants_fixture": True,\n'),
    ("headroom-ignores-the-injected-row", "scripts/dbwait.py",
     "    row = variants_row if variants_row is not None else _variants_row(name)\n"
     '    for setting in row["occupancy"]["at"]:\n'
     '        if setting["workers"] == CONTROL_WORKERS:\n'
     '            return setting["served_concurrently"] - probes\n',
     "    row = _variants_row(name)\n"
     '    for setting in row["occupancy"]["at"]:\n'
     '        if setting["workers"] == CONTROL_WORKERS:\n'
     '            return setting["served_concurrently"] - probes\n'),

    # --- круг 5: устойчивость ёмкости и existential не декоративны --------

    ("existential-becomes-an-unknown-kind", "images/variants.json",
     '"kind": "existential",',
     '"kind": "unverified",'),

    # --- общая логика счётчика (не тронута кругом 5, диверсии свежие) -----

    ("counter-resets-between-requests-of-the-same-connection",
     "service/main.cpp",
     "InFlight::InFlight()\n"
     "    : entered_(g_inflight.fetch_add(1, std::memory_order_relaxed) + 1) {}\n",
     "InFlight::InFlight() : entered_(1) {}\n"),
    ("counter-decrements-twice", "service/main.cpp",
     "InFlight::~InFlight() { g_inflight.fetch_sub(1, std::memory_order_relaxed); }\n",
     "InFlight::~InFlight() {\n"
     "    g_inflight.fetch_sub(1, std::memory_order_relaxed);\n"
     "    g_inflight.fetch_sub(1, std::memory_order_relaxed);\n}\n"),
    ("healthz-counts-itself-again", "service/main.cpp",
     '    return Reply{200, json_line(stand_variant(), "healthz", "served", "200",\n'
     "                                inflight_now())};\n",
     '    return Reply{200, json_line(stand_variant(), "healthz", "served", "200",\n'
     "                                inflight_now() > 0 ? inflight_now() + 1\n"
     "                                                   : inflight_now())};\n"),
    ("query-handle-does-not-carry-the-guard-value", "service/dispatch.cpp",
     "        InFlight guard;\n"
     "        return query(value, variant, guard.entered_with());\n",
     "        InFlight guard;\n        (void)guard;\n"
     "        return query(value, variant, 1);\n"),
    ("dispatch-creates-two-guards-per-request", "service/dispatch.cpp",
     "        InFlight guard;\n"
     "        return block(value, variant, guard.entered_with());\n",
     "        InFlight guard;\n        InFlight second;\n"
     "        return block(value, variant, second.entered_with());\n"),
    ("resource-refusal-list-grows-unexplained", "service/main.cpp",
     "    return sqlstate == kTooManyConnections"
     " || sqlstate == kCannotConnectNow;\n",
     "    return sqlstate == kTooManyConnections"
     ' || sqlstate == kCannotConnectNow || sqlstate == "42601";\n'),
    ("variant-mismatch-only-logged-not-refused", "service/main.cpp",
     "    if (variant != stand_variant()) {\n"
     "        throw std::logic_error(\n"
     '            "ручке передано имя варианта, отличное от прочитанного при "\n'
     '            "старте: " + variant);\n'
     "    }\n",
     "    if (variant != stand_variant()) {\n"
     "        std::fprintf(stderr, \"предупреждение: чужое имя варианта\\n\");\n"
     "    }\n"),
    ("worker-count-parsed-with-atoi-silently", "service/dispatch.cpp",
     "        int parsed = 0;\n        try {\n"
     "            parsed = std::stoi(text, &consumed);\n"
     "        } catch (const std::exception&) {\n            consumed = 0;\n        }\n",
     "        int parsed = std::atoi(text.c_str());\n"
     "        consumed = text.size();\n"),
    # --- симметрия трёх образов (диверсии свежие) --------------------------

    ("beast-dockerfile-adds-a-build-step", "images/Dockerfile.beast",
     "COPY service /src\n",
     "COPY service /src\nRUN echo beast > /tmp/layer-marker\n"),
    ("crow-header-sum-check-removed", "images/install-layer.sh",
     '    echo "${crow_sha256}  /tmp/crow_all.h" | sha256sum -c -\n', ""),
    ("base-image-digest-prefix-corrupted", "images/Dockerfile.base",
     "@sha256:",
     "@sha256-"),
    ("layer-branch-adds-a-compile-flag", "service/CMakeLists.txt",
     "elseif(STAND_LAYER STREQUAL \"crow\")\n"
     "  find_package(Threads REQUIRED)\n",
     "elseif(STAND_LAYER STREQUAL \"crow\")\n"
     "  find_package(Threads REQUIRED)\n"
     "  target_compile_definitions(stand_service PRIVATE CROW_LAYER=1)\n"),
    ("worker-knob-expression-gains-an-offset", "service/beast_main.cpp",
     "index < workers", "index < workers + 0u"),

    # --- запуск и ручки (диверсии свежие) ----------------------------------

    ("run-command-repeats-the-variant-knob", "scripts/docker.py",
     '    if "STAND_VARIANT" not in omit:\n        cmd += ["-e", "STAND_VARIANT=%s" % name]\n',
     '    if "STAND_VARIANT" not in omit:\n'
     '        cmd += ["-e", "STAND_VARIANT=%s" % name]\n'
     '        cmd += ["-e", "STAND_VARIANT=stand-default"]\n'),
    ("startup-refusal-accepts-a-slow-exit-as-alive", "scripts/docker.py",
     "        if exit_code is None:\n"
     "            raise RuntimeError(\n"
     '                "%s без %s не завершился за %.0f с: отказ не доехал до кода "\n'
     '                "возврата" % (name, missing, seconds))\n',
     "        if exit_code is None:\n            exit_code = 0\n"),
    ("ready-check-accepts-any-status-code", "scripts/docker.py",
     "            if status == 200:\n                return True\n",
     "            if status:\n                return True\n"),
    ("warm-up-probe-uses-the-real-wait", "scripts/docker.py",
     "        probe_wait(port, endpoint, 0, host=host)\n"
     "        # Занятость ДО залпа.",
     "        probe_wait(port, endpoint, wait_ms, host=host)\n"
     "        # Занятость ДО залпа."),
    ("published-number-becomes-the-mean", "scripts/docker.py",
     "    return min(runs)\n", "    return sum(runs) // len(runs)\n"),
    ("attempts-limit-quietly-doubled", "scripts/docker.py",
     "MAX_ATTEMPTS = 3\n", "MAX_ATTEMPTS = 6\n"),
    ("probe-count-drops-to-the-worker-ceiling", "scripts/docker.py",
     "PROBE_COUNT = 4\n", "PROBE_COUNT = 3\n"),
    ("wait-quietly-grows-tenfold", "scripts/docker.py",
     "WAIT_MS = 1000\n", "WAIT_MS = 10000\n"),

    # --- контракт строки ---------------------------------------------------
    #
    # РОДОВАЯ ЧАСТЬ КОНТРАКТА ПЕРЕЕХАЛА В `cpp/_common/outcome.py`, и
    # якоря трёх диверсий уехали вместе с ней: они искали в
    # `scripts/outcome.py` текст, которого там больше нет, и молча НЕ
    # ПРИМЕНЯЛИСЬ. Пока копия стенда вообще не собиралась (см.
    # `_copy_stand`), это было не видно: набор краснел у всех. Путь
    # `../_common/...` резолвится внутри копии — она воспроизводит
    # расположение стенда в репозитории.

    ("contract-marker-accepted-mid-file", "../_common/outcome.py",
     "        lines = [l for l in text.split(\"\\n\") if l.strip()]\n"
     "        return bool(lines) and lines[-1].strip() == self.END_MARKER\n",
     '        return self.END_MARKER in text\n'),
    ("named-field-accepts-whitespace-only", "../_common/outcome.py",
     "            if not str(row[field]).strip():\n"
     '                raise ValueError("поле %s пусто" % field)\n',
     "            if row[field] == \"\":\n"
     '                raise ValueError("поле %s пусто" % field)\n'),
    ("outcome-whitelist-gains-a-fourth-value", "scripts/outcome.py",
     '    outcomes=("served", "refused", "error"),\n',
     '    outcomes=("served", "refused", "error", "degraded"),\n'),
    ("empty-measurement-file-is-accepted", "../_common/outcome.py",
     "        if not rows:\n"
     '            raise ValueError("%s: нет ни одной строки данных" % path)\n',
     "        pass\n"),

    # --- фикстура занятости: подмены (диверсии свежие) ---------------------

    ("fixture-delay-contradicts-the-liveness-summary",
     "fixtures/variants.jsonl", END_MARKER + "\n",
     _spoiled_row("beast", patch=lambda row: row["liveness"].update(
         max_delay_ms=0))),
    ("fixture-client-bound-declared-without-its-calls",
     "fixtures/variants.jsonl", END_MARKER + "\n",
     _spoiled_row("crow", patch=lambda row: row["occupancy"]["at"][2][
         "volleys"][0].update(client_bound_holds=True,
                              healthz_calls=[{"value": 99, "delay_ms": 1,
                                             "client_pending": 1}]))),
    ("fixture-liveness-workers-tag-does-not-match-occupancy",
     "fixtures/variants.jsonl", END_MARKER + "\n",
     _spoiled_row("drogon", patch=lambda row: row["liveness"].update(
         workers=1))),
    ("fixture-capacity-declared-stable-anyway",
     "fixtures/variants.jsonl", END_MARKER + "\n",
     _spoiled_row("drogon", capacity_stable=True)),

    # --- фикстура ожидания в базе: подмены (диверсии свежие) ---------------

    ("db-fixture-sha-mismatch-hidden", "fixtures/db-wait.jsonl",
     END_MARKER + "\n",
     _spoiled_db_row("beast", entry_binary_sha256="0" * 64,
                     entry_binary_matches_variants_fixture=True)),
    ("db-fixture-headroom-negative-still-valid", "fixtures/db-wait.jsonl",
     END_MARKER + "\n",
     _spoiled_db_row("crow", control_headroom=-1, control_is_valid=True)),
    ("db-fixture-missed-count-zeroed-out", "fixtures/db-wait.jsonl",
     END_MARKER + "\n",
     _spoiled_db_row("beast", patch=lambda row: (
         row["at"][1]["volleys"][0].update(
             db_peak=0, db_samples=[0], db_observed_activity=False,
             sources_agree=False, sources_missed=True),
         row["sources"].update(missed=0, disagreed=0,
                               agree_everywhere=True),
         row.update(publishable=True)))),
    ("db-fixture-blind-second-source", "fixtures/db-wait.jsonl",
     END_MARKER + "\n", _spoiled_db_rows(_blind_second_source)),
    ("db-fixture-publishable-ignores-headroom-validity",
     "fixtures/db-wait.jsonl", END_MARKER + "\n",
     _spoiled_db_row("crow", control_is_valid=False, publishable=True)),

    # --- задача 3: оснастка базы (диверсии свежие) -------------------------
    #
    # Целятся в то, чем счётчики из базы врут МОЛЧА: готовность, которую
    # выждали вместо того, чтобы проверить; число, отданное строкой;
    # фильтр по имени варианта, который ничего не фильтрует; счёт
    # подготовленных запросов, отвечающий про наблюдателя; отсутствие
    # расширения, прочитанное как ноль вызовов; и база, пережившая свой
    # замер.

    ("pg-readiness-waited-out-not-polled", "scripts/pg.py",
     "    deadline = time.time() + timeout_s\n"
     "    attempts = 0\n"
     "    while time.time() < deadline:\n"
     "        attempts += 1\n"
     '        done = _psql("SELECT 1", container=container, host=host,\n'
     "                     database=SUPERUSER)\n"
     '        if done.returncode == 0 and (done.stdout or "").strip() == "1":\n'
     "            return attempts\n"
     "        time.sleep(0.1)\n",
     "    time.sleep(3)\n    return 1\n"),
    ("pg-readiness-accepts-a-mute-exit-code", "scripts/pg.py",
     '        if done.returncode == 0 and (done.stdout or "").strip() == "1":\n',
     "        if done.returncode == 0:\n"),
    ("pg-readiness-back-to-pg-isready", "scripts/pg.py",
     '        done = _psql("SELECT 1", container=container, host=host,\n'
     "                     database=SUPERUSER)\n",
     "        done = dockerlib._host(host).try_run(\n"
     '            ["docker", "exec", container, "pg_isready"])\n'),
    ("pg-counter-answers-with-a-string", "scripts/pg.py",
     "    return int(text)\n\n\ndef _application_name(value):\n",
     "    return text\n\n\ndef _application_name(value):\n"),
    ("pg-unanswerable-question-becomes-zero", "scripts/pg.py",
     "    if done.returncode != 0 or not text.isdigit():\n"
     "        raise RuntimeError(\n"
     '            "%s: база не ответила числом: rc=%s, %r"\n'
     '            % (what, done.returncode, (done.stdout or "")'
     ' + (done.stderr or "")))\n',
     "    if done.returncode != 0 or not text.isdigit():\n        return 0\n"),
    ("pg-empty-application-name-is-allowed", "scripts/pg.py",
     "    if not isinstance(value, str) or not value.strip():\n",
     "    if value is None:\n"),
    ("pg-connection-filter-matches-by-substring", "scripts/pg.py",
     '        _psql("SELECT count(*) FROM pg_stat_activity"\n'
     "              \" WHERE application_name = '%s'\" % name,\n",
     '        _psql("SELECT count(*) FROM pg_stat_activity"\n'
     "              \" WHERE application_name LIKE '%%%s%%'\" % name,\n"),
    ("pg-active-queries-ignore-the-state", "scripts/pg.py",
     "        \" WHERE application_name = '%s' AND state = 'active'\"\n"
     '        " ORDER BY pid" % name',
     "        \" WHERE application_name = '%s'\"\n"
     '        " ORDER BY pid" % name'),
    ("pg-active-count-counts-idle-connections-too", "scripts/pg.py",
     "              \" WHERE application_name = '%s' AND state = 'active'\""
     " % name,\n",
     "              \" WHERE application_name = '%s'\" % name,\n"),
    ("pg-active-queries-drop-the-query-text", "scripts/pg.py",
     '        rows.append({"pid": int(pid), "state": state, "query": query})\n',
     '        rows.append({"pid": int(pid), "state": state, "query": ""})\n'),
    ("pg-prepared-counts-the-observers-own-session", "scripts/pg.py",
     "    if ask is None:\n"
     "        raise SessionScopedView(\n"
     '            "подготовленные запросы варианта %s из чужой сессии не '
     'видны: "\n'
     '            "pg_prepared_statements — представление сессии без pid и "\n'
     '            "application_name; нужен исполнитель ВНУТРИ соединения '
     'варианта"\n'
     "            % application_name)\n",
     "    if ask is None:\n"
     "        return _number(\n"
     '            _psql("SELECT count(*) FROM pg_prepared_statements",\n'
     "                  container=container, host=host),\n"
     '            "подготовленные запросы")\n'),
    ("pg-prepared-does-not-check-the-variant-is-connected", "scripts/pg.py",
     "    if connections(application_name, container=container, host=host)"
     " == 0:\n"
     "        raise RuntimeError(\n"
     '            "варианта %s нет в базе ни одним соединением: счёт '
     'подготовленных "\n'
     '            "запросов был бы про отсутствие соединения, а не про '
     'отсутствие "\n'
     '            "подготовки" % application_name)\n', ""),
    ("pg-prepared-accepts-a-non-numeric-answer", "scripts/pg.py",
     "    if not text.isdigit():\n"
     "        raise RuntimeError(\n"
     '            "сессия варианта %s ответила про подготовленные запросы не "\n'
     '            "числом: %r" % (application_name, answer))\n'
     "    return int(text)\n",
     "    return int(text) if text.isdigit() else 0\n"),
    ("pg-preload-flag-not-passed-at-start", "scripts/pg.py",
     "    if statements:\n"
     '        cmd += ["-c", PRELOAD_FLAG]\n',
     "    if False:\n"
     '        cmd += ["-c", PRELOAD_FLAG]\n'),
    ("pg-statistics-flag-ignored-by-the-database-object", "scripts/pg.py",
     "        if not self.statements:\n", "        if False:\n"),
    ("pg-statement-calls-take-one-row-instead-of-the-sum", "scripts/pg.py",
     '"SELECT coalesce(sum(calls), 0) FROM %s',
     '"SELECT coalesce(max(calls), 0) FROM %s'),
    ("pg-statement-calls-match-the-whole-text", "scripts/pg.py",
     "              \" WHERE position('%s' in query) > 0\"\n",
     "              \" WHERE query = '%s'\"\n"),

    # --- задача 3, круг 2: то, что ревью нашло НЕЗАЩИЩЁННЫМ --------------
    #
    # Семь диверсий ревьюера прошли зелёными, четыре из них — и с докером
    # тоже. Ниже диверсии придуманы заново, по местам, которые те находки
    # назвали: поиск подстроки метасимволами, ноль на пустом наборе строк,
    # сторож дайджеста, который никогда не исполнялся, фильтр по имени
    # варианта, отключаемый припиской к условию, уборка сети, снятие
    # остатка не по тому имени, адрес базы без имени контейнера и
    # умолчание статистики.

    ("pg-fragment-search-back-to-like-metacharacters", "scripts/pg.py",
     "              \" WHERE position('%s' in query) > 0\"\n",
     "              \" WHERE query LIKE '%%%s%%'\"\n"),
    ("pg-statement-calls-lose-the-zero-on-an-empty-set", "scripts/pg.py",
     '        _psql("SELECT coalesce(sum(calls), 0) FROM %s"\n',
     '        _psql("SELECT sum(calls) FROM %s"\n'),
    ("pg-digest-guard-never-runs", "scripts/pg.py",
     '    if "@sha256:" not in spec["image"]:\n', "    if False:\n"),
    ("pg-connection-filter-disabled-by-a-tautology", "scripts/pg.py",
     "              \" WHERE application_name = '%s'\" % name,\n",
     "              \" WHERE application_name = '%s' OR true\" % name,\n"),
    ("pg-stop-leaves-the-network-behind", "scripts/pg.py",
     '    runner.try_run(["docker", "network", "rm", network])\n', ""),
    ("pg-leftover-removal-targets-the-module-default", "scripts/pg.py",
     "    # Остаток прошлого прогона убирается ДО старта: контейнер, "
     "переживший\n"
     "    # предыдущий замер, — это чужие соединения в счётчиках этого.\n"
     '    runner.try_run(["docker", "rm", "-f", container])\n',
     "    # Остаток прошлого прогона убирается ДО старта: контейнер, "
     "переживший\n"
     "    # предыдущий замер, — это чужие соединения в счётчиках этого.\n"
     '    runner.try_run(["docker", "rm", "-f", CONTAINER])\n'),
    ("pg-address-drops-the-container-name", "scripts/pg.py",
     "            % (container, DATABASE, ROLE, PASSWORD))\n",
     "            % (CONTAINER, DATABASE, ROLE, PASSWORD))\n"),
    ("pg-statistics-default-flips-to-off", "scripts/pg.py",
     "def start(host=None, container=CONTAINER, network=NETWORK,"
     " statements=True,\n",
     "def start(host=None, container=CONTAINER, network=NETWORK,"
     " statements=False,\n"),
    ("pg-teardown-leaves-the-database-running", "scripts/pg.py",
     "    def __exit__(self, kind, value, trace):\n"
     "        self.stop()\n"
     "        return False\n",
     "    def __exit__(self, kind, value, trace):\n"
     "        return False\n"),
    ("pg-leftover-container-not-removed-before-start", "scripts/pg.py",
     "    # Остаток прошлого прогона убирается ДО старта: контейнер, "
     "переживший\n"
     "    # предыдущий замер, — это чужие соединения в счётчиках этого.\n"
     '    runner.try_run(["docker", "rm", "-f", container])\n', ""),
    ("pg-image-pinned-by-a-tag-again", "images/postgres.json",
     "postgres@sha256:4aabea78cf39b90e834caf3af7d602a18565f6fe2508705c"
     "8d01aa63245c2e20", "postgres:18"),
    ("pg-dbwait-raises-its-own-postgres-again", "scripts/dbwait.py",
     "    pg.start(host=host, statements=False, timeout_s=timeout_s)\n",
     "    runner = dockerlib._host(host)\n"
     '    runner.try_run(["docker", "network", "create", NETWORK])\n'
     '    runner.run(["docker", "run", "-d", "--name", POSTGRES,\n'
     '                "--network", NETWORK, "-e", '
     '"POSTGRES_PASSWORD=postgres",\n'
     "                POSTGRES_IMAGE])\n"
     "    pg.wait_ready(timeout_s=timeout_s, container=POSTGRES, host=host)\n"
     "    pg._create_role(container=POSTGRES, host=host)\n"),
    # --- задача 5, ось 2: контроль по размеру пула ------------------------

    ("pool-control-frozen-at-yes", "scripts/pool.py",
     '        "knob_moves_counter": (low["connections_observed"]\n'
     '                               < high["connections_observed"]),\n',
     '        "knob_moves_counter": True,\n'),
    ("pool-ceiling-declared-not-checked", "scripts/pool.py",
     '        "low_within_pool": low["connections_observed"] <= low_size,\n',
     '        "low_within_pool": True,\n'),
    ("pool-control-satisfied-by-one-variant", "scripts/pool.py",
     '        "pool_publishable": bool(\n'
     '            all(item["knob_moves_counter"] and item["low_within_pool"]\n',
     '        "pool_publishable": bool(\n'
     '            any(item["knob_moves_counter"] and item["low_within_pool"]\n'),
    ("pool-published-number-is-the-luckiest-volley", "scripts/pool.py",
     "    return max(values)\n", "    return min(values)\n"),
    ("pool-settings-may-be-measured-by-different-run-counts",
     "scripts/pool.py",
     '    if len(low["connections_by_run"]) != len(high["connections_by_run"]):\n'
     "        raise ValueError(\n"
     '            "%s: настройки сняты разным числом залпов (%d против %d): "\n'
     '            "больший максимум добывается бо́льшим числом попыток, а не "\n'
     '            "подключённой ручкой"\n'
     '            % (variant, len(low["connections_by_run"]),\n'
     '               len(high["connections_by_run"])))\n', ""),
    ("pool-pair-may-differ-in-everything-else", "scripts/pool.py",
     '    for field in ("concurrent", "workers", "sleep_ms", "runs", "driver",\n'
     '                  "entry_binary_sha256"):\n'
     "        if low[field] != high[field]:\n"
     "            raise ValueError(\n"
     '                "%s: пара по размеру пула различается ещё и полем %s (%r "\n'
     '                "против %r) — контролем это уже не является"\n'
     "                % (variant, field, low[field], high[field]))\n", ""),
    ("pool-a-leak-does-not-stop-publication", "scripts/pool.py",
     "            and no_leak and all_served and nothing_before and one_driver),\n",
     "            and all_served and nothing_before and one_driver),\n"),
    ("pool-unserved-probes-do-not-stop-publication", "scripts/pool.py",
     "            and no_leak and all_served and nothing_before and one_driver),\n",
     "            and no_leak and nothing_before and one_driver),\n"),
    ("pool-table-prints-without-a-control", "scripts/pool.py",
     '    if not decision["pool_publishable"]:\n'
     "        raise ValueError(\n",
     "    if False:\n"
     "        raise ValueError(\n"),

    # --- задача 5: откуда берётся число (тавтология) ----------------------

    ("pool-counter-answers-a-constant-instead-of-the-database",
     "scripts/pool.py",
     "    return lambda: pg.connections(name, host=host)\n",
     "    return lambda: CONCURRENT\n"),
    ("pool-counter-asks-about-another-variant", "scripts/pool.py",
     "    return lambda: pg.connections(name, host=host)\n",
     '    return lambda: pg.connections("beast", host=host)\n'),
    ("pool-a-database-refusal-becomes-zero-connections", "scripts/pool.py",
     "    return lambda: pg.connections(name, host=host)\n",
     "    def ask():\n"
     "        try:\n"
     "            return pg.connections(name, host=host)\n"
     "        except Exception:\n"
     "            return 0\n"
     "    return ask\n"),
    ("drogon-own-pool-counts-a-constant", "scripts/drogonpool.py",
     "    return pool.sampler_for(APPLICATION_NAME, host=host)\n",
     "    return lambda: 8\n"),
    ("drogon-own-pool-counts-the-axis-variant", "scripts/drogonpool.py",
     "    return pool.sampler_for(APPLICATION_NAME, host=host)\n",
     '    return pool.sampler_for("drogon", host=host)\n'),

    # --- задача 5: ручка размера пула -------------------------------------

    ("pool-size-guessed-when-the-caller-forgot-it", "scripts/docker.py",
     '    if pool is None and "STAND_POOL" not in omit:\n'
     "        raise ValueError(\n",
     '    if pool is None and "STAND_POOL" not in omit:\n'
     "        pool = POOL_FOR_OTHER_AXES\n"
     "    if False:\n"
     "        raise ValueError(\n"),
    ("pool-size-not-passed-at-launch-at-all", "scripts/docker.py",
     '    if "STAND_POOL" not in omit:\n'
     '        cmd += ["-e", "STAND_POOL=%d" % pool]\n', ""),
    ("pool-size-is-no-longer-a-knob", "scripts/docker.py",
     'KNOBS = ("STAND_VARIANT", "STAND_WORKERS", "STAND_POOL")\n',
     'KNOBS = ("STAND_VARIANT", "STAND_WORKERS")\n'),
    ("pool-size-accepts-anything-at-all", "scripts/docker.py",
     "    if pool is not None and (isinstance(pool, bool)\n"
     "                             or not isinstance(pool, int) or pool <= 0):\n"
     "        raise ValueError(\n"
     '            "размер пула обязан быть положительным целым, получено %r"\n'
     "            % (pool,))\n", ""),
    ("pool-size-baked-into-the-image", "images/Dockerfile.beast",
     "RUN /usr/local/bin/install-layer.sh beast\n",
     "RUN /usr/local/bin/install-layer.sh beast\nENV STAND_POOL=8\n"),

    # --- задача 5: пул в самом сервисе ------------------------------------

    ("pool-slot-never-comes-back", "service/pool.hpp",
     "        ~Lease() {\n"
     "            if (owner_ == nullptr) return;\n"
     "            if (discarded_ || held_ == nullptr) {\n"
     "                owner_->drop_slot();\n"
     "            } else {\n"
     "                owner_->give_back(std::move(held_));\n"
     "            }\n"
     "        }\n",
     "        ~Lease() {}\n"),
    ("pool-ceiling-is-not-a-ceiling", "service/pool.hpp",
     "        free_.wait(lock, [this] { return !idle_.empty()"
     " || open_ < size_; });\n",
     "        free_.wait(lock, [this] { return true; });\n"),
    ("pool-discarded-connection-returns-to-the-set", "service/pool.hpp",
     "            if (discarded_ || held_ == nullptr) {\n",
     "            if (held_ == nullptr) {\n"),
    ("pool-a-failed-connect-eats-the-slot", "service/pool.hpp",
     "        } catch (...) {\n"
     "            drop_slot();\n"
     "            throw;\n"
     "        }\n",
     "        } catch (...) {\n"
     "            throw;\n"
     "        }\n"),
    ("pool-broken-connection-goes-back-as-a-live-one", "service/main.cpp",
     "            lease.discard();\n", ""),
    ("pool-query-opens-its-own-connection-past-the-set", "service/main.cpp",
     "        ConnectionPool<pqxx::connection>::Lease lease =\n"
     "            connections().acquire();\n",
     "        std::unique_ptr<pqxx::connection> held = connect(variant);\n"),
    ("pool-size-defaults-inside-the-service", "service/main.cpp",
     '        const char* raw = std::getenv("STAND_POOL");\n'
     "        if (raw == nullptr || *raw == '\\0') {\n"
     "            throw std::runtime_error(\n",
     '        const char* raw = std::getenv("STAND_POOL");\n'
     "        if (raw == nullptr || *raw == '\\0') {\n"
     "            return 8;\n"
     "        }\n"
     "        if (false) {\n"
     "            throw std::runtime_error(\n"),
    ("pool-size-read-after-the-port-opens", "service/beast_main.cpp",
     "        stand_pool_size();\n", ""),

    # --- задача 5: сама фикстура оси 2 ------------------------------------

    ("pool-fixture-counter-does-not-move", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("beast", 8, connections_observed=1,
                       connections_by_run=[1, 1, 1])),
    ("pool-fixture-small-pool-opened-five", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("crow", 1, connections_observed=5,
                       connections_by_run=[5, 5, 5])),
    ("pool-fixture-leaked-between-volleys", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 8, connections_idle_after=30,
                       connections_idle_after_by_run=[30, 30, 30])),
    ("pool-fixture-pair-differs-in-concurrency", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("beast", 8, concurrent=16)),
    ("pool-fixture-unserved-probes-are-not-noticed", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("crow", 8, not_served=5)),
    ("drogon-own-pool-declared-part-of-the-axis", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 8, kind="drogon-own-pool",
                       used_in_axis=True)),
    ("drogon-own-pool-row-snatched-the-axis-driver", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 1, kind="drogon-own-pool",
                       driver="libpqxx")),
    # --- круг ревью: выводы оси, привязанные к фикстуре -------------------

    ("reading-calls-every-row-saturated", "scripts/pool.py",
     '        "saturated_the_pool": observed == size,\n',
     '        "saturated_the_pool": True,\n'),
    ("reading-never-sees-the-layer-ceiling", "scripts/pool.py",
     '        "below_pool_at_inflight_peak": (inflight is not None\n'
     '                             and observed == inflight < size),\n',
     '        "below_pool_at_inflight_peak": False,\n'),
    ("reading-counts-the-queue-as-idle", "scripts/pool.py",
     '        "queue_occupies_units": (inflight is not None and inflight > observed),\n',
     '        "queue_occupies_units": True,\n'),
    ("reading-allows-more-connections-than-requests", "scripts/pool.py",
     '        "more_connections_than_requests": (inflight is not None\n'
     '                                           and observed > inflight),\n',
     '        "more_connections_than_requests": False,\n'),
    ("impossible-rows-do-not-stop-publication", "scripts/pool.py",
     "            and not impossible\n", ""),
    ("the-before-reading-is-optional-again", "scripts/pool.py",
     '    missing = [row["variant"] for row in rows if "connections_before" not in row]\n'
     "    if missing:\n"
     "        raise KeyError(\n"
     '            "в строках %s нет connections_before: «до залпа ничего не было "\n'
     '            "открыто» проверялось бы на отсутствующем поле" % sorted(missing))\n'
     '    nothing_before = all(row["connections_before"] == 0 for row in rows)\n',
     '    nothing_before = all(row["connections_before"] == 0 for row in rows\n'
     '                         if "connections_before" in row)\n'),
    ("the-leak-is-measured-against-the-volley", "scripts/pool.py",
     '    no_leak = all(row["connections_idle_after"] <= row["pool_size"]\n'
     "                  for row in rows)\n",
     '    no_leak = all(row["connections_idle_after"] <= row["concurrent"]\n'
     "                  for row in rows)\n"),
    ("stability-is-declared-not-computed", "scripts/pool.py",
     '        "connections_stable": min(by_run) == max(by_run),\n',
     '        "connections_stable": True,\n'),
    ("the-volley-barrier-loses-its-deadline", "scripts/pool.py",
     "    gate = threading.Barrier(concurrent, timeout=BARRIER_TIMEOUT_S)\n",
     "    gate = threading.Barrier(concurrent)\n"),
    ("the-pool-for-the-other-axes-drops-to-one", "scripts/docker.py",
     "POOL_FOR_OTHER_AXES = 8\n", "POOL_FOR_OTHER_AXES = 1\n"),
    ("the-axis-one-row-forgets-its-pool", "scripts/docker.py",
     '    row["pool"] = pool\n', ""),

    # --- круг ревью: проба собственного пула доказывает себя ---------------

    ("the-framework-probe-links-the-axis-client", "scripts/drogonpool.py",
     '    "-ldrogon -ltrantor -ljsoncpp -lpthread && "\n',
     '    "-ldrogon -ltrantor -ljsoncpp -lpqxx -lpq -lpthread && "\n'),
    ("the-framework-probe-reports-links-it-did-not-take", "scripts/drogonpool.py",
     '        "links_libpqxx": any(name.startswith("libpqxx") for name in linked),\n',
     '        "links_libpqxx": False,\n'),
    ("the-framework-probe-declares-success-without-running",
     "scripts/drogonpool.py",
     '        "probe_finished_every_query": "PROBE DONE" in (proc.stdout or ""),\n',
     '        "probe_finished_every_query": True,\n'),

    # --- круг ревью: README не расходится с фикстурой ----------------------

    ("readme-promises-liveness-nobody-measured", "README.md",
     "**У drogon одно обращение из пятнадцати осталось БЕЗ ответа**",
     "Маршрут живости НЕ отказывает и не виснет — `every_call_got_a_value`\n"
     "держится у всех трёх. **У drogon одно обращение из пятнадцати осталось "
     "БЕЗ ответа**"),
    ("readme-rounds-the-liveness-table-up", "README.md",
     "| drogon | 15 | 14 | 2003 мс |\n",
     "| drogon | 15 | 15 | 2003 мс |\n"),
    ("readme-prints-a-pool-number-of-its-own", "README.md",
     "| crow | 1 | 7 | 7 | 7, 7, 7 | 24 из 24 |\n",
     "| crow | 1 | 8 | 7 | 7, 7, 7 | 24 из 24 |\n"),
    ("readme-hides-the-layer-ceiling", "README.md",
     "| drogon | 1 | 6 | 6 | **5, 5, 6** | 24 из 24 |\n",
     "| drogon | 1 | 6 | 8 | **5, 5, 6** | 24 из 24 |\n"),

    # --- круг ревью: запрет на возврат порванного соединения ---------------

    ("the-discard-moves-to-a-branch-that-never-runs", "service/main.cpp",
     "            lease.discard();\n            throw;\n",
     "            throw;\n"),
    ("the-failed-query-is-swallowed-with-the-slot", "service/main.cpp",
     "            lease.discard();\n            throw;\n",
     "            lease.discard();\n"),

    # --- круг ревью: замер оси 1 берёт образ по тегу ------------------------

    ("db-wait-measures-whatever-the-tag-carries", "scripts/run-db-wait.py",
     "            print(\"собираю %s\" % name)\n"
     "            dockerlib.build_variant(name)\n", ""),

    # --- круг ревью: сама фикстура против своих выводов ---------------------

    ("pool-fixture-numbers-pulled-up-to-the-setting", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("crow", 8, connections_observed=8,
                       connections_by_run=[8, 8, 8])),
    ("pool-fixture-claims-idle-workers-at-pool-one", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("beast", 1, inflight_peak=1)),
    ("pool-fixture-row-supports-neither-conclusion", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("crow", 8, inflight_peak=8)),
    ("pool-fixture-calls-a-floating-number-stable", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 8, connections_stable=True,
                       connections_by_run=[5, 5, 6])),
    ("framework-fixture-hides-the-axis-client", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 8, kind="drogon-own-pool",
                       links_libpqxx=True)),
    ("framework-fixture-keeps-a-failed-probe", "fixtures/pool.jsonl",
     END_MARKER + "\n",
     _spoiled_pool_row("drogon", 1, kind="drogon-own-pool",
                       probe_exit_code=2)),
    # --- ось 3: параметр против литерала ---------------------------------

    ("prepared-classify-calls-a-parameter-a-value", "scripts/prepared.py",
     '    if has_param and not has_literal:\n        return \"parameterized\"\n',
     '    if has_param and not has_literal:\n        return \"literal\"\n'),
    ("prepared-classify-accepts-a-text-with-both", "scripts/prepared.py",
     "    if has_literal and not has_param:\n", "    if has_literal:\n"),
    ("prepared-the-reading-is-handed-the-whole-row", "scripts/prepared.py",
     "def classify(text):\n", "def classify(text, row=None):\n"),
    ("prepared-control-is-not-required-for-publication", "scripts/prepared.py",
     "            control_holds and arms_hold and seen and reached "
     "and same_driver\n",
     "            arms_hold and seen and reached and same_driver\n"),
    ("prepared-control-believes-the-declared-mode", "scripts/prepared.py",
     '    control_holds = (control_row[\"parameterized_texts\"] == 0\n'
     '                     and control_row[\"literal_texts\"] > 0)\n',
     '    control_holds = control_row[\"sql_mode\"] == LITERAL_MODE\n'),
    ("prepared-observations-are-not-required", "scripts/prepared.py",
     '    seen = all(row[\"texts_sampled\"] > 0 for row in\n',
     '    seen = all(row[\"texts_sampled\"] >= 0 for row in\n'),
    ("prepared-one-good-arm-is-enough", "scripts/prepared.py",
     '    arms_hold = all(row_for(rows, name)[\"literal_texts\"] == 0\n',
     '    arms_hold = any(row_for(rows, name)[\"literal_texts\"] == 0\n'),
    ("prepared-the-probe-client-is-taken-on-trust", "scripts/prepared.py",
     '    same_driver = (bool(control_row.get("links_libpqxx"))\n'
     '                   and any(name.startswith("libpqxx")\n'
     '                           for name in control_row.get("probe_links", [])))\n',
     '    same_driver = True\n'),
    ("prepared-the-probe-links-are-never-snapped", "scripts/prepared.py",
     '        "links_libpqxx": any(name.startswith("libpqxx") for name in linked),\n',
     '        "links_libpqxx": True,\n'),
    ("prepared-the-table-prints-anyway", "scripts/prepared.py",
     '    decision = verdict(rows, control=control)\n'
     '    if not decision[\"prepared_publishable\"]:\n',
     "    decision = verdict(rows, control=control)\n    if False:\n"),
    ("prepared-calls-counted-by-like-again", "scripts/pg.py",
     "              \" WHERE position('%s' in query) > 0\"\n",
     "              \" WHERE query LIKE '%%' || '%s' || '%%'\"\n"),
    ("prepared-the-control-probe-sends-a-parameter", "service/literal_sql_probe.cpp",
     "            tx.exec(literal_sleep_sql(static_cast<double>(sleep_ms) / 1000.0));\n",
     "            tx.exec_params(\"SELECT pg_sleep($1)\", static_cast<double>(sleep_ms) / 1000.0);\n"),
    ("prepared-the-control-probe-forgets-its-name", "scripts/prepared.py",
     '        "-e", "STAND_APPLICATION_NAME=%s" % CONTROL,\n',
     '        "-e", "STAND_UNUSED=%s" % CONTROL,\n'),
    ("prepared-the-literal-text-moves-into-the-service", "service/main.cpp",
     '            tx.exec_params(\"SELECT pg_sleep($1)\",\n',
     '            tx.exec(\"SELECT pg_sleep(1.5)\"); tx.exec_params(\"SELECT pg_sleep($1)\",\n'),
    ("prepared-fixture-control-arm-shows-a-parameter",
     "fixtures/prepared.jsonl", END_MARKER + "\n",
     _spoiled_prepared_row("literal-sql-probe", parameterized_texts=1,
                           literal_texts=28)),
    ("prepared-fixture-control-arm-saw-nothing", "fixtures/prepared.jsonl",
     END_MARKER + "\n",
     _spoiled_prepared_row("literal-sql-probe", texts_sampled=0,
                           literal_texts=0, parameterized_texts=0,
                           distinct_texts=[])),
    # --- задача 8: маркер полного прогона и сводка ------------------------

    ("marker-fixture-changed-but-the-sum-is-not", "fixtures/cancel.jsonl",
     END_MARKER + "\n", _duplicated_cancel_row()),
    ("marker-forgets-the-fourth-axis", "scripts/run-marker.py",
     'FIXTURE_FILES = ("variants.jsonl", "db-wait.jsonl", "pool.jsonl",\n'
     '                 "prepared.jsonl", "cancel.jsonl")\n',
     'FIXTURE_FILES = ("variants.jsonl", "db-wait.jsonl", "pool.jsonl",\n'
     '                 "prepared.jsonl")\n'),
    ("marker-counts-test-files-instead-of-tests", "scripts/run-marker.py",
     "    loader = unittest.TestLoader()\n"
     '    suite = loader.discover(os.path.join(HERE, "scripts"), '
     'pattern="test_*.py",\n'
     "                            top_level_dir=HERE)\n",
     "    return len([name for name in "
     'os.listdir(os.path.join(HERE, "scripts"))\n'
     '                if name.startswith("test_")])\n'
     "    loader = unittest.TestLoader()\n"
     '    suite = loader.discover(os.path.join(HERE, "scripts"), '
     'pattern="test_*.py",\n'
     "                            top_level_dir=HERE)\n"),
    ("run-all-puts-the-marker-before-the-checks", "bench/run-all.sh",
     'step "весь набор проверок" "$PY" -m unittest discover -s scripts '
     "-p 'test_*.py' -t .\n"
     "\n"
     "# Маркер держит СОСТОЯНИЕ, при котором прогон объявлен полным: суммы\n"
     "# пяти фикстур, число проверок, число диверсий.\n"
     'step "маркер полного прогона" "$PY" scripts/run-marker.py\n',
     'step "маркер полного прогона" "$PY" scripts/run-marker.py\n'
     'step "весь набор проверок" "$PY" -m unittest discover -s scripts '
     "-p 'test_*.py' -t .\n"),
    ("run-all-does-not-check-the-step-return-code", "bench/run-all.sh",
     "    rc=$?\n", "    rc=0\n"),
    ("manifest-witness-made-to-coincide",
     "fixtures/manifests/commit-92c3b1d.json",
     _manifest_text("commit-92c3b1d"), _manifest_with_the_witness_copied()),
    ("manifest-field-dropped-from-publication", "scripts/run-manifest.py",
     '        "inflight_peak": None,\n', ""),
    ("readme-cancel-table-loses-the-finding", "README.md",
     "| beast | 0 | 1 | 1 | 0 |\n",
     "| beast | 0 | 1 | 0 | 0 |\n"),
]


def _run_checks(work, with_docker=False):
    """Прогон набора проверок в копии — С ДОКЕРОМ ИЛИ БЕЗ, но ЯВНО.

    ОБЛАСТЬ ВИДИМОСТИ ПРОГОНА — ЧАСТЬ ЕГО РЕЗУЛЬТАТА. Живые проверки
    пропускаются без `STAND_WITH_DOCKER=1`, и диверсия, которую ловят
    только они, проходит зелёной, ничем не отличаясь от той, которую не
    ловит ничто. Переменная выставляется и СНИМАЕТСЯ здесь, а не
    наследуется: иначе один и тот же прогон значил бы разное в зависимости
    от того, что экспортировал оператор, — и «82 из 82» нельзя было бы
    прочитать.
    """
    env = dict(os.environ)
    if with_docker:
        env["STAND_WITH_DOCKER"] = "1"
    else:
        env.pop("STAND_WITH_DOCKER", None)
    try:
        proc = subprocess.run(
            [sys.executable, "-m", "unittest", "discover", "-s", "scripts",
             "-p", "test_*.py"],
            cwd=work, capture_output=True, text=True, env=env,
            timeout=DOCKER_TIMEOUT_S if with_docker else CHECK_TIMEOUT_S)
    except subprocess.TimeoutExpired:
        # Повисший прогон — тоже не «зелёный»: проверки не прошли.
        return 1, ["ERROR: прогон проверок повис и был прерван"]
    dropped = [line for line in proc.stderr.splitlines()
               if line.startswith("FAIL:") or line.startswith("ERROR:")]
    return proc.returncode, dropped


# ДИВЕРСИИ, КОТОРЫЕ ЛОВЯТСЯ ТОЛЬКО ЖИВОЙ ПРОВЕРКОЙ. Их бессмысленно
# гонять без докера: там они пройдут зелёными не потому, что проверок
# нет, а потому, что проверки пропущены. Смешивать эти два исхода в одном
# числе — то же самое, что смешивать «источник разошёлся» и «источник не
# спросили»: оба выглядят как «не сработало».
#
# Список НЕ освобождает от проверки: он переносит её в проход
# `--with-docker`, где эти имена обязаны краснеть. Штатный проход
# печатает их отдельной строкой, чтобы «столько-то из стольких-то»
# читалось вместе с областью видимости.
DOCKER_ONLY = {
    # Имя приложения, не доехавшее до пробы: снаружи она становится
    # безымянной, её строки в pg_stat_activity не находятся вовсе, и
    # контроль держится на пустых данных. По закоммиченной фикстуре
    # это не видно — она-то снята исправным прогоном. Ловит живой
    # замер оси 3.
    "prepared-the-control-probe-forgets-its-name",
    # Приписка ` OR true` к условию по application_name: подменный
    # исполнитель фильтрует сам, разбирая текст запроса, и приписки не
    # видит. Ловится живой проверкой с двумя именами приложения.
    "pg-connection-filter-disabled-by-a-tautology",
    # Запасное значение STAND_POOL ВНУТРИ сервиса: строка throw остаётся
    # на месте, просто до неё не доходит, и текстовая проверка этого не
    # видит. Ловит живой замер отказа при запуске (startup_refusals):
    # вариант без ручки обязан завершиться ненулевым кодом и не открыть
    # порта. Проверено прогоном: без докера зелёная, с докером красная.
    "pool-size-defaults-inside-the-service",
    # Условие замера, исчезнувшее из СТРОКИ, по закоммиченной фикстуре не
    # видно: она-то поле несёт. Ловится свежим замером оси 1.
    "the-axis-one-row-forgets-its-pool",
    # Чем проба собственного пула СОБРАЛАСЬ, видно только если её
    # собрать: команда сборки живёт в python-файле, и подмена в ней
    # закоммиченную фикстуру не трогает.
    "the-framework-probe-links-the-axis-client",
}


def _copy_stand(work):
    """Копия стенда ВМЕСТЕ с родовым модулем `cpp/_common`.

    ПОЧЕМУ НЕ ОДИН КАТАЛОГ СТЕНДА, КАК БЫЛО. Родовая часть контракта
    строки переехала на уровень выше (`cpp/_common`), и
    `scripts/__init__.py` кладёт в sys.path каталог, СОДЕРЖАЩИЙ `cpp/`. В
    копии одного стенда такого каталога нет, и набор проверок валился
    `ModuleNotFoundError: No module named 'cpp'` — ОДИНАКОВО у каждой
    диверсии, независимо от того, что она портит. Диверсия при этом
    отчитывалась как сработавшая: набор ведь покраснел. Красный не по
    делу неотличим от красного по делу, если смотреть только на код
    возврата, — поэтому копия теперь воспроизводит расположение стенда в
    репозитории, а `run_all` держит КОНТРОЛЬНУЮ копию без диверсий.
    """
    ignore = shutil.ignore_patterns("__pycache__")
    inside = os.path.join(work, "cpp")
    stand = os.path.join(inside, os.path.basename(STAND))
    shutil.copytree(STAND, stand, ignore=ignore)
    shutil.copytree(os.path.join(CPP_ROOT, "_common"),
                    os.path.join(inside, "_common"), ignore=ignore)
    shutil.copy2(os.path.join(CPP_ROOT, "__init__.py"),
                 os.path.join(inside, "__init__.py"))
    return stand


def _one(root, item, with_docker=False):
    """Применяет одну диверсию к своей копии и возвращает строки отчёта."""
    name, relpath, anchor, replacement = item
    work = _copy_stand(os.path.join(root, name))
    path = os.path.join(work, relpath)
    with open(path, encoding="utf-8") as handle:
        text = handle.read()
    if text.count(anchor) != 1:
        # Не применившаяся диверсия — проваленная проверка: набор остался
        # зелёным, но не потому, что защита сработала.
        return False, ["НЕ ПРИМЕНИЛАСЬ (якорь не найден или не один): %s"
                       % name]
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(text.replace(anchor, replacement, 1))
    code, dropped = _run_checks(work, with_docker=with_docker)
    if code == 0:
        return False, ["ПРОШЛА ЗЕЛЁНОЙ (дефект проверок): %s" % name]
    lines = ["уронила %d: %s" % (len(dropped), name)]
    lines += ["    " + line.split(" ")[1] for line in dropped[:6]]
    return True, lines


def run_all(only=(), with_docker=False):
    """Прогоняет диверсии; `only` — имена, если нужен не весь набор.

    ПОЧЕМУ ЧАСТИЧНЫЙ ПРОГОН ЕСТЬ, НО НЕ ЗАМЕНЯЕТ ПОЛНЫЙ. Чинить одну дыру,
    гоняя все, — способ чинить её раз в час. Но сдаётся работа по ПОЛНОМУ
    прогону: диверсия, проверенная в одиночку, ничего не говорит про
    остальные.

    ДВА ПРОХОДА, И ЧИСЛО БЕЗ ОБЛАСТИ ВИДИМОСТИ НИЧЕГО НЕ ЗНАЧИТ. Штатный
    проход идёт БЕЗ докера, и живые проверки в нём пропускаются: «столько-то
    из стольких-то» в нём — утверждение про докер-НЕЗАВИСИМУЮ часть набора.
    Ревью волны B показало это замером: три диверсии проходили зелёными
    без докера и краснели с ним, то есть выглядели дырой в проверках, не
    будучи ею. Второй проход (`--with-docker`) обязателен для всего, что
    держится на живых проверках, и отчёт обязан называть, ЧТО именно
    гонялось в каком проходе.

    Проход с докером идёт ПО ОДНОЙ копии за раз: теги образов вариантов и
    имена контейнеров глобальны для демона, и две копии, собирающие
    `cpp-service-and-postgres:beast` разом, писали бы один тег.
    """
    failures = 0
    deferred = []
    chosen = [item for item in SABOTAGES if not only or item[0] in only]
    if only:
        missing = sorted(set(only) - {item[0] for item in chosen})
        if missing:
            print("нет таких диверсий: %s" % ", ".join(missing))
            return 2
    root = tempfile.mkdtemp(prefix="sabotage-stand-")
    try:
        # КОНТРОЛЬНАЯ КОПИЯ: та же копия, но БЕЗ диверсии. Она обязана
        # быть зелёной. Красная контрольная копия означает, что красными
        # будут и все остальные — по причине, к диверсиям отношения не
        # имеющей, — и тогда весь прогон не доказывает ничего. Ровно это
        # и случилось, когда родовой модуль переехал в cpp/_common.
        control_code, control_dropped = _run_checks(
            _copy_stand(os.path.join(root, "__control__")),
            with_docker=with_docker)
        if control_code != 0:
            print("КОНТРОЛЬНАЯ КОПИЯ КРАСНАЯ — прогон диверсий не значил бы "
                  "ничего:")
            for line in control_dropped[:10]:
                print("    " + line)
            return 2
        deferred = [item[0] for item in chosen
                    if not with_docker and item[0] in DOCKER_ONLY]
        running = [item for item in chosen if item[0] not in deferred]
        lanes = 1 if with_docker else LANES
        with ThreadPoolExecutor(max_workers=lanes) as pool:
            results = list(pool.map(
                lambda item: _one(root, item, with_docker=with_docker),
                running))
        for dropped, lines in results:
            if not dropped:
                failures += 1
            for line in lines:
                print(line)
        for name in deferred:
            print("ОТЛОЖЕНА ДО ПРОХОДА С ДОКЕРОМ (ловится только живой "
                  "проверкой): %s" % name)
    finally:
        shutil.rmtree(root, ignore_errors=True)
    print("---")
    print("диверсий: %d, из них прогнано: %d, не сработавших: %d "
          "(прогон %s)"
          % (len(chosen), len(chosen) - len(deferred), failures,
             "С ДОКЕРОМ" if with_docker else "без докера"))
    if deferred:
        print("отложено до прохода с докером: %d — без него эти диверсии "
              "прошли бы зелёными не по делу" % len(deferred))
    return 1 if failures else 0


def main(argv):
    force_utf8()
    command = argv[1] if len(argv) > 1 else "run"
    if command == "list":
        for name, relpath, _, _ in SABOTAGES:
            print("%-52s %s" % (name, relpath))
        return 0
    if command == "run":
        rest = [item for item in argv[2:] if item != "--with-docker"]
        return run_all(only=tuple(rest),
                       with_docker="--with-docker" in argv[2:])
    print("неизвестная команда: %s" % command)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
