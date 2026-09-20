"""Сводит выводы раннеров в матрицу «проба x клиент».

Вход: файлы вида <клиент>.tsv со строками "<id>\t<OK|FAIL>\t<сообщение>".
Выход: Markdown на stdout.

Прогон считается НЕДЕЙСТВИТЕЛЬНЫМ, если у клиента не сошлись контрольные пробы:
__control_ok должна быть OK, __control_fail — FAIL. Раннер, который не различает
исходы, выдаёт правдоподобную на вид матрицу, не значащую ничего, — такую
публиковать нельзя, поэтому скрипт завершается с ошибкой.
"""

import re
import sys
from pathlib import Path

# UUID репликасета встречается в тексте некоторых сообщений об ошибке и меняется
# при каждом пересоздании кластера. Без нормализации чистый повторный прогон
# оставлял бы недетерминированный diff в RESULTS.md.
#
# Тонкость: раннер обрезает сообщение по длине, и обрезка попадает В СЕРЕДИНУ
# UUID — до RESULTS.md доезжает уже «replicaset ed56d311» без хвоста. Поэтому
# матчим не полный UUID, а привязку к слову «replicaset»: за ним идёт цепочка
# hex-символов и дефисов любой длины (и целый UUID, и его обрезанный префикс).
_UUID_AFTER = re.compile(r"replicaset [0-9a-f][0-9a-f-]{5,}")


def stable(msg: str) -> str:
    return _UUID_AFTER.sub("replicaset <uuid>", msg)

# На Windows стандартный вывод по умолчанию в cp1251, и первая же галочка в
# таблице роняет скрипт с UnicodeEncodeError. Явно переводим вывод в UTF-8.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

CONTROL_OK = "__control_ok"
CONTROL_FAIL = "__control_fail"
SERVICE_PREFIX = "__"


def load(path: Path) -> dict[str, tuple[str, str]]:
    rows: dict[str, tuple[str, str]] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        parts = line.split("\t", 2)
        if len(parts) < 2:
            continue
        rows[parts[0]] = (parts[1], stable(parts[2]) if len(parts) > 2 else "")
    return rows


def main() -> None:
    if len(sys.argv) < 3:
        print("usage: report.py <probes.tsv> <клиент=файл.tsv> ...", file=sys.stderr)
        raise SystemExit(2)

    probes_path = Path(sys.argv[1])
    clients: dict[str, dict[str, tuple[str, str]]] = {}
    for arg in sys.argv[2:]:
        name, _, path = arg.partition("=")
        clients[name] = load(Path(path))

    invalid = []
    for name, rows in clients.items():
        ok = rows.get(CONTROL_OK, ("нет", ""))[0]
        bad = rows.get(CONTROL_FAIL, ("нет", ""))[0]
        if ok != "OK" or bad != "FAIL":
            invalid.append(f"{name}: control_ok={ok}, control_fail={bad}")
    if invalid:
        print("НЕДЕЙСТВИТЕЛЬНЫЙ ПРОГОН — контрольные пробы не сошлись:", file=sys.stderr)
        for line in invalid:
            print("  " + line, file=sys.stderr)
        raise SystemExit(1)

    order: list[tuple[str, str]] = []
    for line in probes_path.read_text(encoding="utf-8").splitlines():
        if not line or line.startswith("#"):
            continue
        parts = line.split("\t", 2)
        if len(parts) >= 2 and not parts[0].startswith(SERVICE_PREFIX):
            order.append((parts[0], parts[1]))

    names = list(clients)
    total_ok = 0

    print("# Матрица принимаемого синтаксиса Picodata\n")
    print("Сгенерировано `report.py` по выводам раннеров. ✅ — конструкция **принята**")
    print("сервером, ❌ — отвергнута. Контрольные пробы у всех клиентов сошлись, иначе")
    print("отчёт не был бы собран.\n")
    print("⚠️ **Эта таблица не проверяет результаты.** Раннеры выполняют запрос и")
    print("смотрят только на факт отказа: «принято» здесь означает «не отвергнуто»,")
    print("а не «даёт тот же ответ, что PostgreSQL». Совпадение результатов и")
    print("постусловия DML проверяются отдельно — `results.sh`, вывод в")
    print("`out/results.txt`.\n")
    print("| Проба | Группа | " + " | ".join(names) + " |")
    print("|---|---|" + "---|" * len(names))

    disagreements: list[str] = []
    for probe_id, group in order:
        cells = []
        statuses = set()
        for name in names:
            status = clients[name].get(probe_id, ("—", ""))[0]
            statuses.add(status)
            cells.append("✅" if status == "OK" else ("❌" if status == "FAIL" else "—"))
        if "OK" in statuses:
            total_ok += 1
        if len(statuses - {"—"}) > 1:
            disagreements.append(probe_id)
        print(f"| `{probe_id}` | {group} | " + " | ".join(cells) + " |")

    print()
    print(f"Всего проб: **{len(order)}**, принято хотя бы одним клиентом: **{total_ok}**.\n")

    print("## Расхождения между клиентами\n")
    if disagreements:
        print("Одна и та же проба принята одними клиентами и отвергнута другими —")
        print("значит дело в драйвере, а не в сервере.\n")
        for probe_id in disagreements:
            print(f"- `{probe_id}`:")
            for name in names:
                status, msg = clients[name].get(probe_id, ("—", ""))
                print(f"  - {name}: {status} {msg}".rstrip())
    else:
        print("Не обнаружены: все клиенты сошлись на каждой пробе. Это и есть главный")
        print("вывод уровня SQL — граница совместимости проходит по серверу, а выбор")
        print("драйвера на неё не влияет.")

    print("\n## Причины отказов\n")
    print("Сообщения сервера по отвергнутым пробам (по первому клиенту в списке).\n")
    first = names[0]
    for probe_id, _group in order:
        status, msg = clients[first].get(probe_id, ("—", ""))
        if status == "FAIL" and msg:
            print(f"- `{probe_id}` — {msg}")


if __name__ == "__main__":
    main()
