"""Занимает ли ожидание В БАЗЕ рабочую единицу — счёт, а не рассуждение.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. Спека оси 1 ожидала, что запрос уходит в
PostgreSQL, а слой тем временем свободен. Для слоя на корутинах это
звучит убедительно. Проверяется это не рассуждением о том, как устроен
клиент базы, а двумя одновременными пробами `/q?sleep_ms=…` при ОДНОЙ
рабочей единице: занятость при этом ЧИТАЕТСЯ из ответа — сервис сам
считает обработчиков в полёте.

ПОЧЕМУ СЧЁТ, А НЕ ВРЕМЯ. Прежняя редакция делила ответы на «волны» по
длительности и ради отличия волны от дрожания машины растянула ожидание
до пятнадцати секунд. Так мерилась машина, а не варианты. Счётчик
отвечает на тот же вопрос прямо: если обе пробы были внутри разом, он
прочитается двойкой, когда бы ответы ни пришли.

ЗДЕСЬ ЖЕ ЖИВЁТ ВТОРОЙ ИСТОЧНИК. Счётчик — измеряющий код внутри самого
предмета, то есть слепой заодно с ним. Пока пробы в полёте, число
активных запросов варианта снимается ИЗ САМОЙ БАЗЫ, по application_name,
своим потоком и своим шагом (`dockerlib._sample_second_source`) — не из
цикла, который спрашивает живость: тот сам стоит в очереди за занятым
сервисом, и единственная выборка из него может прийтись уже на пустой
сервис. Источники обязаны сойтись; расхождение записывается как находка,
а не усредняется и не разрешается в пользу удобного. И «расхождение» —
не то же самое, что «источник не поймал момент»: смешивать их в одном
поле значило бы прятать находку под видом статистического шума (см.
`row["sources"]` в `measure`).

ЗАЧЕМ КОНТРОЛЬ ПРИ НЕСКОЛЬКИХ ЕДИНИЦАХ. Без него «счётчик прочитался
единицей» означает что угодно: может быть занята единица, может — весь
сервис, а может пробы вовсе не перекрылись. Если при большем числе
единиц те же пробы перекрылись, дело было в единице.

ПОЧЕМУ КОНТРОЛЬ ИМЕННО ПРИ ТРЁХ. Контрольная настройка у трёх слоёв
ОДНА — иначе сравнивались бы разные постановки, — и годится та, при
которой КАЖДЫЙ слой держит две пробы разом. Одно и то же число единиц
даёт у трёх слоёв разную ёмкость: она снята на ручке /block и лежит в
fixtures/variants.jsonl.

Замер идёт ТЕМ ЖЕ `measure_run`, что и ожидание в обработчике: другой
измеритель дал бы два несравнимых ответа.
"""

import io
import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402

# ОСНАСТКА БАЗЫ ЖИВЁТ В scripts/pg.py, А ЗДЕСЬ ТОЛЬКО ЗОВЁТСЯ. Подъём,
# готовность и счёт активных запросов были написаны и выправлены здесь,
# но второе место, поднимающее PostgreSQL по-своему, означало бы второй
# набор условий замера, о котором фикстура молчит. Имена ниже оставлены
# как ссылки на канонические: обвязка оси 1 их читает.
NETWORK = pg.NETWORK
POSTGRES = pg.CONTAINER
POSTGRES_IMAGE = pg.image()
DSN = pg.dsn()
SLEEP_MS = dockerlib.WAIT_MS
PROBES = 2
# ПОЧЕМУ ПЯТЬ ЗАЛПОВ, А НЕ ТРИ. Вывод «контроль перекрылся ВСЕГДА» тем
# честнее, чем больше у него было шансов упасть.
RUNS = 5
# Число единиц у контроля: см. «ПОЧЕМУ КОНТРОЛЬ ИМЕННО ПРИ ТРЁХ».
CONTROL_WORKERS = 3


def start_postgres(host=None, timeout_s=pg.READY_TIMEOUT_S):
    """Поднимает базу для пробы ОСИ 1 — канонической оснасткой.

    БЕЗ СТАТИСТИКИ НАМЕРЕННО. `pg_stat_statements` требует флага при
    старте сервера и не бесплатен для каждого запроса; здесь спрашивается
    только про ожидание, и лишнее в образе базы было бы лишним в ответе.
    Фикстура этой оси снята без расширения — включить его молча значило
    бы перезамерить ось другой базой, не сказав об этом.

    Почему готовность проверяется запросом, а не pg_isready, и почему
    роль заводится с повторами — см. `scripts/pg.wait_ready` и
    `scripts/pg._create_role`: обоснование переехало туда вместе с кодом.
    """
    pg.start(host=host, statements=False, timeout_s=timeout_s)


def stop_postgres(host=None):
    pg.stop(host=host)


def active_query_count(variant, host=None):
    """ВТОРОЙ ИСТОЧНИК: сколько запросов варианта выполняется в базе сейчас.

    Сам счёт — `scripts/pg.active_query_count`; здесь оставлено имя,
    которым его зовёт замер оси 1.
    """
    return pg.active_query_count(variant, host=host)


def sampler_for(variant, host=None):
    """Замыкание второго источника — то, что передаётся в замер."""
    return lambda: active_query_count(variant, host=host)


def verdict(at_one, at_many, probes=PROBES):
    """Что именно показали две настройки — без единого слова о механизме.

    `at_one`, `at_many` — ПРОЧИТАННАЯ занятость по залпам при одной
    рабочей единице и при контрольной настройке.

    `occupies_unit` — при ОДНОЙ единице ни в одном залпе в полёте не было
    двоих: ожидание в базе единицу занимает.
    `control_overlaps_always` — при контрольной настройке те же пробы
    перекрылись в КАЖДОМ залпе. Только при этом условии сказано, что
    занята единица, а не сервис целиком; иначе вывод для этого слоя не
    подтверждён.
    """
    if not at_one or not at_many:
        raise ValueError("выводу нужны залпы при обеих настройках")
    return {
        "occupies_unit": all(value < probes for value in at_one),
        "control_overlaps_always": all(value == probes for value in at_many),
        "control_overlaps_ever": any(value == probes for value in at_many),
    }


def _variants_row(name):
    """Строка варианта из fixtures/variants.jsonl, или бросает."""
    path = os.path.join(STAND, "fixtures", "variants.jsonl")
    with io.open(path, encoding="utf-8") as handle:
        for line in handle:
            if not line.strip().startswith("{"):
                continue
            row = json.loads(line)
            if row["variant"] == name:
                return row
    raise RuntimeError(
        "в замере занятости нет варианта %s: читать оттуда нечего" % name)


def control_headroom(name, probes=PROBES, variants_row=None):
    """Запас контрольной настройки: ёмкость варианта минус число проб.

    ЗАЧЕМ ЭТО В ФИКСТУРЕ. Контроль выбран так, чтобы каждый слой держал
    разом две пробы. У слоя с ёмкостью ровно две запас нулевой: стоит
    одному залпу дать на пробу меньше, и «контроль перекрылся всегда»
    станет ложью, не переставая быть записанным. Запас записывается
    числом, чтобы читатель видел, на чём держится вывод.

    Ёмкость берётся из ЗАМЕРА занятости (`fixtures/variants.jsonl`), а не
    из рассуждения. `variants_row` — только для проверок измерителя на
    подменном исполнителе: там читать реальный файл неоткуда и незачем,
    строка подставляется той же формы, какую даёт `measure_variant`.
    """
    row = variants_row if variants_row is not None else _variants_row(name)
    for setting in row["occupancy"]["at"]:
        if setting["workers"] == CONTROL_WORKERS:
            return setting["served_concurrently"] - probes
    raise RuntimeError(
        "в замере занятости нет варианта %s при %d единицах: запас контроля "
        "выводить не из чего" % (name, CONTROL_WORKERS))


def variants_entry_sha256(name, variants_row=None):
    """Сумма точки входа, ЗАПИСАННАЯ в fixtures/variants.jsonl.

    Читается для того, чтобы её было с чем сверить: db-wait.jsonl читает
    запас контроля из этого же файла, а сам он снимается в ДРУГОЙ сессии
    докера. Без явной сверки две фикстуры, снятые с разных сборок, дали
    бы согласованный на вид, но неверный запас — числа читались бы
    правильно, только про разные бинарники.
    """
    row = variants_row if variants_row is not None else _variants_row(name)
    return row["entry_binary"]["sha256"]


def summarize_sources(volleys):
    """Сведение двух источников по списку залпов — ТРИ исхода, а не два.

    Источники СОШЛИСЬ, источники РАЗОШЛИСЬ (оба что-то увидели, числа
    разные) и источник НЕ ПОЙМАЛ момент (не увидел ничего, хотя счётчик
    утверждает, что внутри было больше одного). Смешение второго и
    третьего — ровно та находка, которую нельзя спрятать усреднением:
    «сошлись почти везде» неотличимо от «почти никогда не спрашивали
    вовремя», если оба исхода лежат в одном поле.

    ВЫНЕСЕНО ОТДЕЛЬНО НАМЕРЕННО. Формулу можно испортить, не тронув ни
    одного образа — например, забыть один из трёх терминов в
    `agree_everywhere`. Испорченную формулу трудно поймать на живом
    замере: там `disagreed` и `missed` почти всегда идут парой (оба ловят
    один и тот же неудачный залп), и пропажа одного термина прячется за
    другим. Отдельная функция позволяет проверить каждый термин на
    придуманных залпах, где они не связаны.
    """
    disagreed = [item for item in volleys
                if item["sources_agree"] is False
                and not item["sources_missed"]]
    missed = [item for item in volleys if item["sources_missed"]]
    unseen = [item for item in volleys if item["sources_agree"] is None]
    return {
        "volleys": len(volleys),
        "disagreed": len(disagreed),
        "missed": len(missed),
        "without_second_source": len(unseen),
        "agree_everywhere": not disagreed and not missed and not unseen,
    }


def measure(name, host=None, runs=RUNS, probes=PROBES, sleep_ms=SLEEP_MS,
           variants_row=None):
    """Строка фикстуры для одного варианта.

    `variants_row` — только для проверок измерителя на подменном
    исполнителе: там читать `fixtures/variants.jsonl` неоткуда, и без
    этого параметра каждая такая проверка либо не собиралась вовсе, либо
    молча сверялась бы с чужим (закоммиченным настоящим) замером. В
    боевом вызове (`run-db-wait.py`) параметр не передаётся, и функция
    читает committed-фикстуру, как и раньше.
    """
    sampler = sampler_for(name, host=host)
    at = []
    for workers in (1, CONTROL_WORKERS):
        measured = [
            dockerlib.measure_run(
                name, workers=workers, probes=probes, wait_ms=sleep_ms,
                endpoint="q", sampler=sampler, env={"STAND_DSN": DSN},
                network=NETWORK, host=host)
            for _ in range(runs)]
        at.append({
            "workers": workers,
            "probes": probes,
            "runs": [dockerlib.peak_of(item) for item in measured],
            "volleys": measured,
            "attempts_used": [item["attempts_used"] for item in measured],
            "stalls": sum(item["stalls"] for item in measured),
        })
    headroom = control_headroom(name, probes, variants_row=variants_row)
    # СУММА ТОЧКИ ВХОДА, СНЯТАЯ ЗАНОВО В ЭТОЙ СЕССИИ, а не переписанная
    # из чужой фикстуры: только так сверка с fixtures/variants.jsonl
    # доказывает, что запас контроля читается про ТОТ ЖЕ бинарник, а не
    # просто совпадает по имени поля.
    fresh_sha256 = dockerlib.entry_binary(name, host=host)["sha256"]
    row = {
        "kind": "db-wait",
        "variant": name,
        "endpoint": "q",
        "sleep_ms": sleep_ms,
        "probes": probes,
        # Настройка контроля записывается ИЗ КОНСТАНТЫ КОДА: число,
        # оставшееся в фикстуре само по себе, разъезжается с обвязкой
        # молча — замер говорил бы про одно, а меряли бы другое.
        "control_workers": CONTROL_WORKERS,
        "runs_per_setting": runs,
        "control_headroom": headroom,
        # ЗАПАС МОЖЕТ БЫТЬ ОТРИЦАТЕЛЬНЫМ: ёмкость варианта на контрольной
        # настройке замерена НИЖЕ числа проб. Отрицательный запас — не то
        # же самое, что нулевой: при нулевом контроль ещё способен
        # перекрыться (обе пробы влезают ровно), при отрицательном —
        # уже нет, и это не запас контроля, а его отсутствие. Смешивать
        # два состояния в одном булевом поле нельзя — их различает
        # только знак, а `publishable` иначе прочёл бы отрицательный
        # запас как «есть какой-то».
        "control_is_valid": headroom >= 0,
        # СВЯЗЬ С ЗАМЕРОМ ЗАНЯТОСТИ. `entry_binary_sha256` снят ЗАНОВО, в
        # этой же сессии докера; `entry_binary_matches_variants_fixture`
        # сравнивает его с суммой, записанной в fixtures/variants.jsonl.
        # Без этой сверки запас контроля читался бы из чужой сборки
        # молча: `control_headroom` не знает, той ли версии образ он
        # застал.
        "entry_binary_sha256": fresh_sha256,
        "entry_binary_matches_variants_fixture": (
            fresh_sha256 == variants_entry_sha256(
                name, variants_row=variants_row)),
        "at": at,
    }
    row.update(verdict(at[0]["runs"], at[1]["runs"], probes=probes))
    row["sources"] = summarize_sources(
        [item for setting in at for item in setting["volleys"]])
    # ПЕЧАТАЕТСЯ ЛИ ЭТА СТРОКА. Вывод про единицу держится на контроле, а
    # сам счётчик — на втором источнике; и то, и другое нуждается в
    # ЗАПАСЕ контроля, а не только в его знаке «разошёлся / не разошёлся».
    # Нет любого из трёх — ось недействительна, и это записано, а не
    # спрятано.
    row["publishable"] = bool(row["control_overlaps_always"]
                              and row["sources"]["agree_everywhere"]
                              and row["control_is_valid"]
                              and row["entry_binary_matches_variants_fixture"])
    return row
