# -*- coding: utf-8 -*-
"""Разбор фикстуры оси 3 (Задача 7): что нужно ИМЕТЬ, чтобы прочитать
запись, и конверт реестра схем Apicurio — первая внешняя зависимость
стенда.

ЧТО ПРОВЕРЯЕТ ЭТОТ РАЗБОР (task-7-brief.md, «РЕШЕНИЕ КОНТРОЛЛЕРА»):

  1. НЕДЕЙСТВИТЕЛЬНОСТЬ ПРОБЫ — самая важная и самая громкая проверка.
     Строка kind=need с leg=registry_down несёт schema_available=false
     и НЕ несёт outcome "ok"/"wrong" — таков контракт needprobe при
     погашенном реестре (go/cmd/needprobe/main.go). Если наблюдение
     этому противоречит (schema_available=true, или outcome "ok"/
     "wrong", или сам needprobe уже пометил строку как
     "invalid_probe"), разбор поднимает НАХОДКУ НАИВЫСШЕГО ПРИОРИТЕТА
     (invalid_probe): "докажи недоступностью" не удалось, и объявлять
     что-то про формат на основании этой строки нельзя — симметрично
     python-стенду ("если прочиталось всё — проба недействительна, а не
     находка «схема не нужна»").
  2. Требование 4: у leg=registry_up обязано быть присутствующее и
     НЕНУЛЕВОЕ registry_calls — иначе утверждение "нужно ровно N
     обращений к реестру до первого чтения" ничем не подтверждено.
  3. "wrong" без "got" — та же проверка, что и в оси эволюции
     (scripts/analyze-evolution.py): утверждение "прочиталось неверно"
     обязано нести наблюдаемое значение.
  4. Требование 1 — сравнение вердикта реестра (kind=registry_matrix)
     с колонкой avro/newer_reader матрицы эволюции
     (schemas/expected.json). Расхождение печатается ОТДЕЛЬНЫМ,
     специально размеченным видом находки (registry_matrix_divergence)
     — контроллер прямо предупредил, что расхождение здесь не дефект, а
     возможно лучшая находка оси, и её нельзя путать со сбоем пробы.
     Клетки, вырожденные для Avro (n/a в матрице — reuse_tag), с
     реестром не сравниваются: у матрицы там нет мнения.
  5. Полнота: девять изменений в registry_matrix без пропуска и повтора;
     весь обязательный набор строк kind=need (плечо x формат) наличует.
  6. Оборванная или испорченная посередине фикстура — TruncatedFixture,
     тот же принцип, что и у остальных осей стенда.
  7. Любая находка — ненулевой код возврата (в т.ч. registry_matrix_
     divergence: это НАХОДКА статьи, но её всё равно нужно ЗАМЕТИТЬ —
     тихий прогон, отдавший 0, потерял бы её тем же путём, что и другие
     оси теряли бы expected_mismatch).

ЧЕГО ЭТОТ РАЗБОР НЕ ДЕЛАЕТ. Не поднимает и не гасит реестр (это работа
bench/run-need-schema.sh) и не подгоняет вывод под то, что "должно быть"
— расхождение реестра с матрицей печатается как есть, без готовой
оговорки.
"""

import importlib.util
import json
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURE = Path(os.environ.get("NEED_FIXTURE", ROOT / "fixtures" / "need.txt"))
EVOLUTION_EXPECTED_PATH = Path(os.environ.get(
    "EVOLUTION_EXPECTED", ROOT / "schemas" / "expected.json"))

COMPLETE_MARKER = "COMPLETE"
KNOWN_KINDS = ("registry_matrix", "need", "envelope")

# Девять изменений схемы (без base — см. spec.md §4.3, тот же принцип,
# что у матрицы эволюции).
ALL_CHANGES = ["add_default", "add_nodefault", "remove", "rename", "retype",
               "reuse_tag", "unknown_field", "alias_conflict", "retype_message"]

# Обязательный набор строк kind=need: (format, leg). Список — прямое
# отражение того, что needprobe печатает на живом реестре (main.go,
# stepNeed/stepNeedOther); если сюда добавится новое плечо или новое
# плечо-испытание, эту таблицу придётся продолжить вместе с кодом.
REQUIRED_NEED_ROWS = [
    ("avro", "registry_up"),
    ("avro", "schema_local"),
    ("avro", "registry_down"),
    ("protobuf", "schema_local"),
    ("protobuf", "no_schema"),
    ("json", "no_schema"),
    ("json-schema", "no_schema"),
]

# Загружаем resolve_expected из analyze-evolution.py — то же самое
# правило разворачивания записи expected.json (строка / by_lang /
# by_record), без второй копии, которая неизбежно разойдётся с
# оригиналом (тот же принцип, что и с normalize.go/equal.go в Go-части).
_EVOLUTION_SPEC = importlib.util.spec_from_file_location(
    "analyze_evolution", ROOT / "scripts" / "analyze-evolution.py")
_evolution = importlib.util.module_from_spec(_EVOLUTION_SPEC)
_EVOLUTION_SPEC.loader.exec_module(_evolution)
resolve_expected = _evolution.resolve_expected


class TruncatedFixture(Exception):
    """Фикстура недописана или повреждена посередине — тот же принцип,
    что и в других осях стенда (analyze-evolution.py, analyze-size.py)."""


def parse_fixture(text):
    """Разбирает текст фикстуры need.txt в (rows, env)."""
    lines = [line for line in text.splitlines() if line.strip() != ""]
    if not lines or lines[-1].strip() != COMPLETE_MARKER:
        raise TruncatedFixture(
            "фикстура повреждена или недописана: последняя строка — не "
            f"{COMPLETE_MARKER!r} (прогон мог оборваться посреди шага)")

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
        raise ValueError("в фикстуре нет строки env")
    if not rows:
        raise ValueError("в фикстуре нет ни одной строки данных — измерять нечего")
    return rows, env


def load_fixture(path=None):
    path = Path(path) if path else FIXTURE
    return parse_fixture(path.read_text(encoding="utf-8"))


def load_evolution_expected(path=None):
    path = Path(path) if path else EVOLUTION_EXPECTED_PATH
    return json.loads(path.read_text(encoding="utf-8"))


def _matrix_rows(rows):
    return [r for r in rows if r["kind"] == "registry_matrix"]


def _need_rows(rows):
    return [r for r in rows if r["kind"] == "need"]


def _envelope_rows(rows):
    return [r for r in rows if r["kind"] == "envelope"]


def find_findings(rows, evolution_expected):
    findings = []
    findings.extend(_invalid_probe_findings(rows))
    findings.extend(_registry_calls_findings(rows))
    findings.extend(_wrong_without_observation_findings(rows))
    findings.extend(_completeness_findings(rows))
    findings.extend(_registry_matrix_comparison_findings(rows, evolution_expected))
    return findings


def _invalid_probe_findings(rows):
    """Требование 2 — приоритет №1 всего разбора. Три независимых
    признака недействительности строки leg=registry_down, любой из них
    достаточен:

      - needprobe уже пометил её outcome="invalid_probe" (сам заметил,
        что реестр ответил, хотя должен был быть погашен, main.go);
      - schema_available=true — противоречие само по себе для этого
        плеча, независимо от outcome;
      - outcome в {"ok", "wrong"} — то есть чтение состоялось.

    Одна строка может подпасть под несколько признаков — на выходе одна
    находка на строку, а не по одной на признак, иначе один сбой
    выглядел бы как несколько разных находок."""
    findings = []
    for row in rows:
        if row.get("kind") != "need" or row.get("leg") != "registry_down":
            continue
        reasons = []
        if row.get("outcome") == "invalid_probe":
            reasons.append("needprobe сам объявил строку недействительной")
        if row.get("schema_available") is True:
            reasons.append("schema_available=true при leg=registry_down")
        if row.get("outcome") in ("ok", "wrong"):
            reasons.append(f"outcome={row.get('outcome')!r} — чтение состоялось при погашенном реестре")
        if reasons:
            findings.append({
                "kind": "invalid_probe",
                "format": row.get("format"), "leg": row.get("leg"),
                "reasons": reasons, "row": row,
            })
    return findings


def _registry_calls_findings(rows):
    """Требование 4: leg=registry_up обязано нести ненулевое
    registry_calls — иначе "сколько обращений нужно до первого чтения"
    не измерено, а предположено."""
    findings = []
    for row in rows:
        if row.get("kind") != "need" or row.get("leg") != "registry_up":
            continue
        calls = row.get("registry_calls")
        if calls is None or calls <= 0:
            findings.append({
                "kind": "registry_calls_missing",
                "format": row.get("format"), "registry_calls": calls,
            })
    return findings


def _wrong_without_observation_findings(rows):
    findings = []
    for row in rows:
        if row.get("outcome") == "wrong" and "got" not in row:
            findings.append({
                "kind": "wrong_without_observation",
                "kind_of_row": row.get("kind"),
                "format": row.get("format"), "leg": row.get("leg"),
                "decoder": row.get("decoder"),
            })
    return findings


def _completeness_findings(rows):
    findings = []

    matrix = _matrix_rows(rows)
    seen_changes = [r.get("change") for r in matrix]
    missing = sorted(set(ALL_CHANGES) - set(seen_changes))
    counts = {c: seen_changes.count(c) for c in seen_changes}
    duplicated = sorted(c for c, n in counts.items() if n > 1)
    unexpected = sorted(set(seen_changes) - set(ALL_CHANGES))
    if missing or duplicated or unexpected:
        findings.append({
            "kind": "matrix_incomplete",
            "missing": missing, "duplicated": duplicated, "unexpected": unexpected,
        })

    need = _need_rows(rows)
    present = {(r.get("format"), r.get("leg")) for r in need}
    for fmt, leg in REQUIRED_NEED_ROWS:
        if (fmt, leg) not in present:
            findings.append({"kind": "need_row_missing", "format": fmt, "leg": leg})

    envelope = _envelope_rows(rows)
    if len(envelope) != 1:
        findings.append({"kind": "envelope_row_count", "count": len(envelope)})

    return findings


# Соответствие вердикта реестра и исхода матрицы эволюции — ЧИСТО
# СТРУКТУРНОЕ отображение, а не подгонка: реестр либо принял новую
# версию (что означает "совместимо" с его точки зрения — сравнимо с
# матричными "ok"/"wrong", которые тоже про то, что читатель СМОГ что-то
# прочитать), либо отверг (сравнимо с матричным "refused"), либо не
# смог даже оценить схему (schema_error — у матрицы такого исхода нет
# вовсе, это ТРЕТИЙ ответ, отдельный от join accepted/rejected).
def _registry_side(verdict):
    if verdict == "accepted":
        return "compatible"
    if verdict == "rejected":
        return "incompatible"
    return "schema_error"


def _matrix_side(outcome):
    if outcome in ("ok", "wrong"):
        return "compatible"
    if outcome == "refused":
        return "incompatible"
    return None  # n/a, error — реестру не с чем сравниться


def _registry_matrix_comparison_findings(rows, evolution_expected):
    """Требование 1. Для каждого изменения сравнивает вердикт реестра
    (BACKWARD) с колонкой avro/newer_reader матрицы эволюции — ОТДЕЛЬНО
    по каждому языку, если запись expected.json расходится по языкам
    (by_lang, см. alias_conflict в spec.md §6.2/§15.1): реестр — ТРЕТЬЯ,
    независимая от обеих библиотек реализация проверки совместимости, и
    её вердикт может не совпасть ни с одной из двух."""
    findings = []
    for row in _matrix_rows(rows):
        change = row.get("change")
        entry = evolution_expected.get(change, {}).get("avro", {}).get("newer_reader")
        if entry is None:
            continue
        registry_side = _registry_side(row.get("registry_verdict"))

        langs = ("go", "java") if isinstance(entry, dict) and "by_lang" in entry else (None,)
        for lang in langs:
            matrix_outcome = resolve_expected(entry, lang, 0) if lang else entry
            if not isinstance(matrix_outcome, str):
                continue
            matrix_side = _matrix_side(matrix_outcome)
            if matrix_side is None:
                # n/a (вырожденная пара) или error — у матрицы нет
                # мнения об этой клетке, сравнивать не с чем.
                continue
            if matrix_side != registry_side:
                findings.append({
                    "kind": "registry_matrix_divergence",
                    "change": change,
                    "lang": lang,
                    "matrix_outcome": matrix_outcome,
                    "registry_verdict": row.get("registry_verdict"),
                    "http_status": row.get("http_status"),
                })
    return findings


def _print_registry_matrix_table(rows, evolution_expected):
    print("== требование 1: реестр (BACKWARD) против матрицы эволюции (avro/newer_reader) ==")
    for row in sorted(_matrix_rows(rows), key=lambda r: r["change"]):
        change = row["change"]
        entry = evolution_expected.get(change, {}).get("avro", {}).get("newer_reader")
        if isinstance(entry, dict) and "by_lang" in entry:
            matrix_repr = "/".join(f"{lang}={resolve_expected(entry, lang, 0)}"
                                    for lang in ("go", "java"))
        else:
            matrix_repr = str(entry)
        print(f"  {change:<16} матрица={matrix_repr:<24} реестр={row['registry_verdict']} "
              f"(HTTP {row['http_status']})")
    print()


def _print_report(env, rows, findings, evolution_expected):
    print("== ось 3: что нужно иметь, чтобы прочитать запись ==")
    print(f"go: {env.get('go')}")
    print(f"registry: {env.get('registry')}")
    print(f"строк в фикстуре: {len(rows)}")
    print()

    _print_registry_matrix_table(rows, evolution_expected)

    print("== строки kind=need ==")
    for row in _need_rows(rows):
        print(f"  {row['format']:<12} {row['leg']:<14} schema_available={row['schema_available']!s:<5} "
              f"outcome={row['outcome']:<12} registry_calls={row.get('registry_calls')}")
    print()

    print("== строка kind=envelope (наивный декодер) ==")
    for row in _envelope_rows(rows):
        print(f"  decoder={row['decoder']} outcome={row['outcome']} prefix_len={row['prefix_len']}"
              + (f" got={row.get('got')}" if "got" in row else ""))
    print()

    by_kind = {}
    for f in findings:
        by_kind.setdefault(f["kind"], []).append(f)

    if not findings:
        print("Свежих находок нет: все обязательные строки на месте, leg=registry_down "
              "честно недействителен (schema_available=false, чтение не состоялось), "
              "registry_calls присутствует и ненулевой у registry_up, все \"wrong\" "
              "несут наблюдаемое значение, вердикт реестра совпал с матрицей эволюции "
              "на всех изменениях, где у матрицы есть мнение.")
        return

    if "invalid_probe" in by_kind:
        print("== ПРОБА НЕДЕЙСТВИТЕЛЬНА (высший приоритет — требование 2) ==")
        for f in by_kind["invalid_probe"]:
            print(f"  {f['format']}/{f['leg']}: {', '.join(f['reasons'])}")
        print()

    if "registry_matrix_divergence" in by_kind:
        print("== РАСХОЖДЕНИЕ РЕЕСТРА С МАТРИЦЕЙ ЭВОЛЮЦИИ (находка, не дефект — требование 1) ==")
        for f in by_kind["registry_matrix_divergence"]:
            lang_part = f" [{f['lang']}]" if f["lang"] else ""
            print(f"  {f['change']}{lang_part}: матрица={f['matrix_outcome']!r}, "
                  f"реестр={f['registry_verdict']!r} (HTTP {f['http_status']})")
        print()

    if "registry_calls_missing" in by_kind:
        print("== registry_calls НЕ ПОДТВЕРЖДЁН (требование 4) ==")
        for f in by_kind["registry_calls_missing"]:
            print(f"  {f['format']}/registry_up: registry_calls={f['registry_calls']}")
        print()

    if "wrong_without_observation" in by_kind:
        print("== wrong БЕЗ НАБЛЮДАЕМОГО ЗНАЧЕНИЯ ==")
        for f in by_kind["wrong_without_observation"]:
            print(f"  {f['kind_of_row']}/{f.get('format') or f.get('decoder')}/{f.get('leg', '')}")
        print()

    if "matrix_incomplete" in by_kind:
        print("== МАТРИЦА РЕЕСТРА НЕПОЛНА ==")
        for f in by_kind["matrix_incomplete"]:
            print(f"  пропущены: {f['missing']}, повторены: {f['duplicated']}, лишние: {f['unexpected']}")
        print()

    if "need_row_missing" in by_kind:
        print("== ОБЯЗАТЕЛЬНАЯ СТРОКА kind=need ОТСУТСТВУЕТ ==")
        for f in by_kind["need_row_missing"]:
            print(f"  {f['format']}/{f['leg']}")
        print()

    if "envelope_row_count" in by_kind:
        print("== СТРОК kind=envelope НЕ РОВНО ОДНА ==")
        for f in by_kind["envelope_row_count"]:
            print(f"  count={f['count']}")
        print()


def analyze(fixture_text, evolution_expected):
    """Разбирает уже прочитанный текст фикстуры и возвращает код
    возврата — вынесено отдельно от main() ради тестируемости без
    файловой системы."""
    rows, env = parse_fixture(fixture_text)
    findings = find_findings(rows, evolution_expected)
    _print_report(env, rows, findings, evolution_expected)
    return 1 if findings else 0


def main():
    evolution_expected = load_evolution_expected()
    fixture_text = FIXTURE.read_text(encoding="utf-8")
    return analyze(fixture_text, evolution_expected)


if __name__ == "__main__":
    raise SystemExit(main())
