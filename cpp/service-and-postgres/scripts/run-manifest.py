#!/usr/bin/env python3
"""Сводка прогона: ОПУБЛИКОВАННЫЕ ВЕЛИЧИНЫ, а не хеши файлов.

ЗАЧЕМ ЭТО ЕСТЬ.

Фикстуры стенда переснимаются. Утверждение «числа держатся» до сих пор
держалось на памяти автора: прогон перезаписывал те же пять файлов, и
сравнить новый замер с прошлым можно было только глазами по диффу, где
рядом с величинами лежат длительности ответов и сырые залпы. Сводка
вынимает из фикстур ровно то, что попадает в README и в статью, и
кладёт в отдельный файл. Сводки коммитятся — и сравнить две сессии
может кто угодно.

ПОЧЕМУ НЕ ХЕШИ ФИКСТУР. Хеш файла различается при каждом прогоне: в
фикстурах лежат длительности каждого ответа, число заглядываний
наблюдателя и порядок ответов в залпе. Сравнение хешей краснело бы
всегда и потому не значило бы ничего. Суммы фикстур держит МАРКЕР
(`scripts/run-marker.py`) — и он отвечает на другой вопрос: «те ли это
файлы, по которым прогон объявлен полным», а не «те ли это числа».

ЧТО ЗДЕСЬ СРАВНИВАЕТСЯ, А ЧТО НЕТ — СКАЗАНО ПОЛЕМ, А НЕ ПРОЗОЙ.

Каждое поле каждой строки фикстуры обязано быть либо ОПУБЛИКОВАНО
(`PUBLISHED`), либо ИСКЛЮЧЕНО с причиной (`EXCLUDED`). Поле, не
попавшее ни туда, ни туда, роняет сборку сводки. Без этого запрета
величина, выпавшая из сводки, пропадала бы молча: сравнение сличает то,
что в сводках ЕСТЬ, и об отсутствующем не знает ничего.

ПОЛЕ, ИСКЛЮЧЁННОЕ ИЗ СРАВНЕНИЯ, ОБЯЗАНО РАЗЛИЧАТЬСЯ.

Свидетель разных сессий — `pool_reply_ms_total`, сумма длительностей
всех ответов оси пула. Это время, оно исключено из сравнения по
замыслу — и ровно поэтому оно годится в свидетели: у двух РАЗНЫХ
прогонов оно различается по построению. Если свидетель совпал, значит
сравниваются не две сессии, а одна, прочитанная дважды, и вывод
«числа держатся между сессиями» ложен. Это проверяет
`scripts/test_run_manifest.py`.

ЧИСЛО, КОТОРОЕ СТЕНД САМ НАЗВАЛ НЕУСТОЙЧИВЫМ, НЕ СРАВНИВАЕТСЯ.

У drogon ёмкость на оси 1 и число соединений на оси 2 плавают между
сессиями — это записано в README как находка, а не как дефект. Сводка
не делает вид, что их можно сверить: поле сравнивается только если сам
стенд объявил его устойчивым (`capacity_stable`, `connections_stable`).
Пропуск не молчит — он попадает в отчёт сравнения отдельной строкой.
Отсутствие флага устойчивости в старой сводке — тоже пропуск, тоже
названный.

ЗАПУСК (из каталога стенда):
  python3 scripts/run-manifest.py                      # сводка текущего дерева
  python3 scripts/run-manifest.py --from-git REV --session имя [--axes a,b]
  python3 scripts/run-manifest.py --compare имя1 имя2
"""

import argparse
import io
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES = os.path.join(HERE, "fixtures")
MANIFESTS = os.environ.get("MANIFESTS_DIR",
                           os.path.join(FIXTURES, "manifests"))
END_MARKER = "COMPLETE"

AXIS_FILES = {
    "variants": "variants.jsonl",
    "db-wait": "db-wait.jsonl",
    "pool": "pool.jsonl",
    "prepared": "prepared.jsonl",
    "cancel": "cancel.jsonl",
}


# --- что публикуется ---------------------------------------------------
#
# Значение — либо None (величина берётся как есть), либо функция,
# вынимающая из поля то, что публикуется. Функция возвращает словарь:
# его ключи дописываются к имени поля через точку.

def _sub(*names):
    return lambda value: {name: value.get(name) for name in names}


def _capacity(occupancy):
    """Ёмкость по настройке: то, что стоит в таблице «Что замерено».

    Берётся `served_concurrently` — наименьшее по залпам, как и
    печатает разбор. Ключ — число рабочих единиц, а не порядковый
    номер: настройки могут сниматься в любом порядке.
    """
    out = {}
    for item in occupancy["at"]:
        out["at_%d" % item["workers"]] = item["served_concurrently"]
    return out


def _volley_ms_total(volleys):
    return sum(reply["duration_ms"]
               for volley in volleys for reply in volley["replies"])


PUBLISHED = {
    "variants": {
        "axis_1_publishable": None,
        "capacity_stable": None,
        "client_bound_holds": None,
        "healthy_port_open": None,
        "idle_reads_zero": None,
        "pool": None,
        "layer": None,
        "baked_env": None,
        "entrypoint": None,
        "image_tag": None,
        "common_build_flags": None,
        "common_build_defines": None,
        "common_build_includes": None,
        "startup_refusals": None,
        "layer_library": _sub("name", "version"),
        "entry_binary": _sub("sha256", "is_elf", "same_as_build_output"),
        "tools": _sub("cxx", "cmake", "libpqxx"),
        "knob_evidence": _sub("follows_workers", "one_unit_reads_one",
                              "low_peak_max", "high_peak_max",
                              "runs_per_setting"),
        "negative_control": _sub("probes", "reads_one"),
        "occupancy": _capacity,
    },
    "db-wait": {
        "occupies_unit": None,
        "control_is_valid": None,
        "control_workers": None,
        "endpoint": None,
        "probes": None,
        "runs_per_setting": None,
        "sleep_ms": None,
        "entry_binary_sha256": None,
        "entry_binary_matches_variants_fixture": None,
        "sources": _sub("agree_everywhere", "disagreed", "missed",
                        "without_second_source"),
    },
    "pool": {
        "pool_size": None,
        "workers": None,
        "concurrent": None,
        "runs": None,
        "sleep_ms": None,
        "driver": None,
        "served": None,
        "not_served": None,
        "inflight_peak": None,
        "connections_before": None,
        "connections_observed": None,
        "connections_idle_after": None,
        "entry_binary_sha256": None,
        # Строка собственного пула Drogon: в ось она НЕ входит
        # (used_in_axis: false), но величины её сверяются так же.
        "used_in_axis": None,
        "application_name": None,
        "probe_exit_code": None,
        "probe_finished_every_query": None,
        "probe_links": None,
        "links_libpq": None,
        "links_libpqxx": None,
    },
    "prepared": {
        "sql_mode": None,
        "is_the_control": None,
        "driver": None,
        "pool_size": None,
        "workers": None,
        "requests": None,
        "served": None,
        "not_served": None,
        "sleep_ms": None,
        "calls_delta": None,
        "calls_fragment": None,
        "distinct_texts": None,
        "other_texts": None,
        "entry_binary_sha256": None,
        # Строки контрольной пробы: чем она собрана и чем слинкована.
        "built_in_image": None,
        "probe_exit_code": None,
        "probe_finished": None,
        "probe_links": None,
        "links_libpqxx": None,
    },
    "cancel": {
        "counted_application_name": None,
        "sleep_ms": None,
        # Плечо ожидания.
        "replied": None,
        "status": None,
        "active_after_reply": None,
        "queries_after_reply": None,
        # Плечо обрыва.
        "socket_closed": None,
        "bytes_before_close": None,
        "close_after_ms": None,
        "wait_after_close_ms": None,
        "active_before_close": None,
        "active_after_close": None,
        "active_after_window": None,
        "queries_before_close": None,
        "queries_after_close": None,
    },
}


# --- что исключено и почему -------------------------------------------

_SERVICE = "служебное поле фикстуры: имя оси"
_KEY = "имя плеча — ключ группы сводки, а не величина"

EXCLUDED = {
    "variants": {
        "kind": _SERVICE,
        "variant": _KEY,
        "cmd": "командная строка docker build несёт абсолютный путь стенда: "
               "величиной она не является и между машинами различается по "
               "построению",
        "liveness": "сырое наблюдение прогона: число обращений, ответов и "
                    "задержка маршрута живости. README говорит прямо, что "
                    "величина неустойчива — независимый перезамер дал у "
                    "drogon 12 ответов из 12 вместо 14 из 15",
    },
    "db-wait": {
        "kind": _SERVICE,
        "variant": _KEY,
        "at": "сырьё замера: залпы, пробы и их показания",
        "control_headroom": "запас контроля считается из перекрытий проб, а "
                            "они меняются между сессиями — «три из пяти» "
                            "свойством варианта в README не названо",
        "control_overlaps_always": "то же: считается из тех же перекрытий",
        "control_overlaps_ever": "перекрылись ли пробы хотя бы в одном "
                                 "залпе: считается из тех же перекрытий и "
                                 "наследует их неустойчивость",
        "publishable": "вывод, собранный из перекрытий выше: наследует их "
                       "неустойчивость",
    },
    "pool": {
        "kind": _SERVICE,
        "variant": _KEY,
        "volleys": "сырьё замера: ответы залпа с длительностями. Отсюда "
                   "берётся СВИДЕТЕЛЬ сессии — см. WITNESS",
        "connections_by_run": "числа по залпам: у drogon они и дали 5, 5, 6. "
                              "Публикуется наибольшее (connections_observed), "
                              "а разброс по залпам — наблюдение прогона",
        "connections_idle_after_by_run": "числа простаивающих соединений по "
                                         "залпам: публикуется наибольшее "
                                         "(connections_idle_after)",
        "connections_stable": "флаг устойчивости: он не сравнивается, он "
                              "РЕШАЕТ, сравнивать ли connections_observed "
                              "(см. GUARDS)",
        "probe_output": "сырой вывод пробы собственного пула Drogon",
        "why_not_in_the_axis": "причина, записанная прозой, а не величина",
    },
    "prepared": {
        "kind": _SERVICE,
        "variant": _KEY,
        "replies": "сырьё замера: ответы проб",
        "texts_sampled": "сколько раз наблюдатель успел заглянуть в "
                         "pg_stat_activity, пока запросы были живы: зависит "
                         "от планировщика, в прогоне вышло 36, 37, 39 и 40",
        "parameterized_texts": "то же самое число у параметризованных плеч",
        "literal_texts": "то же самое число у контрольного плеча",
        "calls_before": "абсолютный счётчик pg_stat_statements до замера: "
                        "зависит от прошлого этого сервера, сравнивается "
                        "прирост calls_delta",
        "calls_after": "абсолютный счётчик pg_stat_statements после замера: "
                       "зависит от прошлого этого сервера",
        "probe_output": "сырой вывод контрольной пробы",
    },
    "cancel": {
        "kind": _SERVICE,
        "variant": _KEY,
        "arm": "плечо — вторая половина ключа группы",
        "detail": "сырая контрактная строка ответа",
        "seen_active_while_waiting": "сколько раз запрос застали активным до "
                                     "обрыва: число заглядываний, а не "
                                     "величина. Утверждение «застали хотя бы "
                                     "раз» несёт active_before_close",
    },
}


# --- свидетель разных сессий ------------------------------------------
#
# Поле исключено из сравнения И ОБЯЗАНО РАЗЛИЧАТЬСЯ. Совпало — сравнение
# идёт не между двумя сессиями, а внутри одной.

WITNESS = {
    "pool": ("reply_ms_total", _volley_ms_total, "volleys"),
}


# --- поля, сравниваемые только при объявленной устойчивости -----------

GUARDS = {
    "pool": {"connections_observed": "connections_stable",
             "connections_idle_after": "connections_stable"},
    "variants": {"occupancy.at_3": "capacity_stable"},
}


KEY_OF = {
    "variants": lambda row: row["variant"],
    "db-wait": lambda row: row["variant"],
    # В ключ входит РОД строки: у drogon рядом с осевой строкой лежит
    # строка его собственного пула при том же размере, и без рода они
    # накрыли бы друг друга — сводка молча потеряла бы одну из двух.
    "pool": lambda row: "%s/%s/pool=%s" % (row["variant"], row["kind"],
                                           row["pool_size"]),
    "prepared": lambda row: row["variant"],
    "cancel": lambda row: "%s/%s" % (row["variant"], row["arm"]),
}


def rows_of(text, path_for_errors):
    """Строки фикстуры — только из ЗАВЕРШЁННОГО замера.

    Оборванная фикстура (без маркера) даёт меньше строк, а не ошибку, и
    сводка по ней выглядела бы обычной. Поэтому маркер обязателен.
    """
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    if not lines or lines[-1] != END_MARKER:
        raise SystemExit("фикстура не завершена маркером %s: %s"
                         % (END_MARKER, path_for_errors))
    return [json.loads(line) for line in lines[:-1] if line.startswith("{")]


def axis_values(axis, rows, strict=True):
    """Величины одной оси: {ключ группы: {поле: значение}}.

    strict=False — для сводки, собранной из СТАРОГО коммита: набор полей
    фикстуры там мог быть другим, и незнакомое поле не должно ронять
    чтение истории. Оно не пропадает молча: имена таких полей возвращены
    отдельно и попадают в сводку.
    """
    published = PUBLISHED[axis]
    excluded = EXCLUDED[axis]
    out = {}
    unknown = set()
    for row in rows:
        key = KEY_OF[axis](row)
        values = {}
        for name, value in sorted(row.items()):
            if name in excluded:
                continue
            if name not in published:
                if strict:
                    raise SystemExit(
                        "поле %s оси %s не публикуется и не исключено: "
                        "допиши его в PUBLISHED или в EXCLUDED с причиной"
                        % (name, axis))
                unknown.add(name)
                continue
            take = published[name]
            if take is None:
                values[name] = value
            else:
                for sub, item in sorted(take(value).items()):
                    values["%s.%s" % (name, sub)] = item
        witness = WITNESS.get(axis)
        if witness is not None:
            field, extract, source = witness
            if source in row:
                values["!" + field] = extract(row[source])
        out[key] = values
    return out, sorted(unknown)


def build(read, session, source, axes=None, strict=True, omitted=None):
    """Сводка: величины всех осей, чьи фикстуры удалось прочитать.

    `read(имя файла)` отдаёт текст фикстуры или None, если её в этом
    источнике нет.
    """
    manifest = {"session": session, "source": source, "axes": {},
                "unknown_fields": {}, "axes_omitted": dict(omitted or {})}
    for axis, filename in sorted(AXIS_FILES.items()):
        if axes is not None and axis not in axes:
            manifest["axes_omitted"].setdefault(
                axis, "ось в сводку не включена")
            continue
        text = read(filename)
        if text is None:
            manifest["axes_omitted"].setdefault(
                axis, "фикстуры %s в этом источнике нет" % filename)
            continue
        values, unknown = axis_values(axis, rows_of(text, filename),
                                      strict=strict)
        manifest["axes"][axis] = values
        if unknown:
            manifest["unknown_fields"][axis] = unknown
    return manifest


def compare(left, right):
    """Сравнение двух сводок: (расхождения, пропуски).

    Расхождение — это провал. Пропуск — это НАЗВАННОЕ место, где сверки
    не было: ось есть только в одной сводке, поле есть только в одной,
    число объявлено неустойчивым. Пропуски печатаются и возвращаются, а
    не подразумеваются.
    """
    problems = []
    notes = []
    witnessed = 0
    la, ra = left["axes"], right["axes"]
    for axis in sorted(set(la) | set(ra)):
        if axis not in la or axis not in ra:
            notes.append("ось %s есть только в сводке %s"
                         % (axis, left["session"] if axis in la
                            else right["session"]))
            continue
        lk, rk = la[axis], ra[axis]
        for key in sorted(set(lk) | set(rk)):
            if key not in lk or key not in rk:
                notes.append("%s: плечо %s есть только в одной сводке"
                             % (axis, key))
                continue
            problems_here, notes_here = _compare_arm(axis, key, lk[key],
                                                     rk[key])
            problems.extend(problems_here)
            notes.extend(notes_here)
        witnessed += _witness(axis, lk, rk, left, right, problems, notes)
    # СВИДЕТЕЛЬ ОБЯЗАН БЫТЬ ХОТЯ БЫ ОДИН. Пустой список свидетелей —
    # это не «сверка без свидетелей», а сверка, про которую неизвестно,
    # сравнивает ли она две сессии или одну, прочитанную дважды.
    if not witnessed:
        problems.append(
            "ни одного свидетеля: различие сессий ничем не подтверждено — "
            "сравнение могло идти внутри одного прогона")
    return problems, notes


def _compare_arm(axis, key, left, right):
    problems, notes = [], []
    guards = GUARDS.get(axis, {})
    for field in sorted(set(left) | set(right)):
        if field.startswith("!"):
            continue
        if field not in left or field not in right:
            notes.append("%s/%s: поле %s есть только в одной сводке"
                         % (axis, key, field))
            continue
        guard = guards.get(field)
        if guard is not None:
            stable = (left.get(guard), right.get(guard))
            if stable != (True, True):
                notes.append(
                    "%s/%s: %s не сверяется — устойчивость не объявлена "
                    "(%s = %s, %s)" % (axis, key, field, guard,
                                       stable[0], stable[1]))
                continue
        if left[field] != right[field]:
            problems.append("%s/%s: %s — %r против %r"
                            % (axis, key, field, left[field], right[field]))
    return problems, notes


def _witness(axis, lk, rk, left, right, problems, notes):
    """Возвращает число плеч, у которых свидетель нашёлся у обеих сводок."""
    witness = WITNESS.get(axis)
    if witness is None:
        return 0
    field = "!" + witness[0]
    seen = 0
    for key in sorted(set(lk) & set(rk)):
        if field not in lk[key] or field not in rk[key]:
            continue
        seen += 1
        if lk[key][field] == rk[key][field]:
            problems.append(
                "%s/%s: СВИДЕТЕЛЬ %s совпал (%r) — значит сводки %s и %s "
                "сняты одним прогоном, и вывод о двух сессиях ложен"
                % (axis, key, witness[0], lk[key][field],
                   left["session"], right["session"]))
    if not seen:
        problems.append(
            "ось %s: свидетеля %s нет ни в одном общем плече — различие "
            "сессий ничем не подтверждено" % (axis, witness[0]))
    return seen


def load(session):
    path = os.path.join(MANIFESTS, session + ".json")
    with io.open(path, encoding="utf-8") as handle:
        return json.loads(handle.read())


def saved_sessions():
    if not os.path.isdir(MANIFESTS):
        return []
    return sorted(name[:-5] for name in os.listdir(MANIFESTS)
                  if name.endswith(".json"))


def write(manifest):
    if not os.path.isdir(MANIFESTS):
        os.makedirs(MANIFESTS)
    path = os.path.join(MANIFESTS, manifest["session"] + ".json")
    with io.open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(manifest, ensure_ascii=False, indent=2,
                                sort_keys=True) + "\n")
    return path


def _from_tree(name):
    path = os.path.join(FIXTURES, name)
    if not os.path.exists(path):
        return None
    with io.open(path, encoding="utf-8") as handle:
        return handle.read()


def _from_git(rev):
    def read(name):
        spec = "%s:cpp/service-and-postgres/fixtures/%s" % (rev, name)
        proc = subprocess.run(["git", "show", spec], cwd=HERE,
                              capture_output=True)
        if proc.returncode != 0:
            return None
        return proc.stdout.decode("utf-8")
    return read


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--session", default="current")
    parser.add_argument("--from-git", dest="rev", default=None)
    parser.add_argument("--axes", default=None,
                        help="через запятую: только эти оси")
    parser.add_argument("--omitted", default=None,
                        help="причина, по которой остальные оси не взяты")
    parser.add_argument("--compare", nargs=2, default=None)
    args = parser.parse_args(argv)

    if args.compare:
        left, right = (load(name) for name in args.compare)
        problems, notes = compare(left, right)
        for note in notes:
            print("пропуск: %s" % note)
        for problem in problems:
            print("РАСХОЖДЕНИЕ: %s" % problem)
        print("сводки %s и %s: расхождений %d, пропусков %d"
              % (args.compare[0], args.compare[1], len(problems), len(notes)))
        return 1 if problems else 0

    axes = args.axes.split(",") if args.axes else None
    omitted = {}
    if axes is not None and args.omitted:
        for axis in AXIS_FILES:
            if axis not in axes:
                omitted[axis] = args.omitted
    if args.rev:
        manifest = build(_from_git(args.rev), args.session,
                         "фикстуры коммита %s" % args.rev, axes=axes,
                         strict=False, omitted=omitted)
    else:
        manifest = build(_from_tree, args.session, "рабочее дерево",
                         axes=axes, omitted=omitted)
    path = write(manifest)
    print("сводка прогона: %s" % path)
    print("  осей: %d, плеч: %d" % (
        len(manifest["axes"]),
        sum(len(arms) for arms in manifest["axes"].values())))
    return 0


if __name__ == "__main__":
    sys.exit(main())
