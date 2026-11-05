"""Ось 4: переживает ли запрос уход клиента — и что из этого следует.

ЧТО ЗДЕСЬ МЕРЯЕТСЯ. Клиент шлёт `/q?sleep_ms=3000` и обрывает
соединение, не дождавшись ответа. Через фиксированный интервал после
обрыва считается, остался ли запрос варианта в `pg_stat_activity` со
статусом `active`. Наблюдение снимается СНАРУЖИ предмета — из базы, по
`application_name`, — и сервис на него повлиять ничем не может.

ОБРЫВ ДЕЛАЕТ ПРОБА, А НЕ СЕРВИС, и это условие задачи, а не удобство.
Ручки и логика запроса живут в общем `service/main.cpp`, одном на три
слоя: любая правка там меняет бинарник ВСЕХ трёх вариантов, чья сумма
записана в фикстурах осей 1, 2 и 3 и сверяется свежей сборкой. Ровно так
первая редакция задачи 6 завела четвёртое ПЛЕЧО СЕРВИСА и молча испортила
три чужие оси. Уход клиента — свойство клиента, и он тут целиком в
`abandon`: свой сокет, свой `SO_LINGER`, своё закрытие.

КОНТРОЛЬ — НОРМАЛЬНОЕ ЗАВЕРШЕНИЕ. Тот же вариант, тот же запрос, но
клиент дожидается ответа; активных запросов после ответа обязан быть
НОЛЬ. Не ноль — значит счёт видит чужие запросы, и ненулевое число у
брошенного плеча не значит ничего: ось недействительна целиком, таблица
не печатается.

ТРИ УСЛОВИЯ, БЕЗ КОТОРЫХ НАБЛЮДЕНИЕ НЕ НАБЛЮДЕНИЕ:

* запрос был виден активным ДО обрыва (`active_before_close > 0`). Ноль
  наблюдений неотличим от нуля активных, и «остался активным» было бы
  сказано о запросе, которого не было;
* к обрыву от сервиса не пришло НИ ОДНОГО байта. Клиент, ушедший после
  ответа, ничего не бросал;
* счёт снят ПО ИМЕНИ ВАРИАНТА. Счёт без имени показал бы фоновые
  процессы базы — правдоподобное число ни о чём (замер задачи 3,
  `scripts/pg._application_name`).

НАБЛЮДЕНИЕ И МЕХАНИЗМ РАЗВЕДЕНЫ, И ЭТО ГЛАВНОЕ В ЭТОМ МОДУЛЕ. «Запрос
остался активным после обрыва» — факт, снятый из базы. «Потому что
обработчик, синхронно ждущий базу, разрыва не замечает и отмены в libpq
не передаёт» — ОБЪЯСНЕНИЕ по чтению кода, а не замер: снято следствие.
Прежняя редакция объясняла это отменой Asio, «доходящей до сокета», —
но в сервисе нет ни наблюдения за разрывом, ни передачи отмены, и
доходить там нечему. Волна A уже разошлась
на этом месте (код возврата `EAI_SYSTEM` значит «системная ошибка», а не
«не загрузился модуль»), поэтому здесь запрет механический: печать
объяснения проходит через `check_explanation_is_labelled`, и без пометки
оно не печатается вовсе.

НИ СЛОВА О СКОРОСТИ. Ни пропускной способности, ни задержки здесь нет.
"""

import io
import json
import os
import socket
import struct
import sys
import time

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "cancel.jsonl")

# Плечи оси. Имена — ярлыки происхождения строки; ни одно решение разбора
# на них не опирается, кроме выбора того, ЧТО у строки спрашивать.
ABANDONED = "abandoned"
AWAITED = "awaited"

# Ожидание в базе — три секунды: столько названо в спеке оси. Сокет
# закрывается на первой, счёт снимается через секунду после закрытия
# (то есть на второй, когда запрос ещё обязан идти), и ещё раз — после
# того, как всё ожидание с запасом истекло.
SLEEP_MS = 3000
CLOSE_AFTER_MS = 1000
WAIT_AFTER_CLOSE_MS = 1000
WINDOW_MARGIN_MS = 1500
POLL_S = 0.05
# Сколько ждать появления запроса в базе, прежде чем закрывать сокет.
# Это не замер времени: запрос, не дошедший до базы, бросать незачем.
APPEAR_TIMEOUT_S = 5.0

# Одна рабочая единица и один слот пула: у оси один запрос за раз, и
# лишние единицы только добавили бы условий, о которых фикстура молчит.
WORKERS = 1
POOL = 1

# Пометка, под которой печатается объяснение, и слова, которые без неё
# печатать нельзя. Список слов намеренно короткий: он ловит ту самую
# фразу, а не любое упоминание базы.
EXPLANATION_LABEL = "ОБЪЯСНЕНИЕ (не замер)"
MECHANISM_WORDS = ("libpq", "отмена доходит", "отмена не доходит")

EXPLANATION = (
    "в этой реализации RST не остановил запрос в наблюдаемом окне. "
    "Обработчик синхронно ждёт ответа базы, и пока идёт этот вызов, за "
    "разрывом соединения никто не следит: сокет не читается, запроса "
    "отмены в libpq никто не передаёт. Остановила бы запрос другая "
    "реализация — стенд НЕ проверял: снято следствие, а не механизм.")


def check_explanation_is_labelled(text):
    """Пропускает текст с объяснением ТОЛЬКО под пометкой.

    ПОЧЕМУ ЗАПРЕТ МЕХАНИЧЕСКИЙ. Договорённость «мы помним, что это
    объяснение» не переживает ни одной правки печати: фраза остаётся,
    пометка теряется, и объяснение читается как установленная причина.
    Здесь его можно позвать на придуманной строке и потребовать отказа.
    """
    if any(word in text for word in MECHANISM_WORDS):
        if EXPLANATION_LABEL not in text:
            raise ValueError(
                "объяснение напечатано без пометки «%s»: наблюдение «запрос "
                "остался активным» и причина «отмена не доходит до libpq» — "
                "разные утверждения, и второго стенд не доказывает: %s"
                % (EXPLANATION_LABEL, text))
    return text


# --- замер ---------------------------------------------------------------


def count_active(database, variant):
    """Сколько запросов варианта активны ПРЯМО СЕЙЧАС — и какие это тексты.

    Имя варианта здесь не украшение: оно и есть фильтр. Счёт без имени
    (или с пустым именем, которое `scripts/pg` отвергает типом) вернул бы
    фоновые процессы базы, и ненулевое число не значило бы ничего.
    """
    rows = database.active_queries(variant)
    return len(rows), [row["query"] for row in rows]


def _request_bytes(sleep_ms, port):
    return ("GET /q?sleep_ms=%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n"
            "Connection: close\r\n\r\n" % (sleep_ms, port)).encode("ascii")


def abandon(port, database, variant, sleep_ms=SLEEP_MS,
            close_after_ms=CLOSE_AFTER_MS,
            wait_after_close_ms=WAIT_AFTER_CLOSE_MS):
    """Клиент уходит посреди запроса: шлёт, ждёт, закрывает сокет.

    ЗАКРЫТИЕ ЖЁСТКОЕ (`SO_LINGER` с нулевым таймаутом): ядро шлёт RST, а
    не FIN с доводкой. Это один КОНКРЕТНЫЙ способ уйти, и стенд не
    утверждает, что прочие (таймаут, прокси, обрыв сети) дают то же
    самое.

    ЧТО СНИМАЕТСЯ ПЕРЕД УХОДОМ. Число активных запросов варианта — иначе
    «остался активным» было бы сказано о запросе, которого в базе не
    было, — и число байт, пришедших от сервиса. Байты читаются
    НЕБЛОКИРУЮЩЕ: клиент, дождавшийся ответа, ничего не бросал, и это
    обязано попасть в строку числом, а не в рассуждение.
    """
    client = socket.create_connection(("127.0.0.1", port), timeout=5.0)
    try:
        client.sendall(_request_bytes(sleep_ms, port))
        # Ждём, пока запрос ПОЯВИТСЯ в базе: бросать нечего, пока его там
        # нет. Это условие пробы, а не замер времени.
        deadline = time.time() + APPEAR_TIMEOUT_S
        seen = 0
        while time.time() < deadline:
            seen, _ = count_active(database, variant)
            if seen:
                break
            time.sleep(POLL_S)
        time.sleep(close_after_ms / 1000.0)
        active_before, texts_before = count_active(database, variant)
        client.setblocking(False)
        received = 0
        try:
            received = len(client.recv(65536))
        except (BlockingIOError, OSError):
            received = 0
        # RST вместо FIN: соединение обрывается, а не закрывается вежливо.
        client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                          struct.pack("ii", 1, 0))
    finally:
        client.close()
    time.sleep(wait_after_close_ms / 1000.0)
    active_after, texts_after = count_active(database, variant)
    # Второе чтение — после того, как ВСЁ объявленное ожидание истекло с
    # запасом: если число не возвращается к нулю, речь о застрявшей
    # сессии, а не о переживании обрыва, и это находка.
    rest = (sleep_ms + WINDOW_MARGIN_MS - close_after_ms
            - wait_after_close_ms) / 1000.0
    if rest > 0:
        time.sleep(rest)
    active_window, _ = count_active(database, variant)
    return {
        "kind": "cancel",
        "variant": variant,
        "arm": ABANDONED,
        "counted_application_name": variant,
        "sleep_ms": sleep_ms,
        "close_after_ms": close_after_ms,
        "wait_after_close_ms": wait_after_close_ms,
        "seen_active_while_waiting": seen,
        "bytes_before_close": received,
        "socket_closed": True,
        "active_before_close": active_before,
        "queries_before_close": texts_before,
        "active_after_close": active_after,
        "queries_after_close": texts_after,
        "active_after_window": active_window,
    }


def await_reply(port, database, variant, sleep_ms=SLEEP_MS, host=None):
    """КОНТРОЛЬ: клиент дожидается ответа. После ответа активных — ноль.

    Запрос тот же и вариант тот же: контроль, снятый другим запросом,
    сравнивал бы две постановки, а не два исхода ухода клиента.
    """
    status = None
    replied = False
    detail = ""
    try:
        status, body = dockerlib._host(host).get(
            "http://127.0.0.1:%d/q?sleep_ms=%d" % (port, sleep_ms),
            timeout=60)
        replied = status == 200
        detail = body.strip()[:500]
    except Exception as failure:  # OSError, HTTPException и прочее
        detail = str(failure)
    active_after, texts_after = count_active(database, variant)
    return {
        "kind": "cancel",
        "variant": variant,
        "arm": AWAITED,
        "counted_application_name": variant,
        "sleep_ms": sleep_ms,
        "replied": replied,
        "status": status,
        "detail": detail,
        "active_after_reply": active_after,
        "queries_after_reply": texts_after,
    }


def measure(variant, database, sleep_ms=SLEEP_MS, host=None):
    """Две строки на вариант: сперва контроль, потом уход клиента.

    ПОРЯДОК — ЧАСТЬ ЗАМЕРА. Контроль идёт ПЕРВЫМ: брошенный запрос живёт
    в базе ещё секунды после обрыва, и контроль, снятый следом за ним,
    считал бы его остаток своим. Обратный порядок сделал бы нулевой
    контроль невозможным по построению — то есть спрятал бы дефект в
    условие.
    """
    rows = []
    with dockerlib.run_variant(variant, workers=WORKERS, pool=POOL,
                               env={"STAND_DSN": pg.dsn()},
                               network=pg.NETWORK, host=host) as service:
        if not service["ready"]:
            raise RuntimeError("вариант %s не поднялся" % variant)
        rows.append(await_reply(service["port"], database, variant,
                                sleep_ms=sleep_ms, host=host))
        rows.append(abandon(service["port"], database, variant,
                            sleep_ms=sleep_ms))
    return rows


# --- разбор --------------------------------------------------------------


def read(path=FIXTURE, kind="cancel"):
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


def row_for(rows, variant, arm):
    """Строка плеча — или громкий отказ.

    Отсутствующее плечо НЕ подменяется соседним и не превращается в ноль:
    контроль, у которого не хватает половины пары, контролем не является.
    Последняя строка побеждает — то же правило, что у осей 2 и 3: диверсия
    над фикстурой ДОПИСЫВАЕТ испорченную строку.
    """
    for row in reversed(rows):
        if row["variant"] == variant and row["arm"] == arm:
            return row
    raise KeyError("нет замера плеча %s у варианта %s: сравнивать нечего"
                   % (arm, variant))


def variants(rows):
    return sorted({row["variant"] for row in rows})


def verdict(rows):
    """Действительна ли ось 4 — и что она показала.

    Условия перечислены по отдельности намеренно: «непечатно» без причины
    неотличимо от поломки разбора, а причина у каждого своя.

    СТОИТ ОТМЕТИТЬ, ЧЕГО ЗДЕСЬ НЕТ. Сам исход — пережил запрос обрыв или
    нет — в условия печати НЕ входит: опровергнутое ожидание это законный
    результат, и запрещать его печать значило бы печатать только удобное.
    """
    names = variants(rows)
    if not names:
        raise ValueError("в замере нет ни одного варианта")
    by_variant = {}
    survives = {}
    for name in names:
        control = row_for(rows, name, AWAITED)  # нет — KeyError, и это верно
        left = row_for(rows, name, ABANDONED)
        survives[name] = left["active_after_close"] > 0
        by_variant[name] = {
            "active_after_reply": control["active_after_reply"],
            "replied": bool(control["replied"]),
            "active_before_close": left["active_before_close"],
            "active_after_close": left["active_after_close"],
            "active_after_window": left["active_after_window"],
            "bytes_before_close": left["bytes_before_close"],
            "queries_after_close": left["queries_after_close"],
        }
    control_zero = all(by_variant[name]["active_after_reply"] == 0
                       for name in names)
    control_replied = all(by_variant[name]["replied"] for name in names)
    # НОЛЬ НАБЛЮДЕНИЙ — НЕ НОЛЬ АКТИВНЫХ.
    seen = all(by_variant[name]["active_before_close"] > 0 for name in names)
    left_early = all(by_variant[name]["bytes_before_close"] == 0
                     for name in names)
    closed = all(row_for(rows, name, ABANDONED)["socket_closed"]
                 for name in names)
    by_name = all(row["counted_application_name"] == row["variant"]
                  for row in rows)
    ends = all(by_variant[name]["active_after_window"] == 0 for name in names)
    return {
        "variants": names,
        "by_variant": by_variant,
        "control_reads_zero": control_zero,
        "control_got_its_reply": control_replied,
        "query_was_seen_active": seen,
        "client_left_before_the_reply": left_early and closed,
        "every_count_is_by_the_variant_name": by_name,
        # Не условие печати, а наблюдение: сессия, не вернувшаяся к нулю,
        # — находка, и её надо назвать, а не усреднить.
        "the_query_ends_on_its_own": ends,
        "survives_by_variant": survives,
        "survives_everywhere": all(survives.values()),
        "survives_nowhere": not any(survives.values()),
        "cancel_publishable": bool(
            control_zero and control_replied and seen and left_early
            and closed and by_name),
    }


def report(rows):
    """Строки печати — ТОЛЬКО если ось действительна, и с подписью причины.

    ПОЧЕМУ ЗАПРЕТ ЖИВЁТ ЗДЕСЬ, А НЕ В ПЕЧАТАЮЩЕМ СЦЕНАРИИ. Сценарий —
    самое удобное место обойти его, не тронув ни одной проверки:
    достаточно напечатать числа самому. Здесь его можно позвать на
    придуманных строках и потребовать отказа.
    """
    decision = verdict(rows)
    if not decision["cancel_publishable"]:
        raise ValueError(
            "ось 4 недействительна, таблица не печатается: %s"
            % json.dumps(decision, ensure_ascii=False, sort_keys=True))
    lines = ["вариант   активных после ответа  активных до обрыва  "
             "активных после обрыва  через всё окно"]
    for name in decision["variants"]:
        item = decision["by_variant"][name]
        lines.append("%-9s %-22d %-19d %-22d %d"
                     % (name, item["active_after_reply"],
                        item["active_before_close"],
                        item["active_after_close"],
                        item["active_after_window"]))
    lines.append("")
    if decision["survives_everywhere"]:
        lines.append("НАБЛЮДЕНИЕ (замерено): у всех трёх слоёв запрос остался "
                     "в pg_stat_activity активным после того, как клиент "
                     "оборвал соединение.")
    elif decision["survives_nowhere"]:
        lines.append("НАБЛЮДЕНИЕ (замерено): ни у одного слоя активного "
                     "запроса после обрыва не осталось — ожидание до прогона "
                     "опровергнуто пробой, и записано именно это.")
    else:
        lines.append("НАБЛЮДЕНИЕ (замерено): слои разошлись — %s. Это находка, "
                     "и объяснять её надо замером, а не рассуждением."
                     % ", ".join("%s %s" % (name, "остался" if value
                                            else "не остался")
                                 for name, value
                                 in sorted(decision["survives_by_variant"]
                                           .items())))
    lines.append("%s: %s" % (EXPLANATION_LABEL, EXPLANATION))
    for line in lines:
        check_explanation_is_labelled(line)
    return lines
