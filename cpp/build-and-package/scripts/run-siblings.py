#!/usr/bin/env python3
"""Снимает fixtures/siblings.jsonl — прогон оси соседей по языкам.

Порядок работы:
  1. разобрать путь назначения ДО первой сборки (ошибка вызова,
     найденная после четырёх сборок и сорока четырёх запусков
     контейнеров, стоит полного прогона оси);
  2. на каждое из четырёх плеч — собрать образ сборки и финальный образ,
     прочитать ИЗ ОБРАЗА метку прогона, лечение и время изменения
     бинарника, снять размеры и разделяемые зависимости;
  3. поднять контейнер и снять полный ответ HTTP на /healthz и тело
     /resolve — на них стоит проверка «это один и тот же сервис»;
  4. отдельно снять время до первого ответа: одиннадцать независимых
     холодных запусков, медиана и разброс;
  5. проверить контракт ПРИ ЗАПИСИ — и построчно, и по прогону целиком.

ПОЧЕМУ КОНТРАКТ ПРОВЕРЯЕТСЯ ПРИ ЗАПИСИ. Строка с уликой кэша (метка,
не совпавшая с тем, что мы передали; время изменения, общее у двух
плеч) не должна вообще оказаться на диске: она выглядит полноценной, а
значит ничего. Проверка над готовой фикстурой поймала бы то же самое
через час, когда пересъёмка стоит ещё один прогон.

ЧЕГО ЗДЕСЬ НЕТ. Здесь не проверяется, СОВПАЛИ ли ответы плеч и какой
получился размер: это выводы, их печатает разбор, сверяясь с
expected.json. Прогон отвечает за то, чтобы записанное было записанным
верно.
"""

import hashlib
import io
import json
import os
import sys
import time
import uuid

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import siblings as siblib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

FIXTURES_DIR = os.path.join(HERE, "fixtures")
DEFAULT_FIXTURE = os.path.join(FIXTURES_DIR, "siblings.jsonl")

REQUIRED = (
    "kind", "variant", "role", "language", "dockerfile", "docker_argv",
    "nonce_passed", "nonce_read_back", "binary_mtime", "treatment",
    "toolchain", "strip_step",
    "image_registry_bytes", "image_registry_method",
    "image_unpacked_bytes", "image_unpacked_method",
    "binary_bytes", "binary_sha256_in_builder", "binary_sha256_in_final",
    "dynamic_deps", "dynamic_dep_count", "elf_type", "elf_has_interpreter",
    "elf_has_symtab", "elf_debug_sections", "elf_section_count",
    "name_resolution",
    "healthz_response", "healthz_response_sha256", "healthz_response_bytes",
    "resolve_outcome", "resolve_reported_variant",
    "resolve_excluded_fields", "resolve_body_compared",
    "first_response_ms", "first_response_min_ms", "first_response_max_ms",
    "spread_ms", "first_response_samples", "build_seconds",
    "external_libraries_declared",
)

# dynamic_deps и external_libraries_declared обязаны присутствовать, но
# пустой список у них — законное значение и даже ожидаемое: у статически
# слинкованного бинарника разделяемых зависимостей нет. Требовать от них
# непустоты значило бы требовать поломки. То же у elf_debug_sections:
# пустой список и есть требуемый результат strip. Что он пуст не оттого,
# что секции не читались, стережёт elf_section_count.
MAY_BE_EMPTY = ("dynamic_deps", "external_libraries_declared",
                "resolve_detail", "elf_debug_sections")

ROLES = ("measured", "control", "reference")


def check_row_contract(row):
    """Строка обязана нести всё, на чём стоят утверждения оси."""
    missing = []
    for field in REQUIRED:
        if field not in row or row[field] is None:
            missing.append(field)
            continue
        if field in MAY_BE_EMPTY:
            continue
        value = row[field]
        if isinstance(value, str) and not value.strip():
            missing.append(field)
        elif isinstance(value, (list, tuple)) and not value:
            missing.append(field)
    if missing:
        raise SystemExit(
            "плечо %r: строка не удовлетворяет контракту, не заполнено %s — "
            "прогон не записан" % (row.get("variant"), ", ".join(missing)))

    name = row["variant"]
    if row["role"] not in ROLES:
        raise SystemExit("плечо %r: неизвестная роль %r. Прогон не записан."
                         % (name, row["role"]))

    # Главная улика против кэша: метка, прочитанная ОБРАТНО из образа,
    # обязана совпасть с той, которую мы передали этой сборке. Не
    # совпала — образ собран не в этом прогоне.
    if row["nonce_read_back"] != row["nonce_passed"]:
        raise SystemExit(
            "плечо %r: метка, прочитанная из образа (%r), не та, что "
            "передана сборке (%r) — образ собран не в этом прогоне. "
            "Прогон не записан."
            % (name, row["nonce_read_back"], row["nonce_passed"]))
    if "STAND_NONCE=%s" % row["nonce_passed"] not in row["docker_argv"]:
        raise SystemExit(
            "плечо %r: метка %r не встречается в команде, которая "
            "выполнялась. Объявление без команды — не улика и даже не "
            "объявление. Прогон не записан." % (name, row["nonce_passed"]))

    if row["dynamic_dep_count"] != len(row["dynamic_deps"]):
        raise SystemExit(
            "плечо %r: dynamic_dep_count=%r расходится со списком из %d "
            "зависимостей — число обязано быть выводом из списка, а не "
            "мнением. Прогон не записан."
            % (name, row["dynamic_dep_count"], len(row["dynamic_deps"])))

    expected_sha = hashlib.sha256(
        row["healthz_response"].encode("utf-8")).hexdigest()
    if row["healthz_response_sha256"] != expected_sha:
        raise SystemExit(
            "плечо %r: сумма ответа /healthz не следует за самим ответом. "
            "Прогон не записан." % name)
    if row["healthz_response_bytes"] != len(
            row["healthz_response"].encode("utf-8")):
        raise SystemExit(
            "плечо %r: длина ответа /healthz не следует за самим ответом. "
            "Прогон не записан." % name)

    # Сервис обязан называть СЕБЯ. Плечи go и go-divergent делят образ, и
    # без явной -e STAND_VARIANT оба назвали бы себя первым именем.
    if row["resolve_reported_variant"] != name:
        raise SystemExit(
            "плечо %r: сервис назвал себя %r — плечи делят образ, и имя "
            "передаётся при запуске. Прогон не записан."
            % (name, row["resolve_reported_variant"]))

    # Лечение читается из образа, и объявленные поля обязаны быть теми,
    # которые назвал сам шаг сборки.
    for field, key in (("toolchain", "TOOLCHAIN"), ("strip_step", "STRIP")):
        named = siblib.treatment_field(row["treatment"], key)
        if named != row[field]:
            raise SystemExit(
                "плечо %r: поле %s (%r) не то, которое назвал шаг сборки "
                "(%r). Прогон не записан." % (name, field, row[field], named))

    # Один и тот же ли файл замеряют улики и таблица. Факты ELF, метка и
    # время читаются в образе СБОРКИ, размер — в ФИНАЛЬНОМ; между ними
    # стоит COPY. Совпадение сумм — единственное, что связывает их.
    if row["binary_sha256_in_builder"] != row["binary_sha256_in_final"]:
        raise SystemExit(
            "плечо %r: сумма бинарника в образе сборки (%s) не та, что в "
            "финальном образе (%s) — улики сняты с одного файла, а байты с "
            "другого. Прогон не записан."
            % (name, row["binary_sha256_in_builder"],
               row["binary_sha256_in_final"]))

    # Шкала, у которой образ меньше своего единственного файла,
    # распакованным объёмом не является — именно так и выглядит
    # безымянная шкала сжатого содержимого.
    if row["image_unpacked_bytes"] < row["binary_bytes"]:
        raise SystemExit(
            "плечо %r: распакованный объём образа (%d) меньше лежащего "
            "внутри бинарника (%d) — величина названа неверно. Прогон не "
            "записан."
            % (name, row["image_unpacked_bytes"], row["binary_bytes"]))

    # Исключённое поле, оставшееся в сравниваемом теле, означает, что
    # сравнение идёт по тому, по чему сравнивать нельзя.
    for field in row["resolve_excluded_fields"]:
        if '"%s"' % field in row["resolve_body_compared"]:
            raise SystemExit(
                "плечо %r: поле %r объявлено исключённым, но осталось в "
                "сравниваемом теле /resolve. Прогон не записан."
                % (name, field))

    spread = round(row["first_response_max_ms"] - row["first_response_min_ms"],
                   1)
    if abs(row["spread_ms"] - spread) > 0.05:
        raise SystemExit(
            "плечо %r: spread_ms=%r не равен max-min (%r). Прогон не записан."
            % (name, row["spread_ms"], spread))
    if not (row["first_response_min_ms"] <= row["first_response_ms"]
            <= row["first_response_max_ms"]):
        raise SystemExit(
            "плечо %r: медиана вне разброса. Прогон не записан." % name)


def check_run_contract(rows):
    """Проверки, которые нельзя сделать над одной строкой.

    Метки прогона и времена изменения бинарников обязаны быть попарно
    различны по всему прогону. Четыре плеча, каждое из которых внутри
    себя безупречно, могли бы оказаться одним плечом, собранным
    четырежды из одного слоя, — и ни одна построчная проверка этого не
    заметила бы.
    """
    for field, human in (("nonce_read_back", "метка прогона"),
                         ("binary_mtime", "время изменения бинарника")):
        seen = {}
        for row in rows:
            value = row[field]
            if value in seen:
                raise SystemExit(
                    "%s %r встречается у двух плеч (%s и %s) — это одна "
                    "сборка под двумя именами. Прогон не записан."
                    % (human, value, seen[value], row["variant"]))
            seen[value] = row["variant"]

    names = [r["variant"] for r in rows]
    if len(set(names)) != len(names):
        raise SystemExit("плечи в прогоне повторяются: %r. Прогон не записан."
                         % names)
    if not any(r["role"] == "control" for r in rows):
        raise SystemExit(
            "в прогоне нет ни одного контроля — проверку тождества сервиса "
            "нечем уронить, и она неотличима от той, что всегда отвечает "
            "«совпало». Прогон не записан.")

    # Ручек у сервиса две, и живой контроль нужен КАЖДОЙ. Контроль,
    # роняющий только сравнение ответов /healthz, оставляет ветку
    # сравнения тела /resolve уронённой лишь мутацией копии фикстуры —
    # то есть без живого замера.
    controls = {r["variant"] for r in rows if r["role"] == "control"}
    for required in ("go-divergent", "go-resolve-divergent"):
        if required not in controls:
            raise SystemExit(
                "в прогоне нет контроля %r — одна из двух веток проверки "
                "тождества остаётся без живого отрицательного контроля. "
                "Прогон не записан." % required)


def resolve_out_path(argv):
    """Куда писать — решается ДО того, как запущена первая сборка."""
    given = argv[0] if argv else DEFAULT_FIXTURE
    out_path = given
    if not os.path.isabs(out_path):
        out_path = os.path.join(FIXTURES_DIR, out_path)
    parent = os.path.dirname(out_path)
    if not os.path.isdir(parent):
        raise SystemExit(
            "каталог назначения %r не существует. Аргумент %r разобран в "
            "%r: относительное имя присоединяется к каталогу фикстур %r. "
            "Сборки НЕ запускались." % (parent, given, out_path, FIXTURES_DIR))
    if not os.access(parent, os.W_OK):
        raise SystemExit(
            "в каталог назначения %r нельзя писать. Сборки НЕ запускались."
            % parent)
    return out_path


def _log(msg):
    print(msg, file=sys.stderr)
    sys.stderr.flush()


def measure_arm(arm, run_stamp):
    name = arm["name"]
    nonce = "%s-%s-%s" % (run_stamp, name, uuid.uuid4().hex[:8])
    _log("  сборка (метка %s)..." % nonce)
    built = siblib.build_arm(arm, nonce)

    nonce_read_back = siblib.read_nonce(built["builder_tag"])
    treatment = siblib.read_treatment(built["builder_tag"])
    binary_mtime = siblib.read_binary_mtime(built["builder_tag"])
    sha_in_builder = siblib.read_binary_sha256_in_builder(built["builder_tag"])

    _log("  запуск для проб /healthz и /resolve...")
    container = siblib.start_container(arm)
    try:
        siblib._wait_for_ready(container)
        host_port = siblib._host_port(container)
        probe = siblib.probe_service(arm, host_port)
    finally:
        siblib.stop_container(container)

    _log("  время до первого ответа (%d холодных запусков)..."
         % siblib.FIRST_RESPONSE_ATTEMPTS)
    timing = siblib.measure_first_response_ms(arm)

    elf = siblib.read_elf_facts(built["builder_tag"])
    binary_bytes, sha_in_final = siblib.binary_size_and_sha256_in_final(
        built["final_tag"])
    row = {
        "kind": "sibling",
        "variant": name,
        "role": arm["role"],
        "language": arm["language"],
        "dockerfile": arm["dockerfile"],
        "docker_argv": built["docker_argv"],
        "build_seconds": built["build_seconds"],
        "nonce_passed": nonce,
        "nonce_read_back": nonce_read_back,
        "binary_mtime": binary_mtime,
        "treatment": treatment,
        "toolchain": siblib.treatment_field(treatment, "TOOLCHAIN"),
        "strip_step": siblib.treatment_field(treatment, "STRIP"),
        "image_registry_bytes": siblib.image_registry_bytes(
            built["final_tag"]),
        "image_registry_method": siblib.IMAGE_REGISTRY_METHOD,
        "image_unpacked_bytes": siblib.image_unpacked_bytes(
            built["final_tag"]),
        "image_unpacked_method": siblib.IMAGE_UNPACKED_METHOD,
        "binary_bytes": binary_bytes,
        "binary_sha256_in_builder": sha_in_builder,
        "binary_sha256_in_final": sha_in_final,
        "name_resolution": arm["name_resolution"],
        "external_libraries_declared": arm["external_libraries_declared"],
    }
    row.update(elf)
    row.update(probe)
    row.update(timing)
    return row


def main():
    os.makedirs(FIXTURES_DIR, exist_ok=True)
    out_path = resolve_out_path(sys.argv[1:])
    _log("писать будем в %s" % out_path)

    run_stamp = str(int(time.time()))
    _log("метка прогона: %s" % run_stamp)

    rows = []
    for arm in siblib.load_spec():
        _log("=== %s (%s, %s) ===" % (arm["name"], arm["language"],
                                      arm["role"]))
        row = measure_arm(arm, run_stamp)
        _log("  бинарник %d Б, образ: сжатое содержимое %d Б, распакованное "
             "%d Б, зависимостей %d"
             % (row["binary_bytes"], row["image_registry_bytes"],
                row["image_unpacked_bytes"], row["dynamic_dep_count"]))
        _log("  /healthz: %r" % row["healthz_response"])
        _log("  время: медиана %.1f ms, разброс %.1f ms"
             % (row["first_response_ms"], row["spread_ms"]))
        rows.append(row)

    for row in rows:
        check_row_contract(row)
    check_run_contract(rows)

    with io.open(out_path, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            handle.write("\n")
        handle.write("COMPLETE\n")
    _log("записано %d строк в %s" % (len(rows), out_path))


if __name__ == "__main__":
    main()
