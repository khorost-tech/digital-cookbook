"""Разбор оси «что видно до шифрования».

Печатает то, что удалось прочитать в первых байтах соединения — до того,
как появился хоть один ключ. Имя берётся ИЗ БАЙТОВ по найденному
смещению; печатать переданное клиенту имя и называть это наблюдением
было бы самообманом.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.wire_parse import parse_client_hello, name_at_offset

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def rows(name="wire.jsonl"):
    out = []
    with io.open(os.path.join(HERE, "fixtures", name),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise SystemExit("фикстура оборвана")
    for line in text.split("\n"):
        line = line.strip()
        if line.startswith("{"):
            out.append(json.loads(line))
    return out


def main():
    data = rows()

    print("ЧТО ВИДНО НА ПРОВОДЕ ДО ВСЯКОГО ШИФРОВАНИЯ")
    print()
    print("Имя прочитано из байтов первого пакета по указанному смещению.")
    print("Каждому клиенту передавалось СВОЁ имя: общее допускало бы")
    print("случайное совпадение.")
    print()
    print("%-9s %6s %-22s %9s" % ("клиент", "байт", "имя из байтов", "смещение"))
    for row in data:
        print("%-9s %6d %-22s %9d"
              % (row["client"], row["bytes_total"],
                 row["server_name_from_bytes"], row["server_name_at"]))

    print()
    print("ВЕРСИИ: ТРИ РАЗНЫХ МЕСТА, И ПЕРВЫЕ ДВА НАМЕРЕННО ЗАНИЖЕНЫ")
    print("(так новое соединение проходит через посредников, знающих")
    print(" только старые версии; настоящий список — в расширении)")
    print()
    print("%-9s %-12s %-12s %s" % ("клиент", "в записи", "в приветствии",
                                   "в расширении"))
    for row in data:
        print("%-9s %-12s %-12s %s"
              % (row["client"], row["record_version"], row["hello_version"],
                 ", ".join(row["supported_versions"]) or "-"))

    print()
    print("ЧТО ЕЩЁ ЕДЕТ ОТКРЫТЫМ ТЕКСТОМ")
    for row in data:
        print("  %-9s протоколы: %-18s расширений: %d"
              % (row["client"],
                 ",".join(row["alpn"]) or "не предложены",
                 len(row["extensions"])))

    print()
    print("РАЗМЕР ПРИВЕТСТВИЯ РАЗЛИЧАЕТСЯ В %.1f РАЗА"
          % (max(r["bytes_total"] for r in data)
             / min(r["bytes_total"] for r in data)))
    print("  Это не про безопасность: столько занимают предложенные наборы")
    print("  шифров, заготовки ключей и прочее содержимое приветствия.")


def ech_and_downgrade():
    print()
    print("ОТВЕТ НА ОТКРЫТОЕ ИМЯ: ШИФРОВАННОЕ ПРИВЕТСТВИЕ")
    print("  Мерится ровно одно — УМЕЕТ ли клиент. Работает ли это на")
    print("  практике, стенд не мерил: нужны записи в системе имён и")
    print("  поддержка на стороне сервера, ни того ни другого здесь нет.")
    print()
    data = rows("ech.jsonl")
    for row in data:
        print("  %-8s %-34s %s" % (row["client"], row["version"][:34],
                                   "умеет" if row["supported"] else "не умеет"))
        print("           как проверяли: %s" % row["how"])
        print("           что увидели:   %s" % str(row["evidence"])[:70])
    print()
    print("  ИТОГ: умеют %d из %d"
          % (sum(1 for r in data if r["supported"]), len(data)))
    print("  Считать по наличию флага было бы завышением: у curl флаг есть,")
    print("  а библиотека его не поддерживает — проверка это ловит только")
    print("  потому, что пробует применить, а не читает справку.")

    print()
    print("ЗАЩИТА ОТ ПОНИЖЕНИЯ ВЕРСИИ — ЭТО ВОСЕМЬ БАЙТ")
    print("  Сервер, который УМЕЕТ новую версию, но договорился на старую,")
    print("  кладёт метку в конец случайного поля своего приветствия.")
    print("  Сервер в обеих пробах ОДИН И ТОТ ЖЕ и всегда способен на 1.3;")
    print("  меняется только потолок у клиента.")
    print()
    for row in rows("downgrade.jsonl"):
        print("  клиент до %-16s согласовано %-8s метка: %s"
              % (row["client_max_version"], row["negotiated_version"],
                 "есть" if row["downgrade_mark"] else "нет"))
        print("           в приветствии сервера написано %s — это поле в"
              % row["server_hello_version"])
        print("           новой версии всегда говорит одно и то же")
        print("           хвост случайного поля: %s" % row["downgrade_tail"])


if __name__ == "__main__":
    main()
    ech_and_downgrade()
