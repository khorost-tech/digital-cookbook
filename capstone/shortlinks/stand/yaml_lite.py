"""Разбиение slo_test.yml на файлы по сценарию без зависимости от PyYAML.

Файл устроен просто: шапка (rule_files, evaluation_interval, tests:) и сценарии, каждый с
«  - name:» в начале. Шапка повторяется в каждом файле, путь к правилам — абсолютный.
"""


def split_tests(text):
    head, _, body = text.partition("\ntests:\n")
    head = head.replace("rule_files: [slo.yml]", "rule_files: [/p/slo.yml]")
    chunks, cur = [], []
    for line in body.splitlines():
        if line.startswith("  - name:") and cur:
            chunks.append(cur)
            cur = []
        cur.append(line)
    chunks.append(cur)
    if len(chunks) != 6:
        raise RuntimeError("в slo_test.yml ожидалось шесть сценариев, найдено %d" % len(chunks))
    return [head + "\ntests:\n" + "\n".join(c) + "\n" for c in chunks]
