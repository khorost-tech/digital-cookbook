"""Ось 3: параметризован ли запрос НА САМОМ ДЕЛЕ — и как это видно снаружи.

ВОПРОС ПЕРЕПИСАН, И ЭТО ЧАСТЬ ОСИ. Задумывалась она как счёт строк в
`pg_prepared_statements` на соединение. Так спросить нельзя ни с одной
стороны, и обе стороны ЗАМЕРЕНЫ (см. `scripts/pg.py`, функция
`prepared`): снаружи это представление СЕССИИ, без `pid` и без
`application_name`, и отдельная сессия видит там ноль, пока другая держит
подготовленный запрос; изнутри сервис шлёт запрос через `exec_params`,
то есть расширенным протоколом без именованного `PREPARE`, и строк там
не создаёт вовсе. Отрицательный контроль на таком счёте прошёл бы
одинаково у обоих плеч — подтверждая устройство ВОПРОСА, а не устройство
драйвера.

ЧТО ЧИТАЕТСЯ ВМЕСТО ЭТОГО. Два разных наблюдения, и они не
взаимозаменяемы:

* ТЕКСТ ЖИВОГО ЗАПРОСА из `pg_stat_activity`. Пока запрос выполняется,
  его текст виден целиком: у параметризованного вызова на месте величины
  стоит `$1`, у литерального — сама величина. Это ЕДИНСТВЕННЫЙ признак
  оси, различающий плечи;
* ЧИСЛО ВЫЗОВОВ из `pg_stat_statements`, по точному вхождению подстроки
  (`position`, не `LIKE`: в `LIKE` подчёркивание — метасимвол, и замер
  задачи 3 дал на этом сумму 2 вместо 1). Число вызовов говорит, что
  запросы ДОШЛИ до базы, и не говорит, каким был их текст: представление
  хранит текст НОРМАЛИЗОВАННЫМ, и `SELECT pg_sleep(1.500)` лежит там как
  `SELECT pg_sleep($1)` — ровно как параметризованный. Признак плеча из
  него не читается, и попытка прочитать была бы тавтологией.

ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ — ОТДЕЛЬНАЯ ПРОБА, А НЕ ЧЕТВЁРТЫЙ ВАРИАНТ. Три
штатных варианта параметризованы: так требуют оси 1, 2 и 4, и трогать это
нельзя. Первая редакция этой задачи завела четвёртое ПЛЕЧО СЕРВИСА с
переменной режима — и уронила чужие оси: текст запроса живёт в общей
логике (`service/main.cpp`), одной на три слоя, и ветка с литералом
изменила бинарник всех трёх. Его сумма записана в фикстурах осей 1, 2 и 4
и сверяется свежей сборкой — живая проверка `LiveDatabaseWait` покраснела
сразу, а переснятие тех замеров сдвинуло опубликованные числа оси 1
(живость drogon 1 → 2). Замерено, а не предположено; правка отозвана.

Поэтому контроль устроен как проба (`service/literal_sql_probe.cpp`) —
ровно по образцу пробы собственного пула Drogon: отдельный бинарник,
собираемый ВНУТРИ образа варианта тем же компилятором и тем же libpqxx,
сервиса не касающийся вовсе. Он шлёт ТОТ ЖЕ запрос с ВКЛЕЕННОЙ величиной.

ЧТО КОНТРОЛЬ ДОКАЗЫВАЕТ: чтение из `pg_stat_activity` различает параметр
и вклеенную величину — у того же клиента базы, в том же образе, на той же
базе. ЧЕГО НЕ ДОКАЗЫВАЕТ: что сервис МОГ БЫ прислать литерал. Это разные
утверждения, и второго стенд не делает.

ГДЕ ЗДЕСЬ ЛОВУШКА ПУСТЫХ ДАННЫХ. «У литерального плеча параметризованных
вызовов ноль» проходит и тогда, когда не снято НИ ОДНОГО текста.
Поэтому `verdict` требует наблюдений отдельным условием
(`every_arm_was_seen_at_work`), и без них ось недействительна.

НИ СЛОВА О СКОРОСТИ. Стенд не меряет ни пропускную способность, ни
задержку; ось 3 не исключение.
"""

import io
import json
import os
import re
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "prepared.jsonl")

# Клиент базы, общий для всех плеч оси. Строка, снятая другим клиентом,
# сравнивала бы драйверы, а не тексты запросов.
DRIVER = "libpqxx"

# Контрольная проба и образ, в котором она собирается. Имя — это ещё и
# `application_name` в базе: без него участники видели бы чужие строки.
# Оно НЕ равно имени варианта намеренно: проба и сервис — разные
# участники, и общее имя склеило бы их строки в один счёт.
CONTROL = "literal-sql-probe"
CONTROL_IMAGE = "beast"

# Режимы текста запроса, ровно те, что понимает сервис.
PARAM_MODE = "param"
LITERAL_MODE = "literal"

# Подстрока, по которой считаются вызовы. Содержит подчёркивание — то
# самое, из-за которого читать приходится `position`, а не `LIKE`.
FRAGMENT = "pg_sleep"

# Настройки замера. Пул равен числу запросов намеренно: ожидание слота
# заняло бы обработчик, часть запросов не попала бы в базу одновременно,
# и текстов снималось бы меньше. Ожидание — только время, в течение
# которого запрос ЖИВОЙ и его текст виден; оно здесь не меряется ничем.
REQUESTS = 4
WORKERS = 4
POOL = 4
SLEEP_MS = 1500
POLL_S = 0.05
BARRIER_TIMEOUT_S = 5.0

# Параметр в тексте запроса. Ищется именно `$1`: другого параметра у
# этого запроса нет, и общее «есть доллар» поймало бы заодно чужие
# строки.
PARAM_MARKER = "$1"
# Величина, вклеенная в текст: `pg_sleep(` и сразу цифра.
LITERAL_RE = re.compile(r"pg_sleep\(\s*[0-9]")


def classify(text):
    """Чем является текст живого запроса: параметром, литералом или ничем.

    ПРИНИМАЕТ ТОЛЬКО ТЕКСТ, И ЭТО ГЛАВНОЕ В ЭТОЙ ФУНКЦИИ. Разбор,
    которому доступен объявленный режим плеча, подтверждал бы себя
    всегда: плечо, до которого переменная режима не доехала, объявлено
    литеральным и работает параметром — и тавтологический разбор назвал
    бы его литеральным. Признак читается из того, что видно В БАЗЕ.

    `other` — не «прочее по мелочи», а честный отказ засчитать строку:
    сюда попадает и чужой запрос, и текст, в котором есть И параметр, И
    вклеенная величина. Последний ни о чём не говорит, и молча
    засчитать его любому плечу значило бы придумать наблюдение.
    """
    if FRAGMENT not in text:
        return "other"
    has_param = PARAM_MARKER in text
    has_literal = bool(LITERAL_RE.search(text))
    if has_param and not has_literal:
        return "parameterized"
    if has_literal and not has_param:
        return "literal"
    return "other"


# --- замер ---------------------------------------------------------------


PROBE_SOURCE = os.path.join(STAND, "service", "literal_sql_probe.cpp")

# Сборка и запуск пробы ВНУТРИ образа варианта, одной командой. Исходник
# подаётся на вход процессу, а не кладётся в образ: образы трёх вариантов
# обязаны оставаться построчно симметричными, и лишний шаг сборки у
# одного из них завёл бы настоящую асимметрию ради контроля.
#
# ЧЕМ ПРОБА СОБРАЛАСЬ — СНИМАЕТСЯ С БИНАРНИКА (`ldd`), а не объявляется:
# «тот же клиент базы» обязан быть наблюдением. Ровно на этом ревью оси 2
# поймало пробу собственного пула: ярлык в коде остался прежним, а
# линковка уехала.
BUILD_AND_RUN = (
    "cat > /tmp/probe.cpp && "
    "g++ -std=c++20 -o /tmp/probe /tmp/probe.cpp "
    "$(pkg-config --cflags --libs libpqxx) && "
    "echo '---ldd---' && ldd /tmp/probe && echo '---run---' && /tmp/probe")


def build_control_arm(host=None):
    """Готовит ОБРАЗ, внутри которого собирается контрольная проба.

    Своего образа у пробы нет и быть не должно: она обязана собираться
    тем же компилятором и тем же libpqxx, что и вариант, иначе «тот же
    клиент базы» стало бы словами.
    """
    return dockerlib.build_variant(CONTROL_IMAGE, host=host)


def _probe_source():
    with io.open(PROBE_SOURCE, encoding="utf-8") as handle:
        return handle.read()


def measure_control(database, requests=REQUESTS, sleep_ms=SLEEP_MS,
                    host=None):
    """Строка отрицательного контроля: та же база, тот же клиент, литерал.

    Тексты снимаются ТЕМ ЖЕ способом, что у вариантов: чтением
    `pg_stat_activity` по имени приложения, пока запросы живы. Разбор
    текста — тот же `classify`, и ему всё равно, кто прислал строку.
    """
    samples = []
    done = {}
    calls_before = database.statement_calls(FRAGMENT)
    command = [
        "docker", "run", "--rm", "-i", "--network", pg.NETWORK,
        "--entrypoint", "",
        "-e", "STAND_DSN=%s" % pg.dsn(),
        "-e", "STAND_APPLICATION_NAME=%s" % CONTROL,
        "-e", "STAND_SLEEP_MS=%d" % sleep_ms,
        "-e", "STAND_REQUESTS=%d" % requests,
        dockerlib.image_tag(CONTROL_IMAGE), "sh", "-c", BUILD_AND_RUN,
    ]

    def run():
        done["proc"] = subprocess.run(command, input=_probe_source(),
                                      capture_output=True, text=True)

    worker = threading.Thread(target=run)
    worker.start()
    # Счёт идёт, ПОКА проба жива: сборка внутри образа занимает секунды, и
    # выборки до первого запроса дадут пустоту — она не мешает, потому что
    # тексты складываются, а не усредняются.
    while worker.is_alive():
        samples.extend(row["query"] for row in database.active_queries(CONTROL))
        time.sleep(POLL_S)
    worker.join()
    proc = done["proc"]
    output = (proc.stdout or "") + (proc.stderr or "")
    linked = sorted(line.strip().split(" ")[0]
                    for line in output.splitlines()
                    if "=>" in line and line.strip().startswith("lib"))
    calls_after = database.statement_calls(FRAGMENT)
    row = _counted(samples)
    row.update({
        "kind": "prepared",
        "variant": CONTROL,
        "sql_mode": LITERAL_MODE,
        "driver": DRIVER,
        "is_the_control": True,
        "built_in_image": CONTROL_IMAGE,
        "requests": requests,
        "served": requests if proc.returncode == 0 else 0,
        "not_served": 0 if proc.returncode == 0 else requests,
        "sleep_ms": sleep_ms,
        "calls_fragment": FRAGMENT,
        "calls_before": calls_before,
        "calls_after": calls_after,
        "calls_delta": calls_after - calls_before,
        "probe_links": linked,
        "links_libpqxx": any(name.startswith("libpqxx") for name in linked),
        "probe_exit_code": proc.returncode,
        "probe_finished": "PROBE DONE" in (proc.stdout or ""),
        "probe_output": output.strip()[-2000:],
    })
    return row


def _counted(samples):
    """Счёт текстов по разбору — общий для варианта и для контроля.

    ОДИН И ТОТ ЖЕ разбор на обоих: счёт, устроенный у контроля иначе,
    сравнивал бы два способа читать, а не два текста.
    """
    counts = {"parameterized": 0, "literal": 0, "other": 0}
    for text in samples:
        counts[classify(text)] += 1
    return {
        "texts_sampled": len(samples),
        "distinct_texts": sorted(set(samples)),
        "parameterized_texts": counts["parameterized"],
        "literal_texts": counts["literal"],
        "other_texts": counts["other"],
    }


def _one_request(port, sleep_ms, host=None):
    """Один запрос /q: исход, без исключений наружу.

    Отказ — НАБЛЮДЕНИЕ, и он обязан попасть в строку, а не уронить
    замер: «текстов не снято, потому что запросы не дошли» отличается от
    «текстов не снято, потому что мы не успели».
    """
    try:
        status, body = dockerlib._host(host).get(
            "http://127.0.0.1:%d/q?sleep_ms=%d" % (port, sleep_ms),
            timeout=120)
    except Exception as failure:  # OSError, HTTPException и прочее
        return {"served": False, "detail": str(failure)}
    if status != 200:
        return {"served": False, "detail": "status %s: %s" % (status, body)}
    row = dockerlib.parse_reply(body, least=1)
    return {"served": row.get("outcome") == "served",
            "detail": row.get("detail", "")}


def measure(name, database, requests=REQUESTS, workers=WORKERS,
            pool=POOL, sleep_ms=SLEEP_MS, host=None):
    """Строка фикстуры для одного варианта.

    Пока запросы в полёте, отдельный поток читает `pg_stat_activity` по
    имени плеча и складывает ТЕКСТЫ живых запросов. Число вызовов
    снимается ДО и ПОСЛЕ: представление `pg_stat_statements` по плечам не
    разделяется вовсе (у него нет ни `pid`, ни имени приложения), и
    прирост привязан к плечу только тем, что плечи меряются по очереди и
    в базу в это время не ходит никто другой. Это условие замера, и оно
    записано в expected.json, а не выдано за свойство представления.

    База должна быть уже поднята (`scripts.pg.start` со статистикой):
    поднимать её на каждое плечо значило бы мерить каждое в своей базе.
    """
    samples = []
    stop = threading.Event()

    def watch():
        while not stop.is_set():
            samples.extend(row["query"]
                           for row in database.active_queries(name))
            time.sleep(POLL_S)

    calls_before = database.statement_calls(FRAGMENT)
    # РУЧКИ ТЕКСТА ЗАПРОСА У СЕРВИСА НЕТ, и это существенно. Вариант шлёт
    # то, что написано в его исходнике, — ничего, что можно было бы
    # переключить замером. Поэтому `sql_mode` в строке ниже — не
    # настройка, а ярлык происхождения текста, и ни одно решение разбора
    # на него не опирается: признак читается из базы.
    env = {"STAND_DSN": pg.dsn()}
    with dockerlib.run_variant(name, workers=workers, pool=pool, env=env,
                               network=pg.NETWORK, host=host) as service:
        if not service["ready"]:
            raise RuntimeError("плечо %s не поднялось" % name)
        watcher = threading.Thread(target=watch)
        watcher.start()
        gate = threading.Barrier(requests, timeout=BARRIER_TIMEOUT_S)

        def one(_):
            gate.wait()
            return _one_request(service["port"], sleep_ms, host=host)

        try:
            with ThreadPoolExecutor(max_workers=requests) as clients:
                replies = list(clients.map(one, range(requests)))
        finally:
            stop.set()
            watcher.join(timeout=10.0)
    calls_after = database.statement_calls(FRAGMENT)
    row = _counted(samples)
    row.update({
        "kind": "prepared",
        "variant": name,
        "sql_mode": PARAM_MODE,
        "driver": DRIVER,
        "requests": requests,
        "served": sum(1 for item in replies if item["served"]),
        "not_served": sum(1 for item in replies if not item["served"]),
        "workers": workers,
        "pool_size": pool,
        "sleep_ms": sleep_ms,
        "calls_fragment": FRAGMENT,
        "calls_before": calls_before,
        "calls_after": calls_after,
        "calls_delta": calls_after - calls_before,
        "entry_binary_sha256": dockerlib.entry_binary(name,
                                                      host=host)["sha256"],
        "is_the_control": False,
        "replies": replies,
    })
    return row


# --- разбор --------------------------------------------------------------


def read(path=FIXTURE, kind="prepared"):
    """Строки замера из фикстуры — только нужного вида."""
    rows = []
    with io.open(path, encoding="utf-8") as handle:
        for line in handle:
            if not line.strip().startswith("{"):
                continue
            row = json.loads(line)
            if row.get("kind") == kind:
                rows.append(row)
    return rows


def row_for(rows, variant):
    """Строка плеча — или громкий отказ.

    Отсутствующее плечо НЕ подменяется соседним и не превращается в ноль:
    контроль, у которого не хватает половины пары, не контроль.

    ПОСЛЕДНЯЯ СТРОКА ПОБЕЖДАЕТ — то же правило, что у оси 2: диверсия над
    фикстурой ДОПИСЫВАЕТ испорченную строку, и якорем иначе пришлось бы
    брать кусок самого замера, протухающий с каждым перезамером.
    """
    for row in reversed(rows):
        if row["variant"] == variant:
            return row
    raise KeyError("нет замера плеча %s: сравнивать нечего" % variant)


def parameterized(rows, variant):
    """Сколько раз у плеча видели ПАРАМЕТРИЗОВАННЫЙ текст."""
    return row_for(rows, variant)["parameterized_texts"]


def literal(rows, variant):
    """Сколько раз у плеча видели текст с ВКЛЕЕННОЙ величиной."""
    return row_for(rows, variant)["literal_texts"]


def calls(rows, variant):
    """Прирост числа вызовов в базе за замер плеча."""
    return row_for(rows, variant)["calls_delta"]


def param_arms(rows):
    """Варианты оси без контрольной пробы — в порядке имён."""
    return sorted({row["variant"] for row in rows} - {CONTROL})


def verdict(rows, control=CONTROL):
    """Печатается ли ось 3 — и почему, если нет.

    Условия перечислены по отдельности намеренно: «непечатно» без причины
    неотличимо от поломки разбора, а причина у каждого своя и чинится
    по-разному.
    """
    arms = param_arms(rows)
    if not arms:
        raise ValueError("в замере нет ни одного штатного плеча")
    control_row = row_for(rows, control)  # отсутствие — KeyError, и это верно
    by_variant = {}
    for name in arms + [control]:
        row = row_for(rows, name)
        by_variant[name] = {
            "sql_mode": row["sql_mode"],
            "texts_sampled": row["texts_sampled"],
            "parameterized": row["parameterized_texts"],
            "literal": row["literal_texts"],
            "other": row["other_texts"],
            "distinct_texts": row["distinct_texts"],
            "calls_delta": row["calls_delta"],
            "requests": row["requests"],
        }
    # Контроль: у литерального плеча НИ ОДНОГО параметризованного текста —
    # и при этом тексты вообще снимались (см. ниже, условие про данные).
    control_holds = (control_row["parameterized_texts"] == 0
                     and control_row["literal_texts"] > 0)
    arms_hold = all(row_for(rows, name)["literal_texts"] == 0
                    and row_for(rows, name)["parameterized_texts"] > 0
                    for name in arms)
    # НОЛЬ НАБЛЮДЕНИЙ — НЕ НОЛЬ ПАРАМЕТРОВ. Без этого условия контроль
    # держится на пустых данных и подтверждает что угодно.
    seen = all(row["texts_sampled"] > 0 for row in
               [row_for(rows, name) for name in arms + [control]])
    reached = all(row_for(rows, name)["calls_delta"] > 0
                  for name in arms + [control])
    # Расхождение числа вызовов с числом запросов — ОГОВОРКА, а не запрет:
    # оно не делает текст неверным, но запрещает называть прирост числом
    # вызовов плеча.
    calls_match = all(row_for(rows, name)["calls_delta"]
                      == row_for(rows, name)["requests"]
                      for name in arms + [control])
    # ТОТ ЖЕ КЛИЕНТ БАЗЫ — СНЯТЫЙ С БИНАРНИКА ПРОБЫ, а не объявленный.
    # Контроль, собравшийся другим клиентом, сравнивал бы драйверы, а не
    # тексты: ровно так ревью оси 2 поймало пробу собственного пула, где
    # ярлык в коде остался прежним, а линковка уехала.
    same_driver = (bool(control_row.get("links_libpqxx"))
                   and any(name.startswith("libpqxx")
                           for name in control_row.get("probe_links", [])))
    one_driver = len({row["driver"] for row in rows}) == 1
    all_served = all(row["not_served"] == 0 for row in rows)
    texts = {name: tuple(by_variant[name]["distinct_texts"]) for name in arms}
    layers_agree = len(set(texts.values())) == 1
    return {
        "control": control,
        "variants": arms,
        "by_variant": by_variant,
        "control_shows_only_literal": control_holds,
        "parameterized_arms_show_only_the_parameter": arms_hold,
        "every_arm_was_seen_at_work": seen,
        "every_arm_reached_the_database": reached,
        "calls_match_requests_everywhere": calls_match,
        "control_uses_the_same_database_client": same_driver,
        "one_driver_for_every_arm": one_driver,
        "all_requests_served": all_served,
        # Не условие печати, а наблюдение: слой может и не влиять на текст,
        # и тогда это говорится прямо.
        "layers_agree_on_the_text": layers_agree,
        "prepared_publishable": bool(
            control_holds and arms_hold and seen and reached and same_driver
            and one_driver and all_served),
    }


def table(rows, control=CONTROL):
    """Строки таблицы — ТОЛЬКО если контроль сошёлся.

    ПОЧЕМУ ЗАПРЕТ ЖИВЁТ ЗДЕСЬ, А НЕ В ПЕЧАТАЮЩЕМ СЦЕНАРИИ. Сценарий —
    самое удобное место обойти его, не тронув ни одной проверки:
    достаточно напечатать числа самому. Здесь запрет можно позвать на
    придуманных строках и потребовать отказа.
    """
    decision = verdict(rows, control=control)
    if not decision["prepared_publishable"]:
        raise ValueError(
            "ось 3 недействительна, таблица не печатается: %s"
            % json.dumps(decision, ensure_ascii=False, sort_keys=True))
    lines = ["участник            режим     текстов  с параметром  "
             "с величиной  вызовов в базе  текст запроса"]
    for name in decision["variants"] + [control]:
        item = decision["by_variant"][name]
        lines.append("%-19s %-9s %-8d %-13d %-12d %-15d %s"
                     % (name, item["sql_mode"], item["texts_sampled"],
                        item["parameterized"], item["literal"],
                        item["calls_delta"],
                        "; ".join(item["distinct_texts"])))
    return lines
