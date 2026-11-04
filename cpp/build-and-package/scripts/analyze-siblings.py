#!/usr/bin/env python3
"""Разбор оси соседей по языкам: прогон против записанного до него.

Разбор НЕ содержит ожиданий — они лежат в expected.json, блок
"siblings". Здесь только сверка, печать и защиты, без которых таблицу
печатать нельзя. Порядок защит существен:

  1. КЭШ. Метка прогона, прочитанная ОБРАТНО из образа, обязана совпасть
     с переданной, а метки и времена изменения бинарников всех плеч —
     быть попарно различны. Пока не показано, что каждый образ собран в
     этом прогоне, размеры читать бессмысленно: они могли остаться от
     прошлого.
  2. ОДИН ЛИ ЭТО ФАЙЛ. Факты ELF, метка и время читаются в образе
     СБОРКИ, а байты и размер — в ФИНАЛЬНОМ образе; между ними стоит
     COPY. Суммы, снятые в обоих местах, обязаны совпасть, иначе улики
     относятся к одному файлу, а таблица — к другому.
  3. ЛИНКОВКА И STRIP. У каждого плеча ноль разделяемых зависимостей,
     нет загрузчика, нет таблицы символов и секций .debug_*. Плечо,
     собранное динамически, унесло бы часть своего веса в чужие
     библиотеки; плечо с оставшейся отладочной информацией несло бы
     лишний вес.
  4. ТОЖДЕСТВО СЕРВИСА. Главная защита оси. Измеряемые плечи обязаны
     отдавать ПОБАЙТНО одинаковый полный ответ HTTP на /healthz и
     одинаковое тело /resolve после удаления полей, сравнивать которые
     нельзя. Поле variant обязано при этом различаться — иначе
     «отличается ровно одно» проверялось бы там, где не отличается
     ничего. Поле detail исключено по другой причине: текст ошибки у
     трёх реализаций свой, и его сравнение объявляло бы «это не один
     сервис» при любом отказе разрешения имени. Сам исход /resolve
     проверяется отдельно и своим сообщением.
  5. ОТРИЦАТЕЛЬНЫЕ КОНТРОЛИ — ПО ОДНОМУ НА РУЧКУ. Плечо go-divergent
     обязано провалить сравнение ответов /healthz, плечо
     go-resolve-divergent — сравнение тела /resolve. Без первого
     проверка тождества неотличима от той, что всегда отвечает
     «совпало»; без второго так же неотличима её вторая ветка.

Только после пяти защит печатается таблица.

РАЗМЕР ОБРАЗА — ДВЕ ВЕЛИЧИНЫ, И ОБЕ НАЗВАНЫ. `docker image inspect`
отдаёт объём СЖАТОГО содержимого образа — цену хранения в реестре;
распакованный объём снимается отдельно, docker export'ом, и отвечает на
другой вопрос — цену на диске. Одна безымянная величина здесь стояла бы
вместо обеих, и читатель назвал бы её сам.

ПРАВИЛО ПРО ВРЕМЯ применяется буквально в том виде, в каком записано в
expected.json ДО прогона: если наибольший разброс внутри плеча больше
или равен наибольшей разнице между медианами плеч, утверждение по
времени снимается. Снимается — значит: число остаётся в фикстуре, но в
выводы не идёт ни числом, ни словом, ни намёком на направление.
Записанная формулировка ЧИТАЕТСЯ отсюда и сверяется: связь кода с
предрегистрацией держится проверкой, а не добросовестностью.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements
from scripts.console import force_utf8  # noqa: E402

# Потоки переключаются в UTF-8 первым делом: на консоли cp1251
# печать «≠» роняла разбор с UnicodeEncodeError. Найдено прогоном
# на втором хосте.
force_utf8()

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.environ.get(
    "SIBLINGS_FIXTURE", os.path.join(HERE, "fixtures", "siblings.jsonl"))
FIXTURE_RUN1 = os.environ.get(
    "SIBLINGS_FIXTURE_RUN1",
    os.path.join(HERE, "fixtures", "siblings.run-1.jsonl"))

MEASURED = ("cpp", "go", "rust")
CONTROLS = ("go-divergent", "go-resolve-divergent")
# Плечо ВНЕ оси: тот же сервис на C++ без fmt. В таблицу и в кратности
# не идёт; его число справочное и отвечает ровно на один вопрос — во
# сколько байтов обошлась библиотека, которой у соседей нет.
REFERENCE = ("cpp-nofmt",)
# Пары, на которых держатся отрицательные контроли: плечо и его
# намеренно разошедшийся двойник, по одному на каждую ручку сервиса.
DIVERGENT_PAIR = ("go", "go-divergent")
RESOLVE_DIVERGENT_PAIR = ("go", "go-resolve-divergent")

# Формулировка правила про время, которую применяет КОД. Записанная до
# прогона обязана содержать её дословно: иначе применяется не то
# правило, которое предрегистрировано.
TIME_RULE_LITERAL = "больше или равен"


def load_expected():
    with io.open(os.path.join(HERE, "expected.json"), encoding="utf-8") as fh:
        return json.loads(fh.read())["siblings"]


def rows_of(path=None):
    return {r["variant"]: r for r in read_measurements(path or FIXTURE)}


def _stop(title, lines):
    print(title)
    print()
    for line in lines:
        print("  %s" % line)
    raise SystemExit(title)


def _where(label):
    return (" (%s)" % label) if label else ""


def check_cache_did_not_measure_itself(rows, label=""):
    """Каждый образ обязан быть собран в этом прогоне."""
    bad = []
    for name, row in sorted(rows.items()):
        if row["nonce_read_back"] != row["nonce_passed"]:
            bad.append("%s: из образа прочитано %r, передано %r"
                       % (name, row["nonce_read_back"], row["nonce_passed"]))
    for field, human in (("nonce_read_back", "метка прогона"),
                         ("binary_mtime", "время изменения бинарника")):
        seen = {}
        for name, row in sorted(rows.items()):
            value = row[field]
            if value in seen:
                bad.append("%s %r общее у плеч %s и %s"
                           % (human, value, seen[value], name))
            seen[value] = name
    if bad:
        _stop("ОБРАЗ СОБРАН НЕ В ЭТОМ ПРОГОНЕ%s — ТАБЛИЦЫ НЕТ." % _where(label),
              ["Размеры, снятые с образа из кэша слоёв, относятся к другой",
               "сборке, и сравнивать их не с чем."] + [""] + bad)


def check_the_binary_is_the_same_file(rows, label=""):
    """Улики и байты обязаны относиться к одному файлу.

    Метка прогона, время изменения и факты ELF читаются в образе
    СБОРКИ; размер и сумма — в ФИНАЛЬНОМ образе, куда файл попадает
    через COPY. Совпадение сумм — единственное, что связывает две
    половины замера; без него «ноль зависимостей» и «столько-то байтов»
    могли бы относиться к разным файлам.
    """
    bad = []
    for name, row in sorted(rows.items()):
        if row["binary_sha256_in_builder"] != row["binary_sha256_in_final"]:
            bad.append("%s: в образе сборки %s, в финальном %s"
                       % (name, row["binary_sha256_in_builder"][:16],
                          row["binary_sha256_in_final"][:16]))
    if bad:
        _stop("УЛИКИ И БАЙТЫ СНЯТЫ С РАЗНЫХ ФАЙЛОВ%s — ТАБЛИЦЫ НЕТ."
              % _where(label),
              ["Записи NEEDED, метка прогона и время изменения читаются в",
               "образе сборки, размер — в финальном. Если суммы разошлись,",
               "это два разных файла, и таблица описывает не то, что",
               "проверено уликами."] + [""] + bad)


def check_every_arm_is_static(rows, label=""):
    """Ноль зависимостей, никакого загрузчика, никакой отладочной информации.

    Меряется по самому бинарнику: записи NEEDED динамической секции,
    наличие PT_INTERP и список секций. Ответ ldd для первого не годится
    — у статического PIE (так собирает Rust на musl по умолчанию) он
    печатает путь загрузчика, и разбор его текста дал бы «одна
    зависимость» там, где зависимостей нет.

    Секции добавлены потому, что шаг strip у плеч выполняется РАЗНЫМИ
    инструментами: strip из binutils у C++ и Rust, ключи линковщика
    -s -w у Go. Поле strip_step фиксирует объявление шага; здесь
    проверяется результат.
    """
    bad = []
    for name, row in sorted(rows.items()):
        if row["dynamic_dep_count"] != 0:
            bad.append("%s: %d записей NEEDED (%s)"
                       % (name, row["dynamic_dep_count"],
                          ", ".join(row["dynamic_deps"])))
        if row["elf_has_interpreter"]:
            bad.append("%s: у бинарника есть PT_INTERP — он ждёт загрузчик, "
                       "которого в scratch нет" % name)
        if row["elf_section_count"] <= 0:
            bad.append("%s: не прочитано ни одной секции — «отладочных "
                       "секций нет» тут ничего не значит" % name)
        if row["elf_has_symtab"]:
            bad.append("%s: осталась таблица символов .symtab" % name)
        if row["elf_debug_sections"]:
            bad.append("%s: остались отладочные секции %s"
                       % (name, ", ".join(row["elf_debug_sections"])))
    if bad:
        _stop("ПЛЕЧО СОБРАНО НЕ САМОДОСТАТОЧНО%s — РАЗМЕРЫ НЕСРАВНИМЫ."
              % _where(label),
              ["Часть веса такого плеча лежит в чужих библиотеках вне",
               "образа либо в отладочной информации, которую у остальных",
               "убрали, и его байты не сопоставимы с байтами остальных."]
              + [""] + bad)


def check_the_service_is_the_same(rows, label=""):
    """Главная защита: сравниваются три реализации ОДНОГО сервиса."""
    # Отказ разрешения имени — не расхождение реализаций, и путать эти
    # два случая нельзя. Пока текст ошибки входил в сравниваемое тело,
    # любой отказ DNS печатал «это не один сервис» по причине, к
    # тождеству отношения не имеющей.
    refused = ["%s: исход %r, detail %r"
               % (name, rows[name]["resolve_outcome"],
                  rows[name]["resolve_detail"])
               for name in sorted(rows)
               if rows[name]["resolve_outcome"] != "resolved"]
    if refused:
        _stop("РАЗРЕШЕНИЕ ИМЕНИ НЕ ПРОШЛО%s — ЭТО НЕ ПРО ЯЗЫКИ."
              % _where(label),
              ["Плечи обязаны делать одно и то же, а одно из них не",
               "разрешило имя. Это сеть или окружение прогона, а не",
               "расхождение реализаций: сравнивать размеры всё равно",
               "нельзя, но и «это не один сервис» здесь сказать не о чем."]
              + [""] + refused)

    reference = MEASURED[0]
    bad = []
    for name in MEASURED[1:]:
        if rows[name]["healthz_response_sha256"] != \
                rows[reference]["healthz_response_sha256"]:
            bad.append(
                "%s и %s отвечают на /healthz по-разному:\n      %r\n      %r"
                % (reference, name, rows[reference]["healthz_response"],
                   rows[name]["healthz_response"]))
        if rows[name]["resolve_body_compared"] != \
                rows[reference]["resolve_body_compared"]:
            bad.append(
                "%s и %s отвечают на /resolve по-разному:\n      %s\n      %s"
                % (reference, name,
                   rows[reference]["resolve_body_compared"],
                   rows[name]["resolve_body_compared"]))
    # Поле variant объявлено исключением потому, что ОБЯЗАНО различаться.
    # Иначе «различается ровно одно» проверено на данных, где не
    # различается ничего.
    reported = [rows[n]["resolve_reported_variant"] for n in MEASURED]
    if len(set(reported)) != len(reported):
        bad.append("исключённое поле variant не различает плечи: %r — "
                   "тождество проверено там, где различий нет вовсе"
                   % (reported,))
    if bad:
        _stop("ЭТО НЕ ОДИН СЕРВИС НА ТРЁХ ЯЗЫКАХ%s — СРАВНИВАТЬ РАЗМЕРЫ "
              "НЕЛЬЗЯ." % _where(label),
              ["Размер бинарника, у которого другое поведение, не",
               "сравним ни с чем. Ось недействительна."] + [""] + bad)

    # Справочное плечо стоит вне оси, но разность его байтов и байтов
    # плеча cpp что-то значит только пока это тот же сервис.
    for name in REFERENCE:
        if name not in rows:
            continue
        if rows[name]["healthz_response_sha256"] != \
                rows["cpp"]["healthz_response_sha256"] or \
                rows[name]["resolve_body_compared"] != \
                rows["cpp"]["resolve_body_compared"]:
            _stop("СПРАВОЧНОЕ ПЛЕЧО %s — ДРУГОЙ СЕРВИС%s."
                  % (name, _where(label)),
                  ["Разность его байтов и байтов плеча cpp назначена мерить",
                   "цену библиотеки fmt. Если плечи отвечают по-разному,",
                   "она меряет разницу двух программ, и справочное число",
                   "печатать нельзя."])


def check_the_controls_diverge(rows, label=""):
    """Каждая ручка обязана иметь свой живой отрицательный контроль."""
    base, healthz_control = DIVERGENT_PAIR
    _, resolve_control = RESOLVE_DIVERGENT_PAIR
    for control in (healthz_control, resolve_control):
        if control not in rows:
            _stop("ОТРИЦАТЕЛЬНОГО КОНТРОЛЯ %s НЕТ%s — ПРОВЕРКА ТОЖДЕСТВА "
                  "НИЧЕГО НЕ ЗНАЧИТ." % (control, _where(label)),
                  ["Без плеча, которое обязано разойтись, соответствующая",
                   "ветка проверки тождества неотличима от той, что всегда",
                   "отвечает «совпало»."])

    if rows[healthz_control]["healthz_response_sha256"] == \
            rows[base]["healthz_response_sha256"]:
        _stop("ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ СОШЁЛСЯ%s — ПРОВЕРКА ТОЖДЕСТВА СЛЕПА."
              % _where(label),
              ["Плечо %s собрано с намеренно другим телом /healthz и"
               % healthz_control,
               "обязано разойтись с %s. Совпадение означает, что проверка"
               % base,
               "тождества отвечает «совпало» при любых данных, и её",
               "зелёный цвет на измеряемых плечах ничего не доказывает.",
               "Ось недействительна целиком."])

    if rows[resolve_control]["resolve_body_compared"] == \
            rows[base]["resolve_body_compared"]:
        _stop("ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ СОШЁЛСЯ%s — ПРОВЕРКА ТОЖДЕСТВА СЛЕПА."
              % _where(label),
              ["Плечо %s собрано с намеренно другим полем probe в"
               % resolve_control,
               "теле /resolve и обязано разойтись с %s по СРАВНИВАЕМОМУ"
               % base,
               "телу. Совпадение означает, что вторая ветка проверки",
               "тождества отвечает «совпало» при любых данных.",
               "Ось недействительна целиком."])
    # Контроль ветки /resolve обязан быть контролем ИМЕННО этой ветки:
    # разойдись он ещё и по /healthz, он падал бы на первой, а вторая
    # осталась бы непроверенной живым замером.
    if rows[resolve_control]["healthz_response_sha256"] != \
            rows[base]["healthz_response_sha256"]:
        _stop("КОНТРОЛЬ ВЕТКИ /resolve РАЗОШЁЛСЯ И ПО /healthz%s."
              % _where(label),
              ["Он обязан ронять ровно ту ветку, ради которой заведён.",
               "Разойдясь по обеим, он падал бы на первой, и сравнение",
               "тела /resolve снова осталось бы без живого контроля."])


def _ratio(bigger, smaller):
    return float(bigger) / float(smaller)


def _ratios(rows, field, title, note, pointer=None):
    print()
    print(title)
    smallest = min(MEASURED, key=lambda n: rows[n][field])
    for name in MEASURED:
        if name == smallest:
            continue
        value = _ratio(rows[name][field], rows[smallest][field])
        # Оба знака печатает КОД. Десятые идут в прозу, третий знак
        # напечатан рядом, чтобы было видно, откуда взялось округление
        # и насколько оно свободно: устойчив он только внутри одного
        # прогона на одной машине.
        print("  %s / %s = %.1f (посчитано %.3f)"
              % (name, smallest, value, value))
    print("  Наименьший — %s." % smallest)
    print("  %s" % note)
    # Блок вырезают из статьи отдельно от справочного числа ниже — этот
    # указатель обязан ехать вместе с самой кратностью, а не только жить
    # в разделе, до которого вырезка может не дойти.
    if pointer:
        print("  %s" % pointer)


def print_table(rows):
    print("ОСЬ 4: СОСЕДИ ПО ЯЗЫКАМ — тот же сервис на C++, Go и Rust")
    print()
    print("Все три плеча упакованы одинаково: статический бинарник в")
    print("scratch, ноль разделяемых зависимостей, один и тот же ответ на")
    print("/healthz побайтно.")
    print()
    print("У РАЗМЕРА ОБРАЗА ЗДЕСЬ ДВЕ ВЕЛИЧИНЫ, И ОНИ ОТВЕЧАЮТ НА РАЗНЫЕ")
    print("ВОПРОСЫ. «Сжатое» — то, что отдаёт docker image inspect: объём")
    print("сжатого содержимого образа плюс конфиг, то есть цена хранения и")
    print("передачи в реестре. «Распакованное» — сумма размеров файлов во")
    print("всех слоях образа (docker image save, слои распакованы по")
    print("manifest.json), то есть цена на диске. Первая величина у этих")
    print("образов МЕНЬШЕ лежащего внутри бинарника; сжимаемость у трёх")
    print("языков разная, и поэтому кратности по двум шкалам не совпадают.")
    print()
    print("  %-22s %-6s %13s %13s %13s %5s"
          % ("плечо", "язык", "бинарник, Б", "образ сжат, Б",
             "образ распак, Б", "деп."))
    for name in MEASURED:
        row = rows[name]
        print("  %-22s %-6s %13d %13d %13d %5d"
              % (name, row["language"], row["binary_bytes"],
                 row["image_registry_bytes"], row["image_unpacked_bytes"],
                 row["dynamic_dep_count"]))
    for name in CONTROLS:
        row = rows[name]
        print("  %-22s %-6s %13d %13d %13d %5d  (контроль)"
              % (name, row["language"], row["binary_bytes"],
                 row["image_registry_bytes"], row["image_unpacked_bytes"],
                 row["dynamic_dep_count"]))
    for name in REFERENCE:
        row = rows[name]
        print("  %-22s %-6s %13d %13d %13d %5d  (справочно, вне оси)"
              % (name, row["language"], row["binary_bytes"],
                 row["image_registry_bytes"], row["image_unpacked_bytes"],
                 row["dynamic_dep_count"]))
    print()
    print("  Тулчейны: %s"
          % "; ".join("%s — %s" % (n, rows[n]["toolchain"])
                      for n in MEASURED))

    _ratios(rows, "binary_bytes", "КРАТНОСТИ ПО РАЗМЕРУ БИНАРНИКА",
            "В прозу идут десятые: третий знак устойчив только внутри "
            "одного прогона на одной машине.",
            pointer="Справочное число ниже (цена fmt в плече cpp) меняет "
            "чтение этой кратности на противоположное: вырезанная "
            "отдельно, эта таблица читается без него.")
    _ratios(rows, "image_registry_bytes",
            "КРАТНОСТИ ПО ОБРАЗАМ (СЖАТОЕ СОДЕРЖИМОЕ)",
            "Это кратности цены хранения в реестре, а не на диске, и они "
            "несут в себе разную сжимаемость трёх бинарников.")
    _ratios(rows, "image_unpacked_bytes",
            "КРАТНОСТИ ПО ОБРАЗАМ (РАСПАКОВАННОЕ СОДЕРЖИМОЕ)",
            "Образы — scratch с одним файлом и /etc/resolv.conf, поэтому "
            "здесь кратности близки к кратностям по бинарнику.")

    print()
    print("СПРАВОЧНО, ВНЕ ОСИ: ЦЕНА fmt В ПЛЕЧЕ cpp")
    cpp = rows["cpp"]
    nofmt = rows["cpp-nofmt"]
    delta = cpp["binary_bytes"] - nofmt["binary_bytes"]
    print("  Плечо cpp-nofmt — тот же сервис на C++ с форматированием через")
    print("  snprintf: тот же базовый образ, тот же тулчейн, те же флаги,")
    print("  тот же strip, побайтно те же ответы. Его бинарник %d Б против"
          % nofmt["binary_bytes"])
    print("  %d Б у плеча cpp — разность %d Б, то есть %.0f %% байтов плеча"
          % (cpp["binary_bytes"], delta,
             100.0 * delta / cpp["binary_bytes"]))
    print("  cpp приходится на статически слинкованный fmt.")
    print("  Это НЕ строка оси: в таблицу сравнения языков и в кратности")
    print("  плечо не входит (решение R2). Число справочное и относится к")
    print("  ЭТОЙ сборке — не к «цене fmt» вообще и не к «доле библиотеки")
    print("  в языке».")
    print()
    print("  ЧТО ЭТО МЕНЯЕТ В ЧТЕНИИ КРАТНОСТЕЙ. Примесь тут не поправка на")
    print("  проценты: без fmt бинарник C++ оказался бы НАИМЕНЬШИМ из трёх,")
    print("  а не вдвое больше наименьшего.")
    smallest = min(MEASURED, key=lambda n: rows[n]["binary_bytes"])
    print("  С fmt:  cpp / %s = %.1f (%s наименьший)."
          % (smallest, _ratio(cpp["binary_bytes"],
                              rows[smallest]["binary_bytes"]), smallest))
    print("  Без fmt: %s / cpp-nofmt = %.1f (наименьшим было бы плечо на"
          % (smallest, _ratio(rows[smallest]["binary_bytes"],
                              nofmt["binary_bytes"])))
    print("  C++). Поэтому утверждение «на этом сервисе бинарник Rust вышел")
    print("  вдвое меньше бинарника C++» верно ровно для плеча оси — и")
    print("  разворачивается на противоположное, стоит убрать библиотеку,")
    print("  которой у соседей нет. Сравнение остаётся честным только")
    print("  вместе с этим числом.")


def print_size_caveats(rows, expected):
    """Печать раздела «что эти числа не значат».

    Формулировки берутся ИЗ expected.json дословно, а не пересказываются
    здесь: до читателя стенда доезжает эта печать, а не отчёт задачи, и
    пересказ предрегистрации в печати рано или поздно разойдётся с
    предрегистрацией.
    """
    claims = expected["claims_that_will_not_be_made"]
    required = ("about_languages", "about_performance", "about_prevalence",
                "about_the_fmt_confounder", "about_startup_cost")
    missing = [key for key in required if key not in claims]
    if missing:
        _stop("В ПРЕДРЕГИСТРАЦИИ НЕТ ЗАПРЕТОВ, КОТОРЫЕ ПЕЧАТАЕТ РАЗБОР.",
              ["Не найдены: %s." % ", ".join(missing),
               "Печать обязана цитировать записанное, а не пересказывать",
               "его; расхождение здесь значит, что одно из двух правили."])

    print()
    print("ЧТО ЭТИ ЧИСЛА НЕ ЗНАЧАТ")
    print("  Пункты 1-5 — дословно из expected.json, записанного ДО прогона.")
    print()
    print("  1. Ничего про языки.")
    print("     %s" % claims["about_languages"])
    print("  2. Ничего про производительность.")
    print("     %s" % claims["about_performance"])
    print("  3. Ничего про распространённость.")
    print("     %s" % claims["about_prevalence"])
    print("  4. Разница размера не приписывается языку целиком.")
    print("     %s" % claims["about_the_fmt_confounder"])
    print("     Величина этой примеси теперь измерена справочным плечом")
    print("     cpp-nofmt — см. выше; отделяет она fmt в ЭТОЙ сборке, и")
    print("     ничего сверх того.")
    print("  5. Ничего про цену старта.")
    print("     %s" % claims["about_startup_cost"])
    print()
    print("  Пункты 6-9 открылись ПОСЛЕ записи ожиданий и в expected.json")
    print("  не дописаны: файл предрегистрационный.")
    print()
    print("  6. Размер образа — две разные величины, и путать их нельзя.")
    print("     Число, которое отдаёт docker image inspect, у этих образов")
    print("     МЕНЬШЕ лежащего внутри бинарника (%d против %d у cpp): это"
          % (rows["cpp"]["image_registry_bytes"], rows["cpp"]["binary_bytes"]))
    print("     объём СЖАТОГО содержимого, цена хранения в реестре.")
    print("     Распакованный объём снят отдельно (%d Б у cpp) и отвечает"
          % rows["cpp"]["image_unpacked_bytes"])
    print("     на другой вопрос — цену на диске. Кратности по этим двум")
    print("     шкалам не совпадают, потому что сжимаемость у трёх")
    print("     бинарников разная.")
    print("  7. Механизм разрешения имён у плеч НЕ ОДИН И ТОТ ЖЕ.")
    for name in MEASURED:
        print("     %-5s %s" % (name, rows[name]["name_resolution"]))
    print("     Тождество сервиса на оси доказано СОВПАДЕНИЕМ ОТВЕТОВ, а")
    print("     не совпадением работы. Часть веса бинарника Go занимает")
    print("     резолвер, которого у соседей внутри нет, — это разница")
    print("     того же рода, что примесь fmt.")
    print("  8. Ничего про воспроизводимость на другой машине.")
    print("     %s" % _runs_note())
    print("  9. Время сборки плеч (build_seconds) в фикстуре есть, в")
    print("     выводы не идёт: плечи собираются из разных базовых образов")
    print("     и с разным составом шагов, и это была бы величина под")
    print("     одним именем для разных вещей.")


def _runs_note():
    if os.path.exists(FIXTURE_RUN1):
        return ("Ось снята ДВАЖДЫ, но оба раза на одной машине и в одном "
                "окне: это повтор, а не расширение области.")
    return ("Ось снята ОДИН РАЗ, на одной машине, в одном окне. "
            "Устойчивость точных байтов между прогонами не проверена.")


def print_dependency_evidence(rows):
    print()
    print("ЗАВИСИМОСТИ: ЧТО ИЗМЕРЕНО, А ЧТО ОБЪЯВЛЕНО")
    print("  ИЗМЕРЕНО. Число разделяемых зависимостей снято readelf'ом по")
    print("  записям NEEDED в самом бинарнике, плюс проверено отсутствие")
    print("  PT_INTERP. Второе, независимое подтверждение — все плечи")
    print("  работают в scratch, где нет ни загрузчика, ни libc:")
    for name in MEASURED:
        row = rows[name]
        print("    %-6s NEEDED %d, загрузчик %s, тип ELF: %s"
              % (name, row["dynamic_dep_count"],
                 "есть" if row["elf_has_interpreter"] else "нет",
                 row["elf_type"]))
    print("  ИЗМЕРЕНО. Результат strip, а не объявление шага. Инструмент у")
    print("  плеч разный — strip из binutils у C++ и Rust, ключи")
    print("  линковщика -s -w у Go, — поэтому сравнимость держится на том,")
    print("  что осталось в бинарнике, а не на том, что записано в шаге:")
    for name in MEASURED:
        row = rows[name]
        print("    %-6s шаг %-14s секций %2d, .symtab %s, .debug_* %s"
              % (name, row["strip_step"], row["elf_section_count"],
                 "есть" if row["elf_has_symtab"] else "нет",
                 ", ".join(row["elf_debug_sections"]) or "нет"))
    print("  ИЗМЕРЕНО. Отсутствие внешних зависимостей у соседей")
    print("  доказывается тем, что сборка прошла при выключенном доступе")
    print("  к реестрам пакетов — это видно в лечении, прочитанном из")
    print("  образа:")
    for name in ("go", "rust"):
        print("    %-6s %s" % (name, rows[name]["treatment"]))
    print("  ОБЪЯВЛЕНО, а не измерено: содержимое go.mod и Cargo.toml.")
    print("  Их написал автор стенда, и как доказательство они не")
    print("  годятся — поэтому в утверждение идёт сборка без реестра.")

    print()
    print("РАЗРЕШЕНИЕ ИМЁН: ПЛЕЧИ РАБОТАЮТ НЕ ОДИНАКОВО")
    print("  Ответы плеч совпадают побайтно, но механизм у них разный, и")
    print("  тождество сервиса доказано именно совпадением ОТВЕТОВ:")
    for name in MEASURED:
        print("    %-6s %s" % (name, rows[name]["name_resolution"]))
    print("  ОБЪЯВЛЕНО: сама формулировка выше взята из")
    print("  images/sibling-variants.json. ИЗМЕРЕНО другое, и этого")
    print("  достаточно: у плеча go в лечении, прочитанном из образа,")
    print("  стоит CGO_ENABLED=[%s]. При выключенном cgo бинарник не умеет"
          % _cgo(rows["go"]))
    print("  звать функции C вовсе, то есть getaddrinfo той libc, с")
    print("  которой слинкованы соседи, для него недоступен по устройству.")


def _cgo(row):
    from scripts.siblings import treatment_field
    return treatment_field(row["treatment"], "CGO_ENABLED")


def print_time(rows, expected):
    rule = expected["hypothesis_before_run"]["decision_taken_before_the_run"]
    # Связь кода с предрегистрацией: разбор применяет ЗАПИСАННОЕ правило,
    # а не эквивалентное ему. Если записанная формулировка перестала
    # содержать «больше или равен», применять «>=» больше не на
    # основании чего.
    if TIME_RULE_LITERAL not in rule:
        _stop("ПРАВИЛО ПРО ВРЕМЯ В expected.json НЕ ТО, КОТОРОЕ ПРИМЕНЯЕТ "
              "КОД.",
              ["Код сравнивает разброс с разницей как «%s»." % (
                  TIME_RULE_LITERAL,),
               "В записанном до прогона решении этой формулировки нет:",
               rule,
               "Эквивалентное правило вместо записанного — подмена",
               "предрегистрации, и утверждение по времени печатать нельзя."])

    print()
    print("ВРЕМЯ ДО ПЕРВОГО ОТВЕТА")
    times = []
    for name in MEASURED:
        row = rows[name]
        times.append((name, row["first_response_ms"], row["spread_ms"]))
        print("  %-6s медиана %7.1f ms, разброс %5.1f ms (%.1f..%.1f, %d "
              "холодных запусков)"
              % (name, row["first_response_ms"], row["spread_ms"],
                 row["first_response_min_ms"], row["first_response_max_ms"],
                 row["first_response_samples"]))
    medians = [t[1] for t in times]
    between = max(medians) - min(medians)
    within = max(t[2] for t in times)
    print()
    print("  Правило, записанное в expected.json ДО прогона, применяется")
    print("  буквально, в записанной формулировке:")
    print("  %s" % rule)
    print("  Разброс внутри плеча: до %.1f ms. Разница между медианами: "
          "%.1f ms." % (within, between))
    if within >= between:
        print()
        print("  УТВЕРЖДЕНИЕ ПО ВРЕМЕНИ СНЯТО. Разброс перекрывает разницу.")
        print("  Число остаётся в фикстуре и НЕ идёт ни в статью, ни в")
        print("  вывод — ни числом, ни словом «примерно столько же», ни")
        print("  намёком на направление.")
        return False
    print()
    print("  Разброс уже разницы. Это НЕ статистический тест, а ровно то")
    print("  сравнение, которое было записано до прогона; величина")
    print("  покрывает холодный старт контейнера целиком, и долю языка в")
    print("  ней стенд отделять не умеет.")
    return True


def print_hypotheses(rows, expected, time_survived):
    hyp = expected["hypothesis_before_run"]
    print()
    print("ЗАПИСАННОЕ ДО ПРОГОНА — И ЧТО ВЫШЛО")
    order = sorted(MEASURED, key=lambda n: rows[n]["binary_bytes"])
    print("  binary_bytes_order")
    print("    записано: %s" % hyp["binary_bytes_order"])
    print("    вышло: %s. %s"
          % (" < ".join(order),
             "СБЫЛОСЬ." if order == ["rust", "cpp", "go"]
             else "НЕ СБЫЛОСЬ — порядок другой."))
    # Предрегистрация называла величину «размер образа» тогда, когда у
    # неё была одна шкала — та, что отдаёт docker image inspect. По ней
    # гипотеза и проверяется; вторая шкала появилась после записи, и
    # выдавать её за предсказанную нельзя. Порядок по ней печатается
    # рядом как наблюдение.
    image_order = sorted(MEASURED, key=lambda n: rows[n]["image_registry_bytes"])
    unpacked_order = sorted(MEASURED,
                            key=lambda n: rows[n]["image_unpacked_bytes"])
    print("  image_bytes_follows_the_binary")
    print("    записано: %s" % hyp["image_bytes_follows_the_binary"])
    print("    проверяется по СЖАТОМУ содержимому: на момент записи у")
    print("    величины была одна шкала, и это была она.")
    print("    вышло: %s. %s"
          % (" < ".join(image_order),
             "СБЫЛОСЬ." if image_order == order
             else "НЕ СБЫЛОСЬ — порядок образов не тот, что у бинарников."))
    print("    наблюдение сверх записанного (шкала появилась позже):")
    print("    по распакованному содержимому порядок %s."
          % " < ".join(unpacked_order))
    zero = all(rows[n]["dynamic_dep_count"] == 0 for n in MEASURED)
    print("  dynamic_deps_is_zero_everywhere: %s"
          % ("СБЫЛОСЬ." if zero else "НЕ СБЫЛОСЬ."))
    print("    записано: %s" % hyp["dynamic_deps_is_zero_everywhere"])
    print("    ИНСТРУМЕНТ ЗАМЕНЁН. Записанная строка называет ldd; величина")
    print("    снимается записями NEEDED динамической секции (плюс")
    print("    PT_INTERP), потому что ldd над статическим PIE Rust печатает")
    print("    путь загрузчика и засчитал бы его за разделяемую зависимость")
    print("    — «у Rust одна зависимость» было бы просто неверно.")
    print("  healthz_is_identical: СБЫЛОСЬ — иначе разбор не дошёл бы")
    print("    сюда: это защита, а не наблюдение.")
    print("  first_response_ms_will_probably_be_withdrawn: %s"
          % ("СБЫЛОСЬ — утверждение снято." if not time_survived
             else "НЕ СБЫЛОСЬ — разброс оказался уже разницы."))
    print()
    print("  ЧЕСТНОСТЬ ПРЕДРЕГИСТРАЦИИ. Ожидания записаны ДО того, как")
    print("  существовали исходники соседей и измеритель, то есть до")
    print("  любой пробы на собираемость; блока known_from_pre_run_probes")
    print("  в expected.json поэтому нет. Единственное число, известное")
    print("  заранее, помечено там как ПРОШЛЫЕ ДАННЫЕ, а не предсказание:")
    print("  1 022 128 байт у плеча cpp из fixtures/package.jsonl.")
    print("  Замеренное сейчас: %d байт." % rows["cpp"]["binary_bytes"])
    print()
    print("  ЧТО ОТКРЫЛОСЬ ПОСЛЕ ЗАПИСИ и в expected.json НЕ ДОПИСАНО:")
    print("  две шкалы размера образа вместо одной; разный механизм")
    print("  разрешения имён у Go и у соседей; несравнимость поля detail")
    print("  между реализациями. Файл предрегистрационный и правке задним")
    print("  числом не подлежит — всё это названо здесь.")


def print_repeat(rows, run1):
    print()
    print("ПОВТОР В ТОМ ЖЕ ОКНЕ")
    if not run1:
        print("  Второго прогона нет. Утверждать можно только про одно окно")
        print("  замера, и точные байты в статью идти не могут — только")
        print("  порядок.")
        return
    for name in sorted(rows):
        a, b = run1[name], rows[name]
        print("  %-22s бинарник %d и %d — %s"
              % (name, a["binary_bytes"], b["binary_bytes"],
                 "тот же" if a["binary_bytes"] == b["binary_bytes"]
                 else "ДРУГОЙ"))
        for field, human in (("image_registry_bytes", "образ сжатый"),
                             ("image_unpacked_bytes", "образ распак.")):
            print("  %-22s %s %d и %d — %s"
                  % ("", human, a[field], b[field],
                     "тот же" if a[field] == b[field] else "ДРУГОЙ"))
    print("  Оба прогона прошли одни и те же защиты — иначе второй был бы")
    print("  просто напечатан. Это ПОВТОР на той же машине в том же окне,")
    print("  а не расширение области: между прогонами меняются только")
    print("  метка прогона и время.")


def main():
    expected = load_expected()
    rows = rows_of()
    run1 = rows_of(FIXTURE_RUN1) if os.path.exists(FIXTURE_RUN1) else {}

    runs = [("прогон 2", rows)]
    if run1:
        runs.insert(0, ("прогон 1", run1))
    for label, one in runs:
        check_cache_did_not_measure_itself(one, label)
        check_the_binary_is_the_same_file(one, label)
        check_every_arm_is_static(one, label)
        check_the_service_is_the_same(one, label)
        check_the_controls_diverge(one, label)

    print_table(rows)
    print_size_caveats(rows, expected)
    print_dependency_evidence(rows)
    time_survived = print_time(rows, expected)
    print_hypotheses(rows, expected, time_survived)
    print_repeat(rows, run1)
    return 0


if __name__ == "__main__":
    sys.exit(main())
