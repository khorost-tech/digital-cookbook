# -*- coding: utf-8 -*-
"""Разбор фикстуры оси перекрёстного чтения (Задача 8): одна реализация
пишет байты, другая читает их через файл обмена, а не внутри одного
процесса (schemas/spec.md §17).

ЧТО ПРОВЕРЯЕТСЯ (решение контроллера Задачи 8 + §17 spec.md):

  1. Полнота матрицы: у каждой клетки (format, change, direction) ровно
     четыре сочетания писатель/читатель (go/go, go/java, java/go,
     java/java), и у каждого сочетания — ровно пять канонических записей
     0..4 без пропусков и повторов. Общее число строк фикстуры совпадает
     с объявленным в шапке (env.expected_lines).
  2. КОНТРОЛЬНЫЕ КЛЕТКИ (писатель и читатель — один язык) обязаны
     построчно совпасть с тем, что для тех же координат уже даёт ось
     эволюции (fixtures/evolution.txt, kind=compat) — тем же самым
     процессом, только байты идут через файл, а не напрямую. Расхождение
     здесь — Critical (control_mismatch): значит, порчу внёс файловый
     обмен, а не формат (spec.md §17.4).
  3. "wrong" без "got" — как и в оси эволюции, недоказанная находка.
  4. Проба идентичности (kind=identity-probe, по языку): считается
     НЕДЕЙСТВИТЕЛЬНОЙ для формата, если control_equal ложен хоть у одной
     реализации — тогда сравнение байтов между реализациями для этого
     формата не проводится (spec.md §17.6, «проба идентичности
     недействительна без контроля»). Если контроль зелёный у обеих,
     печатается ИТОГОВАЯ строка {"kind":"identity", ...} с cross_equal —
     совпали ли байты МЕЖДУ реализациями (по SHA-256, без физической
     передачи байт — довольно дайджеста).

ФОРМУЛИРОВКИ НЕ СМЕШИВАТЬ (spec.md §17.6). Совпадение байт разных
реализаций доказывается тремя РАЗНЫМИ способами для трёх РАЗНЫХ плеч, и
это разные утверждения:
  - avro — вытекает из спецификации формата (канонический вид схемы плюс
    позиционная запись — порядка, который мог бы разойтись, там просто
    не существует как понятия);
  - protobuf — спецификацией НЕ оговорено, это наблюдение НА ЭТИХ ДВУХ
    КОНКРЕТНЫХ РЕАЛИЗАЦИЯХ при включённом детерминированном кодировании;
  - json / json-schema — совпадения не предполагается вовсе: порядок
    ключей JSON не специфицирован форматом, и обе реализации вправе
    выбрать свой.
Отчёт НАМЕРЕННО не использует слово со значением безусловного обещания
(«со специфического корня») применительно к protobuf: то, что верно
сегодня для двух конкретных реализаций, — не то же самое, что верно для
формата навсегда. Он говорит «подтверждено по спецификации», «подтверждено
на практике для этих реализаций» и «не предполагается вовсе, и это
свойство формата, а не брак».
"""

import json
import os
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "CROSS_FIXTURE", Path(__file__).resolve().parent.parent / "fixtures" / "cross.txt"))
EVOLUTION_FIXTURE = Path(os.environ.get(
    "EVOLUTION_FIXTURE", Path(__file__).resolve().parent.parent / "fixtures" / "evolution.txt"))

COMPLETE_MARKER = "COMPLETE"
KNOWN_KINDS = ("cross", "identity-probe")
EXPECTED_RECORD_INDICES = frozenset(range(5))
WRITER_READER_PAIRS = frozenset({("go", "go"), ("go", "java"), ("java", "go"), ("java", "java")})
CONTROL_PAIRS = frozenset({("go", "go"), ("java", "java")})

# Поля контракта (spec.md §11), которые обязаны совпасть у контрольной
# клетки перекрёстного чтения с той же координатой оси эволюции. "cell"
# и "lang" исключены по построению, "kind" исключён (compat != cross по
# имени, это ожидаемое и единственное различие формы строки), "error" —
# текст диагностики не часть контракта, "stage" не сравнивается: у
# compat стадия может быть "encode" (отказ на кодировании), а у cross
# кодирование уже состоялось на СТОРОНЕ ПИСАТЕЛЯ раньше, поэтому стадия
# приёма — всегда "decode"; сравнивать имеет смысл только реальные
# наблюдения — outcome/bytes/record/want/got.
CONTROL_FIELDS = ("format", "change", "direction", "record_index", "outcome", "bytes", "record", "want", "got")


class TruncatedFixture(Exception):
    """Фикстура недописана или повреждена: нет маркера COMPLETE в конце,
    ИЛИ одна из строк данных — не валидный JSON (испорчена посередине)."""


def parse_fixture(text):
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
        raise ValueError("в фикстуре нет ни одной строки cross/identity-probe — измерять нечего")
    return rows, env


def load_fixture(path=None):
    path = Path(path) if path else FIXTURE
    return parse_fixture(path.read_text(encoding="utf-8"))


def load_evolution_compat_index(path=None):
    """Индекс compat-строк оси эволюции (fixtures/evolution.txt) по
    (lang, format, change, direction, record_index) — только то, с чем
    сверяются контрольные клетки оси перекрёстного чтения. Если файл
    отсутствует, возвращает None: сверка контроля тогда пропускается
    отдельной, явной находкой (evolution_fixture_missing), а не молча."""
    path = Path(path) if path else EVOLUTION_FIXTURE
    if not path.exists():
        return None
    lines = [line for line in path.read_text(encoding="utf-8").splitlines() if line.strip() != ""]
    idx = {}
    for line in lines:
        if line.lstrip().startswith("#") or line.strip() == COMPLETE_MARKER:
            continue
        obj = json.loads(line)
        if obj.get("kind") != "compat":
            continue
        key = (obj["lang"], obj["format"], obj["change"], obj["direction"], obj["record_index"])
        idx[key] = obj
    return idx


def _cross_index(rows):
    """cross-строки -> {(format, change, direction): {(writer, reader): {record_index: row}}}."""
    idx = {}
    for r in rows:
        if r.get("kind") != "cross":
            continue
        cell = (r["format"], r["change"], r["direction"])
        pair = (r["writer"], r["reader"])
        idx.setdefault(cell, {}).setdefault(pair, {})[r["record_index"]] = r
    return idx


def _identity_index(rows):
    """identity-probe строки -> {(format, change): {lang: row}}."""
    idx = {}
    for r in rows:
        if r.get("kind") != "identity-probe":
            continue
        idx.setdefault((r["format"], r["change"]), {})[r["lang"]] = r
    return idx


def find_findings(rows, env=None, evolution_index=None):
    findings = []
    cross_idx = _cross_index(rows)
    findings.extend(_completeness_findings(cross_idx, rows, env))
    findings.extend(_wrong_without_observation_findings(rows))
    findings.extend(_control_findings(cross_idx, evolution_index))
    return findings


def _completeness_findings(cross_idx, rows, env):
    findings = []
    for cell, pairs in cross_idx.items():
        format_, change, direction = cell
        missing_pairs = WRITER_READER_PAIRS - set(pairs)
        if missing_pairs:
            findings.append({
                "kind": "missing_writer_reader_pair", "format": format_, "change": change,
                "direction": direction, "missing": sorted(missing_pairs),
            })
        for pair, records in pairs.items():
            present = set(records)
            if present != EXPECTED_RECORD_INDICES:
                findings.append({
                    "kind": "incomplete_cell", "format": format_, "change": change,
                    "direction": direction, "writer": pair[0], "reader": pair[1],
                    "missing": sorted(EXPECTED_RECORD_INDICES - present),
                    "unexpected": sorted(present - EXPECTED_RECORD_INDICES),
                })

    if env is not None and "expected_lines" in env:
        expected_lines = env["expected_lines"]
        if len(rows) != expected_lines:
            findings.append({
                "kind": "fixture_line_count_mismatch",
                "expected_lines": expected_lines, "actual_lines": len(rows),
            })
    return findings


def _wrong_without_observation_findings(rows):
    findings = []
    for row in rows:
        if row.get("kind") == "cross" and row.get("outcome") == "wrong" and "got" not in row:
            findings.append({
                "kind": "wrong_without_observation",
                "format": row.get("format"), "change": row.get("change"),
                "direction": row.get("direction"), "record_index": row.get("record_index"),
                "writer": row.get("writer"), "reader": row.get("reader"),
            })
    return findings


def _control_findings(cross_idx, evolution_index):
    """Требование 2: контрольные клетки (writer == reader) обязаны
    построчно совпасть с той же координатой оси эволюции (kind=compat,
    тот же lang). Если фикстура эволюции недоступна, сверка не
    выполняется вовсе — явная находка, а не тихий пропуск."""
    findings = []
    if evolution_index is None:
        findings.append({"kind": "evolution_fixture_missing"})
        return findings

    for cell, pairs in cross_idx.items():
        format_, change, direction = cell
        for pair in CONTROL_PAIRS:
            records = pairs.get(pair)
            if not records:
                continue
            lang = pair[0]  # writer == reader на контрольной клетке
            for record_index, row in sorted(records.items()):
                key = (lang, format_, change, direction, record_index)
                ref = evolution_index.get(key)
                if ref is None:
                    findings.append({
                        "kind": "control_reference_missing", "lang": lang, "format": format_,
                        "change": change, "direction": direction, "record_index": record_index,
                    })
                    continue
                for field in CONTROL_FIELDS:
                    if row.get(field) != ref.get(field):
                        findings.append({
                            "kind": "control_mismatch", "lang": lang, "format": format_,
                            "change": change, "direction": direction, "record_index": record_index,
                            "field": field, "cross_value": row.get(field), "evolution_value": ref.get(field),
                        })
    return findings


def identity_summary(rows):
    """Строит итоговую таблицу идентичности по формату: контроль,
    межъязыковое совпадение байт (по SHA-256) и находки недействительности
    (spec.md §17.6). Возвращает (summaries, findings) — summaries — список
    dict в форме интерфейса {"kind":"identity", "format", "control_equal",
    "cross_equal", "sha_go", "sha_java"} (cross_equal и sha_* — None, если
    проба недействительна: сравнивать тогда нечего)."""
    idx = _identity_index(rows)
    summaries = []
    findings = []
    for (format_, change), by_lang in sorted(idx.items()):
        go_row = by_lang.get("go")
        java_row = by_lang.get("java")
        if go_row is None or java_row is None:
            findings.append({
                "kind": "identity_missing_lang", "format": format_, "change": change,
                "present": sorted(by_lang), "missing": sorted({"go", "java"} - set(by_lang)),
            })
            continue

        control_equal = bool(go_row.get("control_equal")) and bool(java_row.get("control_equal"))
        summary = {
            "kind": "identity", "format": format_,
            "control_equal": control_equal,
            "cross_equal": None,
            "sha_go": go_row.get("sha256"), "sha_java": java_row.get("sha256"),
        }
        if not control_equal:
            # Проба недействительна для ЭТОГО формата: одна из реализаций
            # не дала одинаковых байт саму с собой дважды подряд, и
            # межъязыковое сравнение дальше не имеет смысла запускать.
            findings.append({
                "kind": "identity_control_invalid", "format": format_,
                "control_equal_go": bool(go_row.get("control_equal")),
                "control_equal_java": bool(java_row.get("control_equal")),
            })
            summary["cross_equal"] = None
        else:
            summary["cross_equal"] = go_row.get("sha256") == java_row.get("sha256")
        summaries.append(summary)
    return summaries, findings


def _print_identity_report(summaries):
    print("== ПРОБА БАЙТОВОЙ ИДЕНТИЧНОСТИ (контроль обязателен, spec.md §17.6) ==")
    for s in summaries:
        print("  " + json.dumps(s, ensure_ascii=False, sort_keys=True))
        if not s["control_equal"]:
            print(f"    {s['format']}: контроль не пройден — проба идентичности для "
                  "этого плеча недействительна, межъязыковое сравнение не проводится")
            continue
        if s["format"] == "avro":
            note = ("совпадение подтверждено по спецификации формата: Parsing Canonical "
                    "Form плюс позиционная запись не оставляют места для расхождения порядка байт")
        elif s["format"] == "protobuf":
            note = ("совпадение подтверждено на практике для ЭТИХ ДВУХ реализаций при "
                    "включённом детерминированном кодировании — это наблюдение о двух "
                    "конкретных библиотеках на текущих версиях, не обещание формата")
        else:
            note = ("несовпадение ожидаемо и не является дефектом: порядок ключей JSON не "
                    "определён форматом, и обе реализации вправе выбрать свой")
        match_word = "совпали" if s["cross_equal"] else "разошлись"
        print(f"    {s['format']}: байты {match_word} между реализациями (sha_go={s['sha_go'][:12]}…, "
              f"sha_java={s['sha_java'][:12]}…) — {note}")
    print()


def _print_report(env, rows, findings, identity_summaries):
    print("== ось перекрёстного чтения: писатель и читатель из разных реализаций ==")
    print(f"go: {env.get('go')}")
    print(f"java: {env.get('java')}")
    print(f"строк в фикстуре: {len(rows)}"
          + (f" (объявлено в шапке: {env['expected_lines']})" if "expected_lines" in env else ""))
    print()

    _print_identity_report(identity_summaries)

    by_kind = {}
    for f in findings:
        by_kind.setdefault(f["kind"], []).append(f)

    if not findings:
        print("Свежих находок нет: матрица писатель/читатель полна (4 сочетания x 5 "
              "записей на клетку), обе контрольные клетки (свой писатель — свой "
              "читатель) построчно совпали с осью эволюции, все \"wrong\" несут "
              "наблюдаемое значение, общее число строк совпало с объявленным в шапке.")
        return

    if "control_mismatch" in by_kind:
        print("== КОНТРОЛЬНАЯ КЛЕТКА РАСХОДИТСЯ С ОСЬЮ ЭВОЛЮЦИИ (Critical) ==")
        for f in by_kind["control_mismatch"]:
            print(f"  {f['lang']}/{f['format']}/{f['change']}/{f['direction']}/{f['record_index']} "
                  f"поле {f['field']!r}: перекрёстно {f['cross_value']!r}, "
                  f"на оси эволюции {f['evolution_value']!r}")
        print()

    if "control_reference_missing" in by_kind:
        print("== У КОНТРОЛЬНОЙ КЛЕТКИ НЕТ ПАРЫ В ОСИ ЭВОЛЮЦИИ ==")
        for f in by_kind["control_reference_missing"]:
            print(f"  {f['lang']}/{f['format']}/{f['change']}/{f['direction']}/{f['record_index']}: "
                  "координаты не найдены в fixtures/evolution.txt (kind=compat)")
        print()

    if "evolution_fixture_missing" in by_kind:
        print("== ОСЬ ЭВОЛЮЦИИ НЕДОСТУПНА — КОНТРОЛЬ НЕ ПРОВЕРЕН ==")
        print("  fixtures/evolution.txt не найден: сверка контрольных клеток пропущена целиком")
        print()

    if "missing_writer_reader_pair" in by_kind:
        print("== У КЛЕТКИ НЕ ХВАТАЕТ СОЧЕТАНИЯ ПИСАТЕЛЬ/ЧИТАТЕЛЬ ==")
        for f in by_kind["missing_writer_reader_pair"]:
            print(f"  {f['format']}/{f['change']}/{f['direction']}: нет {f['missing']}")
        print()

    if "incomplete_cell" in by_kind:
        print("== КЛЕТКА НЕПОЛНА (не 5 записей 0..4) ==")
        for f in by_kind["incomplete_cell"]:
            print(f"  {f['format']}/{f['change']}/{f['direction']} writer={f['writer']} reader={f['reader']}: "
                  f"нет записей {f['missing']}, лишние {f['unexpected']}")
        print()

    if "wrong_without_observation" in by_kind:
        print("== wrong БЕЗ НАБЛЮДАЕМОГО ЗНАЧЕНИЯ (недоказанная находка) ==")
        for f in by_kind["wrong_without_observation"]:
            print(f"  {f['format']}/{f['change']}/{f['direction']}/{f['record_index']} "
                  f"writer={f['writer']} reader={f['reader']}")
        print()

    if "fixture_line_count_mismatch" in by_kind:
        print("== ЧИСЛО СТРОК ФИКСТУРЫ НЕ СОВПАДАЕТ С ШАПКОЙ ==")
        for f in by_kind["fixture_line_count_mismatch"]:
            print(f"  объявлено {f['expected_lines']}, фактически {f['actual_lines']}")
        print()

    if "identity_control_invalid" in by_kind:
        print("== ПРОБА ИДЕНТИЧНОСТИ НЕДЕЙСТВИТЕЛЬНА (высший приоритет для этого плеча) ==")
        for f in by_kind["identity_control_invalid"]:
            print(f"  {f['format']}: control_equal go={f['control_equal_go']} "
                  f"java={f['control_equal_java']}")
        print()

    if "identity_missing_lang" in by_kind:
        print("== ПРОБЕ ИДЕНТИЧНОСТИ НЕ ХВАТАЕТ СТРОКИ ОДНОГО ИЗ ЯЗЫКОВ ==")
        for f in by_kind["identity_missing_lang"]:
            print(f"  {f['format']}: есть {f['present']}, нет {f['missing']}")
        print()


def main():
    rows, env = load_fixture()
    evolution_index = load_evolution_compat_index()
    identity_summaries, identity_findings = identity_summary(rows)
    findings = find_findings(rows, env, evolution_index) + identity_findings
    _print_report(env, rows, findings, identity_summaries)
    # Требование: любая находка — ненулевой код возврата. Недействительная
    # проба идентичности — тоже находка (высший приоритет по смыслу
    # spec.md §17.6, хотя технически идёт в общий список).
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
