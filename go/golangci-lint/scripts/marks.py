"""Разметка: `// want <линтер>…` — подсаженный дефект, `// noise <линтер>…` — шум.

Разметка — источник истины стенда: находка засчитывается, только если совпали
файл, строка и линтер. `want` — дефект, ради которого написана строка.
`noise` — находка, которую линтер даёт на исправном по смыслу коде (непроверенный
`Close` в defer, путь из переменной); курируемый конфиг обязан её погасить.
Всё, что найдено вне разметки, — «лишнее»; размеченное и не найденное — «пропущено».
"""

import os
import re

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TAG = re.compile(r"//\s*(want|noise|want-next)\s+([a-z0-9_ ]+?)\s*(?=$|//)")
# want-next — метка на строке выше: на строке с //nolint метку ставить нельзя,
# nolintlint принял бы её текст за причину подавления.


def collect(root, base=HERE, tag="want"):
    """{(файл от base, строка, линтер)} по всем .go под root для метки tag."""
    marks = set()
    for d, _, files in os.walk(os.path.join(base, root)):
        for name in sorted(files):
            if not name.endswith(".go"):
                continue
            path = os.path.join(d, name)
            rel = os.path.relpath(path, base)
            with open(path, encoding="utf-8") as f:
                for n, line in enumerate(f, 1):
                    for m in TAG.finditer(line):
                        kind, at = m.group(1), n
                        if kind == "want-next":
                            kind, at = "want", n + 1
                        if kind == tag:
                            for linter in m.group(2).split():
                                marks.add((rel, at, linter))
    return marks


KIND = re.compile(r"//\s*kind\s+(\w+)")


def kinds(root, base=HERE):
    """{(файл, строка): класс места} из меток `// kind <класс>`; без метки — ошибка."""
    out = {}
    for d, _, files in os.walk(os.path.join(base, root)):
        for name in sorted(files):
            if name.endswith(".go"):
                path = os.path.join(d, name)
                with open(path, encoding="utf-8") as f:
                    for n, line in enumerate(f, 1):
                        m = KIND.search(line)
                        if m:
                            out[(os.path.relpath(path, base), n)] = m.group(1)
    return out


def score(issues, want, noise=frozenset()):
    """Сверка находок с разметкой: найдено, пропущено, шум, лишнее."""
    got = {(i["file"], i["line"], i["linter"]) for i in issues}
    return {"found": sorted(got & want), "missed": sorted(want - got), "noise": sorted(got & noise),
            "extra": sorted(got - want - noise)}
