# -*- coding: utf-8 -*-
"""Пересобрать schemas/manifest.json — корень доверия стенда.

Манифест связывает ИМЯ файла с его СОДЕРЖИМЫМ. До него имя клетки
выводилось из имени файла, и копия штатной схемы под чужим именем
переименовывала клетку, не оставляя привычных следов подделки. Теперь
подделка — это правка манифеста, то есть обычный диф, который видно в
ревью.

Запускать после любой правки файлов стенда:

    python scripts/build_manifest.py

и коммитить получившийся manifest.json вместе с самой правкой. Тест
scripts/test_schemas.py сторожит, чтобы про это не забыли.
"""

import hashlib
import io
import json
import os
import re

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "schemas")

# Нотация файла схемы определяется расширением — во всём стенде связь
# однозначна.
NOTATIONS = {".avsc": "avro", ".desc": "protobuf", ".json": "json-schema"}

# Двоичные файлы хешируются как есть; текстовые — с приведёнными концами
# строк, иначе манифест был бы верен ровно на одной операционной системе
# (git отдаёт один и тот же файл то с CRLF, то с LF).
BINARY_EXT = {".desc"}

SCHEMA_NAME = re.compile(r"^user_v(\d+)(?:_(.+))?\.(\w+)$")

# Файлы стенда, которые схемами не являются, но принадлежат ему и обязаны
# быть перечислены: манифест покрывает КАТАЛОГ ЦЕЛИКОМ, чтобы посторонний
# файл рядом со схемами был виден сразу.
NON_SCHEMA_ROLES = {
    "records.json": "records",
    "expected.json": "table",
    "expected-corrections.json": "corrections",
    "spec.md": "spec",
    "build-descriptors.sh": "source",
}


def digest(path, binary):
    with io.open(path, "rb") as handle:
        raw = handle.read()
    if not binary:
        raw = raw.replace(b"\r\n", b"\n")
    return hashlib.sha256(raw).hexdigest()


def classify(name):
    """Вернуть запись манифеста без дайджеста."""
    ext = os.path.splitext(name)[1]
    m = SCHEMA_NAME.match(name)
    if m and ext in NOTATIONS:
        return {"role": "schema",
                "notation": NOTATIONS[ext],
                "version": int(m.group(1)),
                "change": m.group(2) or ""}
    if m and ext == ".proto":
        # Исходник, из которого собран дескриптор: сам по себе схемой для
        # пробы не является, но принадлежит стенду.
        return {"role": "source"}
    if name in NON_SCHEMA_ROLES:
        return {"role": NON_SCHEMA_ROLES[name]}
    raise SystemExit(
        "не знаю, чем считать файл %r: добавь его в NON_SCHEMA_ROLES или "
        "убери из каталога стенда" % name)


def build():
    files = {}
    for name in sorted(os.listdir(ROOT)):
        path = os.path.join(ROOT, name)
        if os.path.isdir(path) or name == "manifest.json":
            continue
        ext = os.path.splitext(name)[1]
        binary = ext in BINARY_EXT
        entry = classify(name)
        entry["digest"] = digest(path, binary)
        entry["content"] = "binary" if binary else "text"
        # порядок ключей в записи — ради читаемости дифа
        files[name] = {k: entry[k] for k in
                       ("digest", "role", "content", "notation", "version", "change")
                       if k in entry}
    return {"algorithm": "sha256", "files": files}


if __name__ == "__main__":
    manifest = build()
    out = os.path.join(ROOT, "manifest.json")
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(manifest, ensure_ascii=False, indent=2,
                                sort_keys=False) + "\n")
    print("записано %s: %d файлов" % (out, len(manifest["files"])))
