# -*- coding: utf-8 -*-
"""Разбор фикстуры оси размера (Задача 5): bytes, zstd, schema_bytes.

ЗАЧЕМ КОНТРОЛЬНОЕ ПЛЕЧО. `json` не читает схему вовсе (schemas/spec.md,
§7.1) и поэтому даёт точку отсчёта: `json-schema` кодирует ТЕ ЖЕ БАЙТЫ,
и совпадение размеров — не совпадение, а следствие устройства стенда.
Оба заявления об этом плече — что оно совпадает с json-schema и что его
schema_bytes равен нулю — заявления будущей статьи, и держатся они на
проверке ниже (find_findings), а не на том, что так «должно быть».

ПОЧЕМУ РАСХОЖДЕНИЕ — НАХОДКА, А НЕ ИСКЛЮЧЕНИЕ. Ревью Задачи 4 подтвердило
побайтовое совпадение всех 70 пар строк control/json-schema, то есть на
исправном стенде эта проверка обязана быть зелёной. Если она покраснела —
это не повод уронить скрипт с трассировкой, а сигнал того же порядка, что
и остальной разбор в этом проекте: что-то расходится с ожиданием, и это
надо показать в отчёте, а не спрятать за необработанным исключением.

ПОЧЕМУ СРАВНЕНИЕ ФОРМАТОВ ТРЕБУЕТ КОНТРОЛЯ. «Формат N компактнее» —
утверждение относительно чего-то. Без контрольного плеча в данных точки
отсчёта нет, и сравнение обязано отказаться явно (ControlArmMissing), а
не подставить вместо контроля первый попавшийся формат.

КРУГ РЕВЬЮ 2 — три урока, зашитые сюда прямо кодом, а не только в отчёт:

- I1: отсутствие данных для проверки — это НАХОДКА (no_control_arm), а
  не молчание. Раньше отчёт мог одновременно сказать «сравнение
  недоступно» и «находок нет» — второе утверждалось о данных, которых
  не было. Теперь отсутствие контроля само превращается в находку, и
  эти два сообщения никогда не проговариваются одновременно.
- I2: любая находка — ненулевой код возврата. Раньше проверялось всё
  (вес схемы контроля, побайтовое равенство, отсутствие контроля), но
  ни один провал не долетал до кода возврата — автоматический прогон не
  отличил бы исправный стенд от сломанного.
- M1: в строке фикстуры лежит ДЛИНА пачки, а не её содержимое —
  «совпало побайтово» между реализациями было на самом деле «совпали
  длины». batch_hash (SHA-256 содержимого) делает это утверждение
  проверяемым по-настоящему: круг ревью 2 нашёл недетерминированную
  сборку protobuf именно потому, что ПОСЧИТАЛ хеш и увидел расхождение,
  которое длина не показывала.
"""

import json
import os
import statistics as st
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE", Path(__file__).resolve().parent.parent / "fixtures" / "size.txt"))

# Уровень сжатия, зафиксированный в пробе (см. cmd/probe/main.go:zstdLevel
# и Probe.ZSTD_LEVEL в Java) — сверяется со строкой env фикстуры, чтобы
# несовпадение уровня между сборкой пробы и снятой фикстурой не прошло
# незамеченным.
EXPECTED_ZSTD_LEVEL = 3

# Поля, которые ОБЯЗАНЫ совпадать между реализациями на одной и той же
# координате: они описывают формат и данные, а не библиотеку (в отличие
# от zstd/batch_zstd — см. spec.md §10.3, оговорка о сравнимости).
CROSS_LANG_INVARIANT_FIELDS = ("bytes", "schema_bytes", "schema_file_bytes")

# Круг ревью 3. Побайтовое равенство между реализациями — свойство
# ФОРМАТА, а не универсальное требование: оно проверяется ТОЛЬКО там,
# где формат его гарантирует.
#
# - avro: гарантирует спецификация — Parsing Canonical Form даёт единый
#   двоичный вид схемы, а сама запись пишется позиционно, в порядке
#   полей схемы; порядка, который мог бы разойтись, там просто нет.
# - protobuf: спецификация wire-формата НЕ гарантирует порядок полей на
#   проводе — но при включённом детерминированном кодировании (spec.md
#   §7.4) обе реализации стенда эмпирически дают одинаковые байты
#   (проверено: batch_hash совпадает на всех восьми повторных прогонах
#   и между языками). Это факт о конкретных двух реализациях, а не
#   гарантия формата, но раз он верен — проверяем его тоже, чтобы
#   заметить, если он вдруг перестанет быть верным.
# - json / json-schema: JSON вообще не специфицирует порядок ключей
#   объекта. Одна реализация сортирует по алфавиту (Go, encoding/json),
#   другая сохраняет порядок вставки (Java, Jackson) — обе правы
#   одновременно, и никакая из них не обязана уступать другой.
#   Побайтовое сравнение здесь бессмысленно по определению; вместо него
#   сравнивается СОДЕРЖИМОЕ (batch_content_hash, §10.3.2 spec.md) —
#   результат расшифровки записи обратно, а не байты с провода.
BYTE_CANONICAL_FORMATS = frozenset({"avro", "protobuf"})

COMPLETE_MARKER = "COMPLETE"


class TruncatedFixture(Exception):
    """Фикстура недописана или повреждена: нет маркера COMPLETE в конце.

    Наполовину снятая фикстура хуже отсутствующей — по ней нельзя
    понять, каких плеч не хватает. Разбор такой фикстуры обязан упасть,
    а не вернуть частичный результат (тот же принцип, что у самой пробы,
    см. schemas/spec.md §12)."""


class ControlArmMissing(Exception):
    """В данных нет контрольного плеча (format=json) для языка, для
    которого запрошено сравнение форматов. Сравнивать компактность не с
    чем — заявление «формат N компактнее» без точки отсчёта недействительно."""


def parse_fixture(text):
    """Разбирает текст фикстуры в (rows, env).

    rows — строки kind="size"; env — единственная строка kind="env" с
    версиями обеих реализаций. Комментарии (строки с '#') пропускаются.
    Пустые строки в конце/начале не в счёт — но последняя НЕПУСТАЯ
    строка обязана быть литералом COMPLETE.
    """
    lines = [line for line in text.splitlines() if line.strip() != ""]
    if not lines or lines[-1].strip() != COMPLETE_MARKER:
        raise TruncatedFixture(
            "фикстура повреждена или недописана: последняя строка — не "
            f"{COMPLETE_MARKER!r} (прогон мог оборваться посреди записи)")

    data_lines = [line for line in lines[:-1] if not line.lstrip().startswith("#")]

    rows = []
    env = None
    for line in data_lines:
        obj = json.loads(line)
        kind = obj.get("kind")
        if kind == "env":
            if env is not None:
                raise ValueError("в фикстуре больше одной строки env — должна быть ровно одна")
            env = obj
        elif kind == "size":
            rows.append(obj)
        else:
            raise ValueError(f"неизвестный kind в строке фикстуры: {kind!r}")

    if env is None:
        raise ValueError("в фикстуре нет строки env с версиями библиотек")
    if not rows:
        raise ValueError("в фикстуре нет ни одной строки kind=size — измерять нечего")
    return rows, env


def _index(rows):
    """rows -> {lang: {format: {record_index: row}}}."""
    out = {}
    for r in rows:
        out.setdefault(r["lang"], {}).setdefault(r["format"], {})[r["record_index"]] = r
    return out


def find_findings(rows):
    """Проверки-заявления будущей статьи, а не просто структура данных.

    По каждому языку:
    - no_control_arm: у языка вообще нет строк format=json — сравнивать
      компактность форматов и проверять контроль не с чем. Круг ревью 2,
      находка I1: раньше это молчаливо пропускалось, и отчёт мог тут же
      сказать «находок нет» — про данные, которых не было;
    - control_schema_weight / control_schema_file_weight: у контрольного
      плеча schema_bytes или schema_file_bytes не ноль (обязаны быть —
      читателю схема не нужна вовсе, spec.md §13);
    - control_mismatch: json и json-schema разошлись по bytes на одной и
      той же записи (обязаны быть побайтово равны, см. spec.md §7.1).

    Плюс МЕЖЪЯЗЫКОВЫЕ проверки (не зависят от контроля):
    - cross_lang_mismatch: поле, обязанное совпадать у всех реализаций
      на одной координате (bytes, schema_bytes, schema_file_bytes),
      разошлось;
    - batch_hash_mismatch: БАЙТЫ пачки разошлись между языками — только
      для плеч с гарантией побайтового равенства (avro, protobuf; круг
      ревью 3, см. BYTE_CANONICAL_FORMATS). Для json/json-schema разное
      содержимое одной длины ловит недетерминированную сборку (круг
      ревью 2, находки C3+M1), но у них самих порядок ключей JSON не
      специфицирован — сравнивать байты между ними бессмысленно;
    - batch_content_mismatch: СОДЕРЖИМОЕ пачки (результат расшифровки,
      а не байты с провода) разошлось между языками — для плеч БЕЗ
      гарантии побайтового равенства (json, json-schema). Настоящее
      расхождение здесь по-прежнему красит проверку, а известное
      свойство формата (разный порядок ключей) больше не создаёт ложной
      тревоги.

    Возвращает список находок, а не бросает исключение: находка — это
    результат разбора, а не сбой самого разбора.
    """
    findings = []
    idx = _index(rows)

    for lang, formats in idx.items():
        control = formats.get("json")
        if control is None:
            findings.append({"kind": "no_control_arm", "lang": lang})
            continue
        for record_index, crow in sorted(control.items()):
            if crow.get("schema_bytes", 0) != 0:
                findings.append({
                    "kind": "control_schema_weight",
                    "lang": lang,
                    "record_index": record_index,
                    "schema_bytes": crow["schema_bytes"],
                })
            if crow.get("schema_file_bytes", 0) != 0:
                findings.append({
                    "kind": "control_schema_file_weight",
                    "lang": lang,
                    "record_index": record_index,
                    "schema_file_bytes": crow["schema_file_bytes"],
                })
        schema = formats.get("json-schema")
        if schema is None:
            continue
        for record_index, crow in sorted(control.items()):
            srow = schema.get(record_index)
            if srow is None:
                continue
            if crow["bytes"] != srow["bytes"]:
                findings.append({
                    "kind": "control_mismatch",
                    "lang": lang,
                    "record_index": record_index,
                    "json_bytes": crow["bytes"],
                    "json_schema_bytes": srow["bytes"],
                })

    findings.extend(_cross_lang_findings(idx))
    return findings


def _cross_lang_findings(idx):
    """Межъязыковые проверки: поля, которые ОБЯЗАНЫ совпасть у всех
    реализаций на одной координате (bytes/schema_bytes/schema_file_bytes
    — свойства формата и данных, не библиотеки — см.
    CROSS_LANG_INVARIANT_FIELDS), и пачка — БАЙТАМИ там, где формат
    гарантирует единственный верный порядок (BYTE_CANONICAL_FORMATS), и
    СОДЕРЖИМЫМ там, где нет (круг ревью 3)."""
    findings = []
    langs = sorted(idx)
    if len(langs) < 2:
        return findings  # сравнивать не с чем — не находка, а свойство входа

    formats = sorted({fmt for lang_formats in idx.values() for fmt in lang_formats})
    for fmt in formats:
        # -- поштучные поля --
        record_indices = sorted({
            ri for lang in langs for ri in idx.get(lang, {}).get(fmt, {})
        })
        for ri in record_indices:
            rows_here = {lang: idx[lang][fmt][ri] for lang in langs
                         if ri in idx.get(lang, {}).get(fmt, {})}
            if len(rows_here) < 2:
                continue
            for field in CROSS_LANG_INVARIANT_FIELDS:
                values = {lang: r.get(field) for lang, r in rows_here.items()}
                if len(set(values.values())) > 1:
                    findings.append({
                        "kind": "cross_lang_mismatch",
                        "format": fmt,
                        "record_index": ri,
                        "field": field,
                        "values": values,
                    })

        # -- клеточное поле: пачка. Какое именно поле сравнивать —
        # решает ФОРМАТ (BYTE_CANONICAL_FORMATS), а не догадка: у avro и
        # protobuf побайтовое равенство гарантировано (спецификацией
        # либо — для protobuf — эмпирически проверенным следствием
        # детерминированного кодирования), у json/json-schema — нет, и
        # сравнивать там нужно содержимое, а не байты.
        field, kind = (("batch_hash", "batch_hash_mismatch") if fmt in BYTE_CANONICAL_FORMATS
                       else ("batch_content_hash", "batch_content_mismatch"))
        values_by_lang = {}
        for lang in langs:
            recs = idx.get(lang, {}).get(fmt, {})
            v = _single_value(recs, field)
            if v is not None:
                values_by_lang[lang] = v
        if len(values_by_lang) >= 2 and len(set(values_by_lang.values())) > 1:
            findings.append({
                "kind": kind,
                "format": fmt,
                "values": values_by_lang,
            })
    return findings


def compare_formats(rows):
    """Сравнивает средние bytes/zstd форматов ОТНОСИТЕЛЬНО контроля,
    отдельно по языку.

    Требует контрольного плеча (format=json) для КАЖДОГО языка,
    встречающегося в rows: без него у сравнения нет точки отсчёта, и это
    ControlArmMissing, а не подстановка чего-то вместо контроля.
    """
    idx = _index(rows)
    out = {}
    for lang, formats in idx.items():
        if "json" not in formats:
            raise ControlArmMissing(
                f"нет строк контрольного плеча (format=json) для lang={lang!r} — "
                "сравнение компактности форматов недействительно без точки отсчёта")
        control_bytes = st.mean(r["bytes"] for r in formats["json"].values())
        control_zstd = st.mean(r["zstd"] for r in formats["json"].values())
        control_batch_zstd = _single_value(formats["json"], "batch_zstd")
        per_format = {}
        for fmt, records in formats.items():
            mean_bytes = st.mean(r["bytes"] for r in records.values())
            mean_zstd = st.mean(r["zstd"] for r in records.values())
            schema_bytes_values = {r["schema_bytes"] for r in records.values()}
            schema_file_bytes_values = {r.get("schema_file_bytes") for r in records.values()
                                         if "schema_file_bytes" in r}
            # batch_bytes/batch_zstd/batch_hash — свойства КЛЕТКИ
            # (schemas/spec.md §10.3.2), одно значение на все пять строк,
            # а не среднее по записям. Поле может отсутствовать (старые
            # фикстуры/тесты без пачки) — тогда None, а не KeyError.
            batch_bytes = _single_value(records, "batch_bytes")
            batch_zstd = _single_value(records, "batch_zstd")
            batch_hash = _single_value(records, "batch_hash")
            batch_content_hash = _single_value(records, "batch_content_hash")
            per_format[fmt] = {
                "mean_bytes": mean_bytes,
                "mean_zstd": mean_zstd,
                "schema_bytes": (schema_bytes_values.pop()
                                 if len(schema_bytes_values) == 1 else None),
                "schema_file_bytes": (schema_file_bytes_values.pop()
                                      if len(schema_file_bytes_values) == 1 else None),
                "bytes_ratio_to_control": mean_bytes / control_bytes,
                "zstd_ratio_to_control": mean_zstd / control_zstd,
                "batch_bytes": batch_bytes,
                "batch_zstd": batch_zstd,
                "batch_hash": batch_hash,
                "batch_content_hash": batch_content_hash,
                # Какое из двух полей — свойство равенства между
                # реализациями (BYTE_CANONICAL_FORMATS, круг ревью 3).
                "byte_canonical": fmt in BYTE_CANONICAL_FORMATS,
                "batch_zstd_ratio_to_control": (
                    batch_zstd / control_batch_zstd
                    if batch_zstd is not None and control_batch_zstd else None),
            }
        out[lang] = per_format
    return out


def _single_value(records, field):
    """Достаёт ОДНО значение поля, общее для всех строк клетки (field —
    свойство клетки, а не записи). None, если поля нет вовсе (старые
    данные) или значения внутри клетки почему-то разошлись — расхождение
    не должно тихо подмениться первым попавшимся числом."""
    values = {r[field] for r in records.values() if field in r}
    return values.pop() if len(values) == 1 else None


def load_fixture(path=None):
    path = Path(path) if path else FIXTURE
    return parse_fixture(path.read_text(encoding="utf-8"))


def _print_report(env, findings, comparison, control_missing_note):
    print("== ось размера: bytes / zstd / schema_bytes (каноническая форма) ==")
    print(f"zstd level: {env.get('zstd_level')} "
          f"(ожидали {EXPECTED_ZSTD_LEVEL}"
          f"{'' if env.get('zstd_level') == EXPECTED_ZSTD_LEVEL else ' — РАСХОЖДЕНИЕ'})")
    print(f"go: {env.get('go')}")
    print(f"java: {env.get('java')}")
    print()

    if control_missing_note:
        print(f"Сравнение форматов недоступно: {control_missing_note}")
        print()

    for lang in sorted(comparison):
        print(f"-- lang={lang} --")
        for fmt in ("json", "json-schema", "avro", "protobuf"):
            if fmt not in comparison[lang]:
                continue
            row = comparison[lang][fmt]
            batch = ""
            if row["batch_bytes"] is not None:
                ratio = (f"x{row['batch_zstd_ratio_to_control']:.2f} от контроля"
                         if row["batch_zstd_ratio_to_control"] is not None else "?")
                # Круг ревью 3: печатаем то поле, которое реально служит
                # заявлением о равенстве для ЭТОГО формата — байты там,
                # где формат их гарантирует, содержимое там, где нет.
                equality_label = "batch_hash" if row["byte_canonical"] else "batch_content_hash"
                equality_value = row["batch_hash"] if row["byte_canonical"] else row["batch_content_hash"]
                batch = (f"  batch_bytes={row['batch_bytes']} "
                         f"batch_zstd={row['batch_zstd']} ({ratio})"
                         f"  {equality_label}={equality_value}")
            print(f"  {fmt:<11} bytes~={row['mean_bytes']:.1f} "
                  f"(x{row['bytes_ratio_to_control']:.2f} от контроля)  "
                  f"zstd~={row['mean_zstd']:.1f}  "
                  f"schema_bytes={row['schema_bytes']} (файл: {row['schema_file_bytes']}){batch}")
        print()

    # I1: сообщение «находок нет» печатается ТОЛЬКО когда проверки
    # реально прошли по полным данным. Если контроля не было хотя бы у
    # одного языка, find_findings уже добавил no_control_arm в findings,
    # так что этот блок и «недоступно» выше никогда не расходятся во
    # мнениях о состоянии стенда.
    if findings:
        print("== НАХОДКИ ==")
        for f in findings:
            print(f"  {f}")
    else:
        print("Находок нет: контрольное плечо присутствует у всех языков, "
              "json и json-schema побайтово совпали на всех записях, вес "
              "схемы контроля — 0 везде (канонический и файловый); "
              "avro/protobuf совпали БАЙТАМИ пачки, json/json-schema — "
              "СОДЕРЖИМЫМ пачки (у них нет канонической формы байт — "
              "круг ревью 3, это не проверяется побайтово и не находка).")


def main():
    rows, env = load_fixture()
    findings = find_findings(rows)
    control_missing_note = None
    try:
        comparison = compare_formats(rows)
    except ControlArmMissing as e:
        control_missing_note = str(e)
        comparison = {}
    _print_report(env, findings, comparison, control_missing_note)
    # I2: любая находка — ненулевой код возврата. Без этого автоматический
    # прогон не отличит исправный стенд от сломанного: разбор до этой
    # правки печатал находки и всё равно выходил кодом 0.
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
