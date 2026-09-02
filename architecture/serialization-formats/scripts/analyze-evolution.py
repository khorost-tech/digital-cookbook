# -*- coding: utf-8 -*-
"""Разбор фикстуры оси эволюции схемы (Задача 6 + круги правок 6bis,
6ter): таблица «изменение x плечо x направление» из пяти исходов (ok,
refused, wrong, n/a, error — schemas/spec.md §1), снятая обеими
реализациями по обоим видам пробы (compat, roundtrip).

ЧТО УЖЕ ВЫЧИСЛЕНО, А ЧТО ДЕЛАЕТ ЭТОТ РАЗБОР. Исходы клеток вычисляет
сама проба. Работа этого файла — не пересчитать исходы, а СНЯТУЮ
фикстуру проверить так, чтобы подмену или нашу собственную порчу нельзя
было спрятать под видом «так и было задумано форматом»:

  1. Сверка с записанными ЗАРАНЕЕ ожиданиями (schemas/expected.json).
     Каталог существует только для compat. Расхождение с expected.json
     НЕ делает прогон недействительным: таблица написана ДО замера, и
     её опровержение — предмет статьи, а не повод считать разбор
     сломанным. Проверка ДВУСТОРОННЯЯ: клетка каталога без строки в
     фикстуре — находка (missing_cell), клетка фикстуры без записи в
     каталоге — тоже находка (unexpected_cell).
  2. Клетка с исходом "wrong" обязана нести наблюдаемое значение — поле
     "got", причём КЛЮЧ должен присутствовать (falsy, но ПРИСУТСТВУЮЩЕЕ
     значение — легитимное наблюдение, а не отсутствие).
  3. Расщепление клетки: если пять записей одной клетки дают РАЗНЫЕ
     исходы — печатается отдельно и заметно (cell_split) в отчёте.
     ЭТО ЧИСТО ОПИСАТЕЛЬНАЯ печать (круг 6ter): расщепление само по
     себе не находка, если оно совпадает с тем, что объявляет каталог
     (см. пункт 7 ниже) — а если не совпадает, каждая отклонившаяся
     запись уже поймана требованием 1 как обычный expected_mismatch.
  4. Межъязыковая построчная сверка: Go и Java обязаны совпасть по всем
     полям контракта, КРОМЕ cell, lang и error — за вычетом ОБЪЯВЛЕННОГО
     каталогом расхождения (см. пункт 7). Всё НЕобъявленное красит
     прогон.
  5. Оборванная фикстура валит разбор (TruncatedFixture) с ВНЯТНЫМ
     сообщением — включая случай, когда фикстура повреждена посередине
     (невалидный JSON в строке данных).
  6. Полнота клетки: ровно пять записей с номерами 0..4 без пропусков и
     повторов, и общее число строк фикстуры совпадает с объявленным в
     шапке (env.expected_lines).
  7. КРУГ ПРАВОК 6ter (по требованию ревью). Раньше исключения
     "клетка расщепляется" и "языки законно расходятся" были множествами,
     вшитыми В ЭТОТ ФАЙЛ (KNOWN_SPLIT_CELLS,
     KNOWN_LANGUAGE_DIVERGENT_CELLS) — то есть самая ответственная часть
     стенда (что валит прогон, а что нет) лежала ВНЕ цепочки дайджестов
     манифеста и не менялась вместе со схемами и записями. Теперь ЭТО
     ДАННЫЕ: значение клетки в schemas/expected.json — не только простая
     строка-исход, но и, где нужно, структура:
       - {"by_record": {"0": "wrong", "1": "refused", ...}, "reason": "..."}
         — клетка расщепляется ПРЕДНАМЕРЕННО, исход зависит от номера
         записи (сравнимо между языками — Go и Java обязаны дать ОДНО
         И ТО ЖЕ значение на каждый номер записи);
       - {"by_lang": {"go": "wrong", "java": "refused"}, "reason": "..."}
         — языки ЗАКОННО дают разные исходы (расхождение БИБЛИОТЕК, а не
         стенда) — сравнимо ПО КАЖДОМУ языку отдельно с ЕГО СОБСТВЕННЫМ
         объявленным значением, а не друг с другом.
     Оба поля сопровождаются ОБЯЗАТЕЛЬНЫМ "reason" — что за явление и
     почему оно не является находкой. Разбор эти данные ТОЛЬКО ЧИТАЕТ
     (resolve_expected, declares_lang_divergence) — не владеет ими и не
     хранит копию решения где-то ещё. Любое ЗНАЧЕНИЕ, не совпадающее с
     объявленным (для любого языка, любой записи), — обычная, свежая
     находка (expected_mismatch / cross_lang_mismatch), без исключений.

ЧЕГО ОСТЕРЕГАТЬСЯ (девять кругов правок стенда сводились к одной
болезни). Неожиданный исход здесь — сперва повод проверить, не наш ли
это дефект, и только потом — претендент на находку про формат.
"""

import json
import os
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "EVOLUTION_FIXTURE", Path(__file__).resolve().parent.parent / "fixtures" / "evolution.txt"))
EXPECTED_PATH = Path(os.environ.get(
    "EVOLUTION_EXPECTED", Path(__file__).resolve().parent.parent / "schemas" / "expected.json"))

COMPLETE_MARKER = "COMPLETE"

# Виды пробы, которые может нести фикстура. "size" сюда не относится —
# у него своя фикстура и свой разбор (Задача 5, analyze-size.py).
KNOWN_KINDS = ("compat", "roundtrip")

# Поля строки результата (schemas/spec.md §11), которые ОБЯЗАНЫ
# совпасть между реализациями на одной и той же координате. cell и lang
# исключены по построению (они и есть то, чем строки отличаются между
# языками); error исключён явно спекой — текст диагностики не часть
# контракта.
CROSS_LANG_FIELDS = (
    "kind", "format", "change", "direction", "record_index",
    "stage", "outcome", "encoded", "bytes", "record", "want", "got",
)

# Поля, производные от "outcome": когда каталог объявляет, что языки
# ЗАКОННО расходятся по исходу на этой клетке (by_lang) и наблюдение
# СОВПАДАЕТ с объявленным для КАЖДОГО языка отдельно, разница в этих
# полях — прямое следствие разного исхода, а не второе, независимое
# расхождение. "record" сюда НЕ входит: входная запись обязана быть
# одной и той же у обоих языков всегда, независимо от исхода.
OUTCOME_DERIVED_FIELDS = frozenset({"outcome", "want", "got", "stage", "bytes", "encoded"})

# Номера канонических записей — ровно пять, 0..4 (schemas/records.json,
# §3.5 spec.md).
EXPECTED_RECORD_INDICES = frozenset(range(5))


class TruncatedFixture(Exception):
    """Фикстура недописана или повреждена: нет маркера COMPLETE в конце,
    ИЛИ одна из строк данных — не валидный JSON (испорчена посередине)."""


class MalformedExpectedEntry(Exception):
    """Запись schemas/expected.json — не строка-исход и не одна из двух
    признанных структур (by_lang/by_record). Это ошибка каталога, а не
    находка о формате: сверяться не с чем, пока запись не приведена к
    одному из двух видов."""


def resolve_expected(entry, lang, record_index):
    """Разворачивает запись expected.json в конкретный исход для
    (lang, record_index). Три формы, каждая может быть вложена в
    другую:

      - строка -- исход как есть, один на все языки и все записи;
      - {"by_lang": {"go": <форма>, "java": <форма>}, ...} -- своя форма
        на каждый язык;
      - {"by_record": {"0": <форма>, ..., "4": <форма>}, ...} -- своя
        форма на каждый номер записи.

    Возвращает None, если структура присутствует, но не покрывает этот
    конкретный (lang, record_index) -- это отдельная находка
    (expected_entry_incomplete), а не молчаливый пропуск: объявленная
    структура обязана быть исчерпывающей."""
    if isinstance(entry, str):
        return entry
    if not isinstance(entry, dict):
        raise MalformedExpectedEntry("запись expected.json — не строка и не объект: %r" % (entry,))
    if "by_lang" in entry:
        sub = entry["by_lang"].get(lang)
        if sub is None:
            return None
        return resolve_expected(sub, lang, record_index)
    if "by_record" in entry:
        sub = entry["by_record"].get(str(record_index))
        if sub is None:
            return None
        return resolve_expected(sub, lang, record_index)
    raise MalformedExpectedEntry(
        "объект в expected.json без \"by_lang\" и без \"by_record\": %r" % (entry,))


def declares_lang_divergence(entry):
    """Истина, если где-то внутри записи (возможно, вложенно под
    by_record) объявлено, что языки могут дать разный исход. Только
    такие клетки получают снисхождение в межъязыковой сверке -- и то
    лишь там, где наблюдение СОВПАДАЕТ с объявленным для каждого языка
    (см. _cross_lang_findings)."""
    if isinstance(entry, str):
        return False
    if not isinstance(entry, dict):
        return False
    if "by_lang" in entry:
        return True
    if "by_record" in entry:
        return any(declares_lang_divergence(v) for v in entry["by_record"].values())
    return False


def collect_reasoned_entries(expected):
    """Обходит каталог целиком и возвращает список (change, format,
    direction, entry) для КАЖДОЙ записи, использующей by_lang/by_record
    (то есть несущей "reason"). Источник -- сами данные, а не отдельный
    список в коде: печать в отчёте берёт объяснения ОТСЮДА, а не из
    константы, которая может отстать от каталога."""
    out = []
    for change, by_format in expected.items():
        for fmt, by_direction in by_format.items():
            for direction, entry in by_direction.items():
                if isinstance(entry, dict):
                    out.append((change, fmt, direction, entry))
    return out


def parse_fixture(text):
    """Разбирает текст фикстуры в (rows, env).

    rows -- строки kind="compat" или kind="roundtrip"; env -- единственная
    строка kind="env" с версиями обеих реализаций. Комментарии (строки с
    '#') пропускаются. Последняя непустая строка обязана быть литералом
    COMPLETE -- иначе фикстура читается как оборванная (TruncatedFixture),
    а не как частичный, но валидный результат.
    """
    lines = [line for line in text.splitlines() if line.strip() != ""]
    if not lines or lines[-1].strip() != COMPLETE_MARKER:
        raise TruncatedFixture(
            "фикстура повреждена или недописана: последняя строка — не "
            f"{COMPLETE_MARKER!r} (прогон мог оборваться посреди клетки)")

    data_lines = [line for line in lines[:-1] if not line.lstrip().startswith("#")]

    rows = []
    env = None
    for n, line in enumerate(data_lines, start=1):
        try:
            obj = json.loads(line)
        except json.JSONDecodeError as e:
            raise TruncatedFixture(
                f"фикстура повреждена: строка данных №{n} — не валидный JSON "
                f"({e}); содержимое строки: {line!r}") from e
        kind = obj.get("kind")
        if kind == "env":
            if env is not None:
                raise ValueError("в фикстуре больше одной строки env — должна быть ровно одна")
            env = obj
        elif kind in KNOWN_KINDS:
            rows.append(obj)
        else:
            raise ValueError(f"неизвестный kind в строке фикстуры: {kind!r}")

    if env is None:
        raise ValueError("в фикстуре нет строки env с версиями реализаций")
    if not rows:
        raise ValueError("в фикстуре нет ни одной строки compat/roundtrip — измерять нечего")
    return rows, env


def load_fixture(path=None):
    path = Path(path) if path else FIXTURE
    return parse_fixture(path.read_text(encoding="utf-8"))


def load_expected(path=None):
    path = Path(path) if path else EXPECTED_PATH
    return json.loads(path.read_text(encoding="utf-8"))


def _index(rows):
    """rows -> {lang: {kind: {format: {change: {direction: {record_index: row}}}}}}."""
    out = {}
    for r in rows:
        by_change = out.setdefault(r["lang"], {}).setdefault(r["kind"], {}).setdefault(r["format"], {})
        by_direction = by_change.setdefault(r["change"], {})
        by_direction.setdefault(r["direction"], {})[r["record_index"]] = r
    return out


def _freeze(value):
    """Делает значение хешируемым для сравнения через set(): dict/list ->
    кортежи. record/want/got — произвольные JSON-объекты, и без этого
    сравнение "совпадают ли значения между языками" не построить."""
    if isinstance(value, dict):
        return tuple(sorted((k, _freeze(v)) for k, v in value.items()))
    if isinstance(value, list):
        return tuple(_freeze(v) for v in value)
    return value


def find_findings(rows, expected, env=None):
    """Возвращает список находок — фактов, а не приговоров. Каждая
    находка — словарь с полем "kind". Расщепление клетки (cell_split)
    сюда НЕ входит — это чисто описательная информация для отчёта (см.
    docstring модуля, пункт 3/7): корректность уже полностью проверяется
    требованиями 1 и 4, а расщепление само по себе не может быть
    "плохим" или "хорошим" независимо от них."""
    idx = _index(rows)
    findings = []
    findings.extend(_expected_comparison_findings(idx, expected))
    findings.extend(_unexpected_cell_findings(idx, expected))
    findings.extend(_wrong_without_observation_findings(rows))
    findings.extend(_cross_lang_findings(idx, expected))
    findings.extend(_completeness_findings(idx, rows, env))
    return findings


def _expected_comparison_findings(idx, expected):
    """Требование 1. Сверка ТОЛЬКО для kind="compat". Круг 6ter: запись
    каталога МОЖЕТ быть структурой (by_lang/by_record) — resolve_expected
    разворачивает её для конкретных (lang, record_index) прозрачно для
    этой функции: сравнение то же самое, что и для плоской строки."""
    findings = []
    langs = sorted(idx)
    for change, by_format in expected.items():
        for fmt, by_direction in by_format.items():
            for direction, entry in by_direction.items():
                for lang in langs:
                    cell_rows = (idx.get(lang, {}).get("compat", {})
                                 .get(fmt, {}).get(change, {}).get(direction, {}))
                    if not cell_rows:
                        findings.append({
                            "kind": "missing_cell",
                            "lang": lang, "op": "compat",
                            "format": fmt, "change": change, "direction": direction,
                            "expected": entry,
                        })
                        continue
                    for record_index, row in sorted(cell_rows.items()):
                        exp_outcome = resolve_expected(entry, lang, record_index)
                        if exp_outcome is None:
                            findings.append({
                                "kind": "expected_entry_incomplete",
                                "lang": lang, "format": fmt, "change": change,
                                "direction": direction, "record_index": record_index,
                            })
                            continue
                        observed = row.get("outcome")
                        if observed != exp_outcome:
                            findings.append({
                                "kind": "expected_mismatch",
                                "lang": lang, "format": fmt, "change": change,
                                "direction": direction, "record_index": record_index,
                                "expected": exp_outcome, "observed": observed,
                                "error": row.get("error"),
                            })
    return findings


def _unexpected_cell_findings(idx, expected):
    """Обратная сторона требования 1: клетка, которую снял прогон, но
    которой нет в schemas/expected.json, — тоже находка, а не тихо
    пропущенная координата."""
    findings = []
    for lang, kinds in idx.items():
        compat = kinds.get("compat", {})
        for fmt, changes in compat.items():
            for change, directions in changes.items():
                for direction in directions:
                    if expected.get(change, {}).get(fmt, {}).get(direction) is None:
                        findings.append({
                            "kind": "unexpected_cell",
                            "lang": lang, "format": fmt, "change": change, "direction": direction,
                        })
    return findings


def _wrong_without_observation_findings(rows):
    """Требование 2. "wrong" без "got" — утверждение "прочиталось
    неверно" ничем не подтверждено. Проверяется ПРИСУТСТВИЕ КЛЮЧА, а не
    истинность значения: пустая, но полученная запись — легитимное
    наблюдение, а не отсутствие."""
    findings = []
    for row in rows:
        if row.get("outcome") == "wrong" and "got" not in row:
            findings.append({
                "kind": "wrong_without_observation",
                "lang": row.get("lang"), "op": row.get("kind"),
                "format": row.get("format"), "change": row.get("change"),
                "direction": row.get("direction"), "record_index": row.get("record_index"),
            })
    return findings


def _cell_split_findings(idx):
    """Чисто ОПИСАТЕЛЬНАЯ сводка расщеплений клетки — для отчёта, не
    для кода возврата (см. docstring модуля, пункт 3/7). Если пять
    записей одной клетки дают разные исходы, это стоит показать явно
    независимо от того, ожидалось расщепление каталогом или нет:
    корректность уже проверена по каждой записи требованием 1."""
    splits = []
    for lang, kinds in idx.items():
        for kind, formats in kinds.items():
            for fmt, changes in formats.items():
                for change, directions in changes.items():
                    for direction, records in directions.items():
                        outcomes_by_record = {ri: r.get("outcome") for ri, r in records.items()}
                        if len(set(outcomes_by_record.values())) > 1:
                            splits.append({
                                "kind": "cell_split",
                                "lang": lang, "op": kind, "format": fmt,
                                "change": change, "direction": direction,
                                "outcomes_by_record": outcomes_by_record,
                            })
    return splits


def _cross_lang_findings(idx, expected):
    """Требование 4. Построчная сверка Go/Java по всем полям контракта,
    кроме cell, lang (различаются по построению) и error (текст
    диагностики прямо выведен из контракта спекой, §11).

    Круг 6ter: если для этой клетки каталог ОБЪЯВИЛ расхождение языков
    (declares_lang_divergence), и наблюдение для КАЖДОГО языка СОВПАДАЕТ
    с ЕГО СОБСТВЕННЫМ объявленным исходом — поля, производные от исхода
    (OUTCOME_DERIVED_FIELDS), не считаются расхождением: они РАЗНЫЕ
    ровно потому, что объявлено разное ожидание, а не потому, что что-то
    сломалось. Если наблюдение хоть на йоту разошлось с объявленным —
    снисхождения нет, и разница красится как обычно: непредвиденное
    расхождение (даже если оно "похоже" на уже известное) обязано
    гореть."""
    findings = []
    langs = sorted(idx)
    if len(langs) < 2:
        return findings  # сравнивать не с чем — не находка, а свойство входа

    keys = set()
    for lang in langs:
        for kind, formats in idx[lang].items():
            for fmt, changes in formats.items():
                for change, directions in changes.items():
                    for direction, records in directions.items():
                        for record_index in records:
                            keys.add((kind, fmt, change, direction, record_index))

    for kind, fmt, change, direction, record_index in sorted(keys):
        rows_here = {}
        for lang in langs:
            row = (idx.get(lang, {}).get(kind, {}).get(fmt, {})
                   .get(change, {}).get(direction, {}).get(record_index))
            if row is not None:
                rows_here[lang] = row

        missing_langs = [lang for lang in langs if lang not in rows_here]
        if missing_langs:
            findings.append({
                "kind": "missing_lang_row",
                "op": kind, "format": fmt, "change": change, "direction": direction,
                "record_index": record_index,
                "present_langs": sorted(rows_here), "missing_langs": missing_langs,
            })
            continue

        divergence_allowed = False
        if kind == "compat":
            entry = expected.get(change, {}).get(fmt, {}).get(direction)
            if entry is not None and declares_lang_divergence(entry):
                divergence_allowed = all(
                    resolve_expected(entry, lang, record_index) == r.get("outcome")
                    for lang, r in rows_here.items()
                )

        for field in CROSS_LANG_FIELDS:
            if divergence_allowed and field in OUTCOME_DERIVED_FIELDS:
                continue
            values = {lang: r.get(field) for lang, r in rows_here.items()}
            if len({_freeze(v) for v in values.values()}) > 1:
                findings.append({
                    "kind": "cross_lang_mismatch",
                    "op": kind, "format": fmt, "change": change, "direction": direction,
                    "record_index": record_index, "field": field, "values": values,
                })
    return findings


def _completeness_findings(idx, rows, env):
    """Требование 6. Ровно пять записей 0..4 без пропусков и повторов в
    каждой клетке, и общее число строк фикстуры совпадает с объявленным
    в шапке (env.expected_lines, если оно есть)."""
    findings = []
    for lang, kinds in idx.items():
        for kind, formats in kinds.items():
            for fmt, changes in formats.items():
                for change, directions in changes.items():
                    for direction, records in directions.items():
                        present = set(records)
                        if present != EXPECTED_RECORD_INDICES:
                            findings.append({
                                "kind": "incomplete_cell",
                                "lang": lang, "op": kind, "format": fmt,
                                "change": change, "direction": direction,
                                "missing": sorted(EXPECTED_RECORD_INDICES - present),
                                "unexpected": sorted(present - EXPECTED_RECORD_INDICES),
                            })

    if env is not None and "expected_lines" in env:
        expected_lines = env["expected_lines"]
        if len(rows) != expected_lines:
            findings.append({
                "kind": "fixture_line_count_mismatch",
                "expected_lines": expected_lines,
                "actual_lines": len(rows),
            })
    return findings


def _outcomes_table(idx, kind):
    """Сводит клетки (без разбивки по записям — берётся первая строка
    клетки; расхождения внутри клетки уже отдельно ловит cell_split) в
    таблицу «изменение x плечо x направление -> исход» на язык. Только
    для печати полной таблицы исходов в отчёте."""
    table = {}
    for lang, kinds in idx.items():
        formats = kinds.get(kind, {})
        for fmt, changes in formats.items():
            for change, directions in changes.items():
                for direction, records in directions.items():
                    if not records:
                        continue
                    first = records[min(records)]
                    table.setdefault(lang, {}).setdefault(change, {}).setdefault(fmt, {})[direction] = (
                        first.get("outcome"))
    return table


def _print_table(idx, kind):
    table = _outcomes_table(idx, kind)
    for lang in sorted(table):
        print(f"-- lang={lang} kind={kind} --")
        for change in sorted(table[lang]):
            parts = []
            for fmt in sorted(table[lang][change]):
                by_dir = table[lang][change][fmt]
                dirs = " ".join(f"{d}={o}" for d, o in sorted(by_dir.items()))
                parts.append(f"{fmt}[{dirs}]")
            print(f"  {change:<14} " + "  ".join(parts))
        print()


def _print_declared_special_entries(expected):
    """Круг 6ter: печатает ВСЕ объявленные "особые" клетки каталога
    (by_lang/by_record) вместе с их "reason" — источник этого раздела
    отчёта ДАННЫЕ (schemas/expected.json), а не список в коде. Печатается
    ВСЕГДА, независимо от findings: это не "находки", а справка о том,
    что каталог уже объясняет заранее."""
    entries = collect_reasoned_entries(expected)
    if not entries:
        return
    print("== ОБЪЯВЛЕННЫЕ ОСОБЫЕ КЛЕТКИ КАТАЛОГА (schemas/expected.json) ==")
    for change, fmt, direction, entry in sorted(entries):
        shape = "by_lang" if "by_lang" in entry else "by_record"
        print(f"  {fmt}/{change}/{direction} [{shape}]")
        if shape == "by_lang":
            for lang, val in sorted(entry["by_lang"].items()):
                print(f"    {lang}: {val}")
        else:
            for ri, val in sorted(entry["by_record"].items(), key=lambda kv: int(kv[0])):
                print(f"    запись {ri}: {val}")
        print(f"    причина: {entry.get('reason', '(не указана)')}")
    print()


def _print_report(env, rows, findings):
    print("== ось эволюции схемы: изменение x плечо x направление ==")
    print(f"go: {env.get('go')}")
    print(f"java: {env.get('java')}")
    print(f"строк в фикстуре: {len(rows)}"
          + (f" (объявлено в шапке: {env['expected_lines']})" if "expected_lines" in env else ""))
    print()

    idx = _index(rows)
    for kind in KNOWN_KINDS:
        if any(kind in kinds for kinds in idx.values()):
            _print_table(idx, kind)

    # Расщепление клетки печатается ВСЕГДА, отдельно и заметно (пункт 3
    # docstring) — независимо от findings: это описание свойства данных,
    # а не приговор.
    splits = _cell_split_findings(idx)
    if splits:
        print("== РАСЩЕПЛЕНИЕ КЛЕТКИ (устойчивость зависит от данных) ==")
        for f in splits:
            print(f"  {f['lang']}/{f['op']}/{f['format']}/{f['change']}/{f['direction']}: "
                  f"{f['outcomes_by_record']}")
        print()

    by_kind = {}
    for f in findings:
        by_kind.setdefault(f["kind"], []).append(f)

    if not findings:
        print("Свежих находок нет: наблюдение полностью совпало с "
              "schemas/expected.json (kind=compat, включая объявленные "
              "by_lang/by_record — см. раздел выше), все wrong несут "
              "наблюдаемое значение, Go и Java совпали построчно по всем "
              "полям контракта (за вычетом объявленных расхождений), "
              "каждая клетка полна (5 записей 0..4), общее число строк "
              "совпало с объявленным в шапке.")
        return

    if "expected_mismatch" in by_kind:
        print("== РАСХОЖДЕНИЕ С schemas/expected.json (находка, не сбой прогона) ==")
        for f in by_kind["expected_mismatch"]:
            print(f"  клетка {f['format']}/{f['change']}/{f['direction']} "
                  f"[{f['lang']}, запись {f['record_index']}]: "
                  f"ожидали {f['expected']!r}, получили {f['observed']!r}"
                  + (f" (ошибка: {f['error']})" if f.get("error") else ""))
        print()

    if "missing_cell" in by_kind:
        print("== КЛЕТКА ИЗ expected.json ОТСУТСТВУЕТ В ФИКСТУРЕ ==")
        for f in by_kind["missing_cell"]:
            print(f"  {f['lang']}/{f['op']}/{f['format']}/{f['change']}/{f['direction']} "
                  f"(ожидание было {f['expected']!r})")
        print()

    if "unexpected_cell" in by_kind:
        print("== КЛЕТКА ФИКСТУРЫ ОТСУТСТВУЕТ В schemas/expected.json ==")
        for f in by_kind["unexpected_cell"]:
            print(f"  {f['lang']}/{f['format']}/{f['change']}/{f['direction']} "
                  f"— в фикстуре есть, в каталоге ожиданий нет")
        print()

    if "expected_entry_incomplete" in by_kind:
        print("== ЗАПИСЬ КАТАЛОГА НЕ ПОКРЫВАЕТ ЭТОТ ЯЗЫК/ЗАПИСЬ ==")
        for f in by_kind["expected_entry_incomplete"]:
            print(f"  {f['lang']}/{f['format']}/{f['change']}/{f['direction']}/{f['record_index']}: "
                  f"структура by_lang/by_record объявлена, но не покрывает эту координату")
        print()

    if "wrong_without_observation" in by_kind:
        print("== wrong БЕЗ НАБЛЮДАЕМОГО ЗНАЧЕНИЯ (недоказанная находка) ==")
        for f in by_kind["wrong_without_observation"]:
            print(f"  {f['lang']}/{f['op']}/{f['format']}/{f['change']}/{f['direction']}"
                  f"/{f['record_index']}")
        print()

    if "cross_lang_mismatch" in by_kind:
        print("== МЕЖЪЯЗЫКОВОЕ РАСХОЖДЕНИЕ (Critical) ==")
        for f in by_kind["cross_lang_mismatch"]:
            print(f"  {f['op']}/{f['format']}/{f['change']}/{f['direction']}/{f['record_index']} "
                  f"поле {f['field']!r}: {f['values']}")
        print()

    if "missing_lang_row" in by_kind:
        print("== СТРОКА ЕСТЬ НЕ У ВСЕХ РЕАЛИЗАЦИЙ ==")
        for f in by_kind["missing_lang_row"]:
            print(f"  {f['op']}/{f['format']}/{f['change']}/{f['direction']}/{f['record_index']}: "
                  f"есть у {f['present_langs']}, нет у {f['missing_langs']}")
        print()

    if "incomplete_cell" in by_kind:
        print("== КЛЕТКА НЕПОЛНА (не 5 записей 0..4) ==")
        for f in by_kind["incomplete_cell"]:
            print(f"  {f['lang']}/{f['op']}/{f['format']}/{f['change']}/{f['direction']}: "
                  f"нет записей {f['missing']}, лишние записи {f['unexpected']}")
        print()

    if "fixture_line_count_mismatch" in by_kind:
        print("== ЧИСЛО СТРОК ФИКСТУРЫ НЕ СОВПАДАЕТ С ШАПКОЙ ==")
        for f in by_kind["fixture_line_count_mismatch"]:
            print(f"  объявлено {f['expected_lines']}, фактически {f['actual_lines']}")
        print()


def main():
    rows, env = load_fixture()
    expected = load_expected()
    findings = find_findings(rows, expected, env)
    _print_declared_special_entries(expected)
    _print_report(env, rows, findings)
    # Требование: любая находка — ненулевой код возврата.
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
