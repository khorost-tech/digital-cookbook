"""Три инструмента обязаны установить один и тот же набор.

Проверка блокирующая: не сошлись — замер не запускается. Причина та же,
что в предыдущих стендах, где такая же проверка ловила настоящие
расхождения трижды. Здесь она стережёт другое: если инструменты разрешат
зависимости по-разному, сравнение их скорости потеряет смысл — быстрее
окажется тот, кто сделал меньше.

Код возврата: 0 — сошлись, 2 — разные наборы, 3 — плечо не отработало.
"""

import json
import subprocess
import sys

sys.path.insert(0, "/bench")

import installed  # noqa: E402
import tools  # noqa: E402


def main():
    results = {}
    for tool in tools.TOOLS:
        try:
            tools.prepare(tool)
            tools.install(tool)
            snap = installed.snapshot(tools.venv_path(tool))
        except subprocess.CalledProcessError as exc:
            print(json.dumps({"tool": tool.name, "error": str(exc)[:200]},
                             ensure_ascii=False))
            print(f"CHECK FAIL: плечо {tool.name} не отработало",
                  file=sys.stderr)
            return 3
        results[tool.name] = snap
        print(json.dumps({"tool": tool.name, "count": snap["count"],
                          "sha256": snap["sha256"],
                          "bootstrap": snap["bootstrap"]},
                         ensure_ascii=False, sort_keys=True))

    print()
    hashes = {name: r["sha256"] for name, r in results.items()}
    if len(set(hashes.values())) == 1:
        first = next(iter(results.values()))
        print(f"CHECK OK: три инструмента установили один набор — "
              f"{first['count']} пакетов, хеш {first['sha256']}")
        boots = {name: r["bootstrap"] for name, r in results.items()}
        if len({tuple(v) for v in boots.values()}) != 1:
            print()
            print("  Загрузочные пакеты различаются, и это не ошибка —")
            print("  разница в СОЗДАНИИ окружения, а не в установке:")
            for name in sorted(boots):
                print(f"    {name:<10} {boots[name] or '—'}")
        return 0

    print("CHECK FAIL: инструменты установили РАЗНОЕ", file=sys.stderr)
    sets = {name: set(r["packages"]) for name, r in results.items()}
    base_name = sorted(sets)[0]
    for name in sorted(sets):
        if name == base_name:
            continue
        only_base = sorted(sets[base_name] - sets[name])
        only_other = sorted(sets[name] - sets[base_name])
        print(f"  {base_name} против {name}:", file=sys.stderr)
        for item in only_base[:10]:
            print(f"    только у {base_name}: {item}", file=sys.stderr)
        for item in only_other[:10]:
            print(f"    только у {name}: {item}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
