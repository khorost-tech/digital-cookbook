"""Сводка прогона: величины, которые публикуются, и на чём они сняты.

ЗАЧЕМ ЭТО ЕСТЬ.

Статья утверждала, что все числа совпали на двух платформах. Проверить
это по фикстурам было НЕЛЬЗЯ: платформа в них не записана, а полный
прогон перезаписывает один и тот же набор файлов. Утверждение опиралось
на память автора — ровно то, против чего построен весь стенд.

Теперь каждый прогон оставляет сводку в fixtures/manifests/<платформа>.json.
Сводки коммитятся, и сравнить их может кто угодно.

ПОЧЕМУ НЕ ХЕШИ ФАЙЛОВ.

Фикстуры содержат случайные поля рукопожатия и дампы байт — они РАЗНЫЕ
при каждом прогоне, даже на одной машине. Хеш файла ничего не докажет и
только создаст видимость строгости. Сравнивать надо ровно те величины,
которые попадают в текст.

ЧТО НАМЕРЕННО НЕ СРАВНИВАЕТСЯ.

Строка версии Go содержит имя платформы и обязана различаться. Она
записывается для сведения, но из сравнения исключена — и исключена
явно, а не тихо.
"""

import io
import json
import os
import platform
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURES = os.path.join(HERE, "fixtures")
MANIFESTS = os.path.join(FIXTURES, "manifests")

# Поля, которые обязаны различаться между платформами и потому не
# сравниваются. Список явный: молчаливое исключение превратило бы
# сравнение в фикцию.
NOT_COMPARED = ("go_version",)


def rows(name):
    with io.open(os.path.join(FIXTURES, name + ".jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise SystemExit("%s: фикстура оборвана" % name)
    return [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]


def platform_key():
    return platform.system().lower()


def published_values():
    """Ровно те величины, которые попадают в статью и README."""
    out = {}

    chain = rows("chain")
    out["chain_cells"] = len(chain)
    out["chain_cases"] = len({r["case"] for r in chain})
    out["chain_clients"] = len({r["client"] for r in chain})
    out["chain_outcomes"] = {
        r["case"]: sorted({x["outcome"] for x in chain if x["case"] == r["case"]})[0]
        for r in chain}
    out["distinct_reasons"] = {
        client: len({r["detail"] for r in chain
                     if r["client"] == client and r["outcome"] == "rejected"})
        for client in sorted({r["client"] for r in chain})}

    mutual = rows("mutual")
    out["mutual_rows"] = len(mutual)
    out["mutual_outcomes"] = {
        "%s/%s" % (r["case"], r["client"]): r["outcome"] for r in mutual}

    wire = rows("wire")
    out["wire_bytes"] = {r["client"]: r["bytes_total"] for r in wire}
    out["wire_name_offset"] = {r["client"]: r["server_name_at"] for r in wire}
    out["wire_alpn"] = {r["client"]: r["alpn"] for r in wire}
    out["wire_versions"] = {
        r["client"]: [r["record_version"], r["hello_version"],
                      r["supported_versions"]] for r in wire}

    ech = rows("ech")
    out["ech_supported"] = sum(1 for r in ech if r["supported"])
    out["ech_by_client"] = {r["client"]: r["supported"] for r in ech}
    # Семьи реализаций: клиентов четыре, независимых семей меньше, и это
    # напрямую сужает вывод о совпадении базовой проверки.
    out["tls_stacks"] = {r["client"]: r["tls_stack"] for r in ech}
    out["tls_stack_versions"] = {r["client"]: r["tls_stack_version"]
                                 for r in ech}
    out["distinct_stacks"] = len({r["tls_stack"] for r in ech})

    down = {r["case"]: r for r in rows("downgrade")}
    out["downgrade_mark"] = down["negotiated_12"]["downgrade_tail"]
    out["downgrade_control_has_mark"] = down["negotiated_13"]["downgrade_mark"]
    out["plaintext_certificate"] = {
        case: row["plaintext_certificate_bytes"] for case, row in down.items()}
    out["hidden_record_max"] = max(
        r["length"] for r in down["negotiated_13"]["records"]
        if r["type_name"] == "прикладные данные")

    hand = {}
    for row in rows("handshake"):
        hand.setdefault(row["case"], row)
    out["handshake"] = {
        case: {"messages": row["messages_total"], "flights": row["flights"],
               "composition": row["messages"]}
        for case, row in hand.items()}

    rev = {r["case"]: r["outcome"] for r in rows("revocation")}
    out["revocation"] = rev
    return out


def environment():
    def ask(cmd):
        try:
            done = subprocess.run(cmd, capture_output=True, text=True,
                                  encoding="utf-8", errors="replace")
            return (done.stdout + done.stderr).strip().split("\n")[0]
        except Exception as exc:
            return "не определить: %s" % exc

    return {
        "platform": platform_key(),
        "platform_full": platform.platform(),
        "python": sys.version.split()[0],
        # Строка версии Go содержит имя платформы и ОБЯЗАНА различаться.
        "go_version": ask(["go", "version"]),
    }


def main():
    os.makedirs(MANIFESTS, exist_ok=True)
    manifest = {
        "environment": environment(),
        "not_compared": list(NOT_COMPARED),
        "values": published_values(),
    }
    path = os.path.join(MANIFESTS, platform_key() + ".json")
    with io.open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    print("сводка прогона записана: %s" % path)

    stored = sorted(n for n in os.listdir(MANIFESTS) if n.endswith(".json"))
    print("сводок на месте: %d (%s)" % (len(stored), ", ".join(stored)))
    if len(stored) < 2:
        print("ПОКА ОДНА ПЛАТФОРМА: утверждать о совпадении между "
              "платформами нечем, пока не появится вторая сводка.")


if __name__ == "__main__":
    main()
