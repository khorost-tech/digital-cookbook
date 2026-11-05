"""Ось 2: сколько соединений драйвер открывает НА САМОМ ДЕЛЕ.

ВОПРОС. Слой объявляет размер пула; база знает, сколько соединений он
открыл. Это то же различие, на котором волна A развела объявленную версию
библиотеки и измеренную, и здесь оно устроено так же: число берётся из
`pg_stat_activity` по `application_name` варианта, а не из ответа сервиса
и не из настройки. Счётчик, взятый из настройки, подтвердил бы контроль
всегда и не сказал бы ничего.

КОНТРОЛЬ ПЕРВИЧЕН. Пара по размеру пула: пул на 1 против пула на 8, всё
остальное одинаково. Счётчик обязан пойти за настройкой. Не пошёл — мы
меряем не пул, ось недействительна, таблица не печатается,
`pool_publishable` ложно. Это законный исход, и он записывается.

ЧТО БЫЛО ДО ЭТОЙ ОСИ. Пула не было НИ У ОДНОГО из трёх слоёв: соединение
открывалось на каждый запрос и закрывалось вместе с ним — замерено пробой
до прогона (expected.json, блок pool). Соединений при этом было ровно
столько, сколько запросов было В ПОЛЁТЕ, а не сколько рабочих единиц: у
слоя, чей потолок ниже числа единиц, их меньше. Пул написан здесь же, в общей
логике сервиса, ОДИН на три слоя: пул средствами каждого слоя развёл бы
варианты ещё и клиентом базы, и ось сравнивала бы драйверы. Собственный
пул Drogon замерен ОТДЕЛЬНОЙ строкой (`kind: "drogon-own-pool"`) и в ось
не входит — почему, сказано там же.

ПОЧЕМУ ЗДЕСЬ СВОЙ ЗАЛП, А НЕ `dockerlib.volley`. Тот залп устроен под ось
1: он спрашивает маршрут живости, сводит два источника и считает
обработчиков в полёте. Второй источник в нём — число АКТИВНЫХ ЗАПРОСОВ, и
подать туда счёт соединений значило бы сравнивать разные величины под
именем «источники сошлись». Здесь предмет другой, и залп свой: послать N
одновременных запросов и считать СОЕДИНЕНИЯ, пока они идут и сразу после.
"""

import io
import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "pool.jsonl")

# Клиент базы, общий для трёх слоёв. Лежит в каждой строке оси и
# проверяется: строка, снятая другим клиентом, сравнивала бы драйверы, а
# не слои.
DRIVER = "libpqxx"

# Пара контроля. Восемь — не круглое число ради красоты: оно равно и
# числу одновременных запросов, и числу рабочих единиц, чтобы пул на 8
# мог быть насыщен. Пул, больший залпа, показал бы меньше слотов, чем
# имеет, и «счётчик не дошёл до восьми» читалось бы как свойство пула.
SIZES = (1, 8)
CONCURRENT = 8
WORKERS = 8

# Ожидание в базе на один запрос. Оно здесь не замеряется ни в каком
# виде: это только время, в течение которого соединения ЗАНЯТЫ и их
# можно успеть сосчитать. Секунда выбрана потому, что один опрос счётчика
# — это `docker exec psql`, то есть сотни миллисекунд: при коротком
# ожидании залп при пуле на 8 кончился бы раньше первой выборки.
SLEEP_MS = 1000
POLL_S = 0.05

# Сколько залпов на настройку. Утверждение «при пуле на 1 соединение
# было одно» тем честнее, чем больше у него было шансов не сойтись.
RUNS = 3

# Сколько барьер ждёт остальные пробы, прежде чем сдаться. Залп, ушедший
# вразнобой, не насытил бы пул, и число соединений говорило бы о клиенте.
BARRIER_TIMEOUT_S = 5.0


def sampler_for(name, host=None):
    """Замыкание счётчика соединений — ЕДИНСТВЕННЫЙ источник числа оси.

    Спрашивается `scripts.pg.connections`, то есть `pg_stat_activity` по
    имени приложения варианта. Отказ базы пролетает наружу и НЕ
    превращается в ноль: ноль здесь означал бы «вариант не подключён», а
    это другое наблюдение.
    """
    return lambda: pg.connections(name, host=host)


def observed(values):
    """Что печатается по набору залпов — НАИБОЛЬШЕЕ наблюдённое число.

    ПОЧЕМУ МАКСИМУМ, А НЕ СРЕДНЕЕ ИЛИ МИНИМУМ. Утверждение оси — «столько
    соединений драйвер открыл». Открыл — значит хоть раз; и обещание
    «при пуле на 1 соединение было одно» обязано выдерживать КАЖДЫЙ залп,
    а не лучший из них. Максимум и есть тот залп, на котором обещание
    ломается, если оно ложно; минимум отчитался бы об удаче, среднее
    спрятало бы выброс между двумя верными числами.

    Сравнивать максимумы двух настроек можно только при РАВНОМ числе
    залпов: больший максимум добывается бо́льшим числом попыток. Это
    проверяет `verdict`.
    """
    values = list(values)
    if not values:
        raise ValueError("печатать нечего: ни одного залпа")
    return max(values)


def _one_request(port, sleep_ms, host=None):
    """Один запрос /q: исход и прочитанная занятость, без исключений.

    Отказ здесь — НАБЛЮДЕНИЕ (база исчерпала соединения, слой не принял),
    и он обязан попасть в строку, а не уронить замер: «соединений мало,
    потому что запросы не дошли» — тоже ответ, и он отличается от «пул
    ограничил».
    """
    started = time.time()
    try:
        status, body = dockerlib._host(host).get(
            "http://127.0.0.1:%d/q?sleep_ms=%d" % (port, sleep_ms),
            timeout=120)
    except Exception as failure:  # OSError, HTTPException и прочее
        return {"served": False, "inflight": None, "detail": str(failure),
                "duration_ms": int((time.time() - started) * 1000)}
    reply = {"duration_ms": int((time.time() - started) * 1000)}
    if status != 200:
        reply.update({"served": False, "inflight": None,
                      "detail": "status %s: %s" % (status, body)})
        return reply
    row = dockerlib.parse_reply(body, least=1)
    reply.update({"served": row.get("outcome") == "served",
                  "inflight": row["inflight"],
                  "detail": row.get("detail", "")})
    return reply


def volley(port, sampler, concurrent=CONCURRENT, sleep_ms=SLEEP_MS,
           host=None):
    """Один залп: N одновременных /q и счёт СОЕДИНЕНИЙ, пока они идут.

    ЧИСЛО СНИМАЕТСЯ И ВО ВРЕМЯ ЗАЛПА, И СРАЗУ ПОСЛЕ. Опрос счётчика — это
    `docker exec psql`, сотни миллисекунд на вопрос: при пуле на 8 залп
    кончается за одно ожидание, и выборок успевает быть мало. Показание
    ПОСЛЕ залпа от этой спешки не зависит вовсе — и оно же отвечает на
    вопрос, ради которого пул и заводился: соединение переживает запрос
    или закрывается вместе с ним. До этой оси оно читалось нулём.

    Барьер отпускает залп одной точкой. Залп, ушедший вразнобой, не
    насытил бы пул, и число соединений говорило бы о клиенте, а не о
    наборе слотов.
    """
    samples = []
    stop = threading.Event()

    def watch():
        while not stop.is_set():
            samples.append(sampler())
            time.sleep(POLL_S)

    gate = threading.Barrier(concurrent, timeout=BARRIER_TIMEOUT_S)
    watcher = threading.Thread(target=watch)
    watcher.start()

    def one(_):
        gate.wait()
        return _one_request(port, sleep_ms, host=host)

    try:
        with ThreadPoolExecutor(max_workers=concurrent) as clients:
            replies = list(clients.map(one, range(concurrent)))
    finally:
        stop.set()
        watcher.join(timeout=10.0)
    after = sampler()
    inflight = [item["inflight"] for item in replies
                if item["inflight"] is not None]
    return {
        "concurrent": concurrent,
        "sleep_ms": sleep_ms,
        "samples": samples,
        "connections_while_busy": max(samples) if samples else None,
        "connections_after": after,
        # То самое число оси: наибольшее, увиденное В БАЗЕ за залп,
        # считая показание сразу после. Ни из настройки, ни из ответа
        # сервиса сюда не попадает ничего.
        "connections": max(samples + [after]),
        "inflight_peak": max(inflight) if inflight else None,
        "served": sum(1 for item in replies if item["served"]),
        "not_served": sum(1 for item in replies if not item["served"]),
        "replies": replies,
    }


def measure(name, pool_size, runs=RUNS, concurrent=CONCURRENT,
            workers=WORKERS, sleep_ms=SLEEP_MS, host=None):
    """Строка фикстуры для одной настройки одного варианта.

    Контейнер поднимается ОДИН на все залпы настройки: пул тем и пул, что
    переживает запрос, и перезапуск между залпами стирал бы ровно то, что
    ось меряет. Заодно становится видна утечка: слот, не вернувшийся при
    отказе, копился бы от залпа к залпу.

    База должна быть уже поднята (`scripts.pg.start`): поднимать её на
    каждый вариант значило бы мерить каждый вариант в своей базе.
    """
    sampler = sampler_for(name, host=host)
    with dockerlib.run_variant(name, workers=workers, pool=pool_size,
                               env={"STAND_DSN": pg.dsn()},
                               network=pg.NETWORK, host=host) as service:
        if not service["ready"]:
            raise RuntimeError("%s (пул %d) не поднялся" % (name, pool_size))
        # ДО ПЕРВОГО ЗАПРОСА соединений быть не должно: пул заводится
        # лениво, и ненулевое число здесь означало бы, что мы считаем
        # чужие соединения — например, остаток прошлого замера.
        before = sampler()
        volleys = [volley(service["port"], sampler, concurrent=concurrent,
                          sleep_ms=sleep_ms, host=host)
                   for _ in range(runs)]
    by_run = [item["connections"] for item in volleys]
    idle_after = [item["connections_after"] for item in volleys]
    inflight = [item["inflight_peak"] for item in volleys
                if item["inflight_peak"] is not None]
    return {
        "kind": "pool",
        "variant": name,
        "driver": DRIVER,
        "pool_size": pool_size,
        "concurrent": concurrent,
        "workers": workers,
        "sleep_ms": sleep_ms,
        "runs": len(volleys),
        "connections_before": before,
        "connections_by_run": by_run,
        "connections_observed": observed(by_run),
        # УСТОЙЧИВО ЛИ ЧИСЛО ВНУТРИ СЕССИИ. У оси 1 для этого есть
        # capacity_stable, и по той же причине: число, скачущее от залпа
        # к залпу, величиной не является, и выводить из него нечего.
        # ВНУТРИСЕССИОННАЯ устойчивость при этом НЕ равна
        # воспроизводимости между сессиями — у drogon ревьюер получил
        # при пуле 8 пять соединений там, где здесь шесть, причём у
        # каждого внутри своей сессии число не менялось. Поэтому поле
        # называется по залпам, а оговорка про сессии живёт в README.
        "connections_stable": min(by_run) == max(by_run),
        "connections_idle_after_by_run": idle_after,
        "connections_idle_after": max(idle_after),
        # ЗАНЯТОСТЬ ПРИ ЭТОЙ НАСТРОЙКЕ. Ожидание свободного слота
        # занимает рабочую единицу так же, как ожидание в базе (ось 1).
        # Это наблюдение, а не дефект, и оно записывается числом: при
        # пуле на 1 в полёте оказываются все, кто стоит в очереди.
        "inflight_peak": max(inflight) if inflight else None,
        "served": sum(item["served"] for item in volleys),
        "not_served": sum(item["not_served"] for item in volleys),
        "entry_binary_sha256": dockerlib.entry_binary(name,
                                                      host=host)["sha256"],
        "volleys": volleys,
    }


# --- разбор -------------------------------------------------------------


def read(path=FIXTURE, kind="pool"):
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


def reading(row):
    """Во что упёрлось число соединений ЭТОЙ строки — и упёрлось ли.

    ЗАЧЕМ ЭТО ОТДЕЛЬНОЙ ФУНКЦИЕЙ. Обе содержательные находки оси 2
    держатся на сравнении двух полей строки, и, пока сравнение живёт
    только в README, опровергнуть находку можно прямо в фикстуре, ничего
    не уронив: ревью так и сделало — три подмены («числа при пуле 8
    подтянуты к восьмёрке», «соединений семь, а в полёте восемь»,
    «при пуле на 1 занятость единица») прошли зелёными, хотя каждая
    прямо противоречит печатаемому выводу.

    `saturated_the_pool` — слотов не осталось: соединений столько же,
    сколько слотов. `below_pool_at_inflight_peak` — пул до потолка не дошёл:
    соединений столько же, сколько обработчиков бывало в полёте разом, и
    это меньше числа слотов. ПРИЧИНУ это поле не называет. Почему при
    восьми одновременных запросах разом в полёте оказалось меньше
    восьми, стенд не установил: предел слоя, распределение соединений
    между потоками и недогруженная проба дают одно и то же равенство, а
    контроля насыщения со стороны клиента нет. `queue_occupies_units` — в полёте больше, чем соединений:
    те, кто стоит в очереди за слотом, рабочую единицу удерживают.

    `more_connections_than_requests` — НЕВОЗМОЖНОЕ состояние, и оно
    здесь не для красоты. Пул открывает новое соединение только тогда,
    когда все уже открытые выданы; возвращённое соединение не
    закрывается, а ждёт следующего обработчика среди простаивающих. Значит
    открытых разом не бывает больше, чем обработчиков, державших их
    разом, а тех не бывало больше `inflight_peak` (это ПИК за всю жизнь
    контейнера, а залпы одинаковы). Строка, где соединений больше, — это подогнанное число,
    а не наблюдение.
    """
    observed = row["connections_observed"]
    inflight = row["inflight_peak"]
    size = row["pool_size"]
    return {
        "saturated_the_pool": observed == size,
        "below_pool_at_inflight_peak": (inflight is not None
                             and observed == inflight < size),
        "queue_occupies_units": (inflight is not None and inflight > observed),
        "more_connections_than_requests": (inflight is not None
                                           and observed > inflight),
    }


def setting(rows, variant, pool_size):
    """Строка варианта при названном размере пула — или громкий отказ.

    Отсутствующая настройка НЕ подменяется соседней и не превращается в
    ноль: контроль, у которого не хватает половины пары, не контроль.

    ПОЧЕМУ ПОСЛЕДНЯЯ СТРОКА, А НЕ ПЕРВАЯ. Фикстура пишется одним прогоном
    и дубликатов не содержит. Но диверсия над фикстурой ДОПИСЫВАЕТ
    испорченную строку: якорем иначе пришлось бы брать кусок самого
    замера, а числа в нём меняются с каждым перезамером, и диверсия
    протухала бы молча — «не применилась» читалось бы как «проверки
    держат». Последняя строка побеждает — то же правило, по которому
    разбор оси 1 кладёт строки в отображение по имени варианта.
    """
    for row in reversed(rows):
        if row["variant"] == variant and row["pool_size"] == pool_size:
            return row
    raise KeyError(
        "нет замера варианта %s при пуле %r: сравнивать нечего"
        % (variant, pool_size))


def connections(rows, variant, pool_size):
    """Сколько соединений открыл вариант при названном размере пула."""
    return setting(rows, variant, pool_size)["connections_observed"]


def control(rows, variant, sizes=SIZES):
    """Контроль ОДНОГО варианта: пошёл ли счётчик за размером пула.

    Два условия, и они разные. `knob_moves_counter` — счётчик вырос
    вместе с настройкой; он держится и при 2 против 8, то есть сам по
    себе не доказывает, что пул на 1 хоть что-то ограничивает.
    `low_within_pool` — при пуле на 1 соединение не больше одного; без
    него потолок объявлен, но не проверен.

    ПАРА ОБЯЗАНА РАЗЛИЧАТЬСЯ ТОЛЬКО РАЗМЕРОМ ПУЛА. Настройка, поехавшая
    заодно (число одновременных запросов, число рабочих единиц, длина
    ожидания, бинарник), делает из контроля сравнение двух разных
    замеров, и разницу объясняли бы две причины сразу.
    """
    low_size, high_size = min(sizes), max(sizes)
    low = setting(rows, variant, low_size)
    high = setting(rows, variant, high_size)
    for field in ("concurrent", "workers", "sleep_ms", "runs", "driver",
                  "entry_binary_sha256"):
        if low[field] != high[field]:
            raise ValueError(
                "%s: пара по размеру пула различается ещё и полем %s (%r "
                "против %r) — контролем это уже не является"
                % (variant, field, low[field], high[field]))
    if len(low["connections_by_run"]) != len(high["connections_by_run"]):
        raise ValueError(
            "%s: настройки сняты разным числом залпов (%d против %d): "
            "больший максимум добывается бо́льшим числом попыток, а не "
            "подключённой ручкой"
            % (variant, len(low["connections_by_run"]),
               len(high["connections_by_run"])))
    return {
        "low_pool": low_size,
        "high_pool": high_size,
        "low": low["connections_observed"],
        "high": high["connections_observed"],
        "knob_moves_counter": (low["connections_observed"]
                               < high["connections_observed"]),
        "low_within_pool": low["connections_observed"] <= low_size,
        "high_within_pool": high["connections_observed"] <= high_size,
        "inflight_at_low": low["inflight_peak"],
        "inflight_at_high": high["inflight_peak"],
    }


def verdict(rows, sizes=SIZES):
    """Печатается ли таблица оси 2 — и почему, если нет.

    Условия перечислены по отдельности намеренно: «непечатно» без
    причины неотличимо от поломки разбора, а причина у каждого своя и
    чинится по-разному.
    """
    variants = sorted({row["variant"] for row in rows})
    if not variants:
        raise ValueError("в замере нет ни одной строки оси 2")
    by_variant = {name: control(rows, name, sizes=sizes) for name in variants}
    no_leak = all(row["connections_idle_after"] <= row["pool_size"]
                  for row in rows)
    all_served = all(row["not_served"] == 0 for row in rows)
    # ПОЛЕ ОБЯЗАТЕЛЬНО, А НЕ «ЕСЛИ ЕСТЬ». Условие `if ... in row` делало
    # зелёной проверку на ОТСУТСТВУЮЩЕМ поле: замер, переставший его
    # писать, проходил бы как «до залпа было пусто». Это проверка
    # отсутствия на пустых данных — ровно тот способ, которым утверждение
    # держится ни на чём.
    missing = [row["variant"] for row in rows if "connections_before" not in row]
    if missing:
        raise KeyError(
            "в строках %s нет connections_before: «до залпа ничего не было "
            "открыто» проверялось бы на отсутствующем поле" % sorted(missing))
    nothing_before = all(row["connections_before"] == 0 for row in rows)
    one_driver = len({row["driver"] for row in rows}) == 1
    # Во что упёрлось число у каждой строки — и не солгала ли строка.
    readings = {(row["variant"], row["pool_size"]): reading(row)
                for row in rows}
    impossible = sorted(key for key, item in readings.items()
                        if item["more_connections_than_requests"])
    stable = all(row.get("connections_stable", False) for row in rows)
    return {
        "variants": variants,
        "by_variant": by_variant,
        # Контроль обязан сойтись У КАЖДОГО варианта: таблица печатается
        # целиком, и «у двух сошлось» напечатало бы третью строку под тем
        # же заголовком.
        "control_holds_everywhere": all(
            item["knob_moves_counter"] and item["low_within_pool"]
            and item["high_within_pool"] for item in by_variant.values()),
        "readings": {"%s@%d" % key: item for key, item in readings.items()},
        # Строка, где соединений больше, чем запросов бывало в полёте, —
        # подогнанное число: новое соединение пул открывает, только когда
        # все открытые уже выданы.
        "rows_with_impossible_numbers": ["%s@%d" % key for key in impossible],
        # УСТОЙЧИВОСТЬ ВНУТРИ СЕССИИ — не условие печати, а оговорка: она
        # не делает замер неверным, но запрещает называть число
        # величиной без слов «в этом прогоне».
        "every_number_stable_within_the_session": stable,
        "no_leak": no_leak,
        "all_served": all_served,
        "nothing_open_before_the_volley": nothing_before,
        "one_driver_for_every_layer": one_driver,
        "pool_publishable": bool(
            all(item["knob_moves_counter"] and item["low_within_pool"]
                and item["high_within_pool"]
                for item in by_variant.values())
            and not impossible
            and no_leak and all_served and nothing_before and one_driver),
    }


def table(rows, sizes=SIZES):
    """Строки таблицы — ТОЛЬКО если контроль сошёлся.

    ПОЧЕМУ ЗАПРЕТ ЖИВЁТ ЗДЕСЬ, А НЕ В ПЕЧАТАЮЩЕМ СЦЕНАРИИ. Сценарий —
    самое удобное место обойти запрет, не тронув ни одной проверки:
    достаточно напечатать числа самому. Здесь запрет можно позвать на
    придуманных строках и потребовать отказа.

    Ни одного слова о скорости в таблице нет и быть не может: стенд не
    меряет ни пропускную способность, ни задержку.
    """
    decision = verdict(rows, sizes=sizes)
    if not decision["pool_publishable"]:
        raise ValueError(
            "ось 2 недействительна, таблица не печатается: %s"
            % json.dumps(decision, ensure_ascii=False, sort_keys=True))
    lines = ["вариант   пул  одновременных  соединений  в полёте  "
             "во что упёрлось          устойчиво по залпам"]
    for name in decision["variants"]:
        for size in sorted(sizes):
            row = setting(rows, name, size)
            item = reading(row)
            if item["saturated_the_pool"]:
                into = "в пул (слотов не осталось)"
            elif item["below_pool_at_inflight_peak"]:
                into = "не в пул (слоты остались)"
            else:
                into = "ни во что из названного"
            lines.append("%-9s %-4d %-14d %-11d %-9s %-24s %s"
                         % (name, size, row["concurrent"],
                            row["connections_observed"],
                            row["inflight_peak"], into,
                            "да" if row.get("connections_stable") else "НЕТ"))
    return lines
